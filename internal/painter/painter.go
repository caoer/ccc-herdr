package painter

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/caoer/ccc-herdr/internal/facts"
	"github.com/caoer/ccc-herdr/internal/herdr"
)

const (
	// debounce collapses the burst of cache writes one turn produces.
	debounce = 300 * time.Millisecond
	// sweepInterval drives lease renewal and decay; dedup makes a no-change
	// sweep nearly free.
	sweepInterval = 60 * time.Second
	// refreshHorizonCap bounds identity lease renewal: an active session
	// re-sends at most once an hour under no-change, always inside the TTL.
	refreshHorizonCap = time.Hour
	// titleFetchTTL throttles the pane.get read behind {{HERDR_TITLE}}.
	titleFetchTTL = 5 * time.Second
	// staleSession stops sweeping sessions whose cache went quiet — their
	// labels decay via TTL; a new cache write revives them instantly.
	staleSession = 48 * time.Hour
)

// sessState is the in-memory dedup state for one session. The statusd
// sidecar files existed only because its writers were short-lived processes;
// a resident painter holds this in memory and repaints all on start.
type sessState struct {
	hash             string
	sentAt           time.Time
	sockDev, sockIno uint64
	failedHash       string // last compose that herdr rejected — don't retry until content changes

	title   string
	titleAt time.Time

	lastPending int // -1 = unknown → first AUQ decision always sends
	auqSentAt   time.Time
}

// Painter is the resident renderer: cache facts in, herdr metadata out.
type Painter struct {
	Log     *log.Logger
	CfgPath string

	mu       sync.Mutex
	cfg      Config
	sessions map[string]*sessState
	timers   map[string]*time.Timer
}

func New(cfgPath string, logger *log.Logger) *Painter {
	p := &Painter{
		Log:      logger,
		CfgPath:  cfgPath,
		sessions: map[string]*sessState{},
		timers:   map[string]*time.Timer{},
	}
	p.reloadConfig() // eager: one-shot callers (paint) never call Run
	return p
}

// Run is the resident loop; it returns when ctx is canceled.
func (p *Painter) Run(ctx context.Context) error {
	p.reloadConfig()

	cacheDir := facts.CacheDir()
	if cacheDir == "" {
		p.Log.Printf("no cache dir resolvable (UCC_HOME unset?) — nothing to paint")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			if err := watcher.Add(cacheDir); err != nil {
				p.Log.Printf("watch %s: %v — running on sweeps only", cacheDir, err)
			}
		}
	}
	if dir := filepath.Dir(p.CfgPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
		if err := watcher.Add(dir); err != nil {
			p.Log.Printf("watch config dir %s: %v — config reloads on restart only", dir, err)
		}
	}

	go p.herdrLink(ctx)

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	p.Sweep() // start = repaint all (fresh memory always sends)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			p.handleEvent(ev)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			p.Log.Printf("watcher: %v", err)
		case <-ticker.C:
			p.Sweep()
		}
	}
}

func (p *Painter) handleEvent(ev fsnotify.Event) {
	if ev.Name == p.CfgPath && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
		p.Log.Printf("config changed — reloading")
		p.reloadConfig()
		go p.Sweep()
		return
	}
	if !strings.HasSuffix(ev.Name, ".json") || filepath.Dir(ev.Name) != facts.CacheDir() {
		return
	}
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	sid := strings.TrimSuffix(filepath.Base(ev.Name), ".json")
	p.debounced(sid)
}

// debounced schedules one repaint per session per debounce window.
func (p *Painter) debounced(sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.timers[sid]; ok {
		t.Reset(debounce)
		return
	}
	p.timers[sid] = time.AfterFunc(debounce, func() {
		p.mu.Lock()
		delete(p.timers, sid)
		p.mu.Unlock()
		p.Repaint(sid, false)
	})
}

func (p *Painter) reloadConfig() {
	cfg := LoadConfig(p.CfgPath, func(format string, args ...any) {
		p.Log.Printf("config: "+format, args...)
	})
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
}

func (p *Painter) config() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

func (p *Painter) state(sid string) *sessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[sid]
	if !ok {
		s = &sessState{lastPending: -1}
		p.sessions[sid] = s
	}
	return s
}

// Sweep repaints every live session: lease renewal, decay, takeover.
func (p *Painter) Sweep() {
	dir := facts.CacheDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) > staleSession {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		seen[sid] = true
		p.Repaint(sid, false)
	}
	// Forget sessions whose cache file is gone — their labels TTL out.
	p.mu.Lock()
	for sid := range p.sessions {
		if !seen[sid] {
			delete(p.sessions, sid)
		}
	}
	p.mu.Unlock()
}

// Repaint composes and (dedup permitting) sends both reports for one session.
func (p *Painter) Repaint(sid string, force bool) {
	cfg := p.config()
	if !cfg.Enabled {
		return
	}
	cache, err := facts.ReadCache(filepath.Join(facts.CacheDir(), sid+".json"))
	if err != nil || cache.HerdrPaneID == "" || cache.HerdrSocketPath == "" {
		return
	}
	entry := facts.LoadSessionMap()[sid]
	st := p.state(sid)

	vars := facts.Vars(sid, cache, entry)
	if configReferencesVar(cfg, "HERDR_TITLE") {
		vars["HERDR_TITLE"] = p.resolveTitle(st, cache.HerdrSocketPath, cache.HerdrPaneID, force)
	}

	p.paintIdentity(st, cfg, cache, vars, force)
	p.paintAUQ(st, cfg, cache, force)
}

func (p *Painter) paintIdentity(st *sessState, cfg Config, cache facts.Cache, vars map[string]string, force bool) {
	report, ok := ComposeIdentity(cache.HerdrPaneID, cfg, vars, func(format string, args ...any) {
		p.Log.Printf(format, args...)
	})
	if !ok {
		return
	}
	dev, ino, sockOK := herdr.SocketIdentity(cache.HerdrSocketPath)
	if !sockOK {
		return // socket gone — a send could only fail
	}
	hash := report.ContentHash()
	now := time.Now()
	if !force {
		if hash == st.failedHash {
			return // herdr rejected this exact content — wait for a change
		}
		if !shouldSend(st, hash, dev, ino, now, cfg.TTL) {
			return
		}
	}
	if err := herdr.SendReport(cache.HerdrSocketPath, report); err != nil {
		st.failedHash = hash
		p.Log.Printf("identity %s → %s: %v", cache.SessionID, cache.HerdrPaneID, err)
		return
	}
	st.failedHash = ""
	st.hash = hash
	st.sentAt = now
	st.sockDev, st.sockIno = dev, ino
}

func (p *Painter) paintAUQ(st *sessState, cfg Config, cache facts.Cache, force bool) {
	if cfg.AUQLabel == "" {
		return
	}
	pending := cache.AUQPending
	now := time.Now()
	edge := pending != st.lastPending // includes the -1 fresh state: takeover clears stale labels
	renew := pending > 0 && now.Sub(st.auqSentAt) > cfg.AUQLease/4
	if !force && !edge && !renew {
		return
	}
	report := ComposeAUQ(cache.HerdrPaneID, cfg, pending)
	if err := herdr.SendReport(cache.HerdrSocketPath, report); err != nil {
		p.Log.Printf("auq %s → %s: %v", cache.SessionID, cache.HerdrPaneID, err)
		return
	}
	st.lastPending = pending
	st.auqSentAt = now
}

// shouldSend is the dedup decision: content changed, herdr restarted (socket
// inode moved — herdr drops all metadata), or the lease needs renewal.
func shouldSend(st *sessState, hash string, dev, ino uint64, now time.Time, ttl time.Duration) bool {
	if st.hash == "" || hash != st.hash {
		return true
	}
	if dev != st.sockDev || ino != st.sockIno {
		return true
	}
	if ttl > 0 {
		horizon := ttl / 2
		if horizon > refreshHorizonCap {
			horizon = refreshHorizonCap
		}
		if now.Sub(st.sentAt) > horizon {
			return true
		}
	}
	return false
}

// resolveTitle returns the HERDR_TITLE value: a pane fact only herdr knows,
// read back and throttled. A failed fetch keeps the last-known value and
// still advances the clock — a wedged herdr is probed once per window.
func (p *Painter) resolveTitle(st *sessState, socketPath, paneID string, force bool) string {
	now := time.Now()
	if age := now.Sub(st.titleAt); !force && !st.titleAt.IsZero() && age >= 0 && age < titleFetchTTL {
		return st.title
	}
	if title, ok := herdr.FetchPaneTitle(socketPath, paneID); ok {
		st.title = title
	}
	st.titleAt = now
	return st.title
}

// configReferencesVar reports whether any template mentions {{name}} —
// tokens, display_agent, or top-level string params.
func configReferencesVar(cfg Config, name string) bool {
	needle := "{{" + name + "}}"
	if strings.Contains(cfg.DisplayAgent, needle) {
		return true
	}
	for _, tmpl := range cfg.Tokens {
		if strings.Contains(tmpl, needle) {
			return true
		}
	}
	for _, v := range cfg.Params {
		if s, ok := v.(string); ok && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// herdrLink holds a subscribed connection to the herdr socket as a liveness
// probe: herdr drops all metadata on restart, so every (re)connect triggers a
// full repaint. Event content is irrelevant — the connection lifecycle is the
// signal; dedup's socket-inode check backstops anything missed.
func (p *Painter) herdrLink(ctx context.Context) {
	socketPath := os.Getenv("HERDR_SOCKET_PATH")
	if socketPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		socketPath = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	first := true
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			first = false
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		sub, _ := json.Marshal(map[string]any{
			"id":     "ccc-herdr:events",
			"method": "events.subscribe",
			"params": map[string]any{"subscriptions": []map[string]string{{"type": "pane.closed"}}},
		})
		_, _ = conn.Write(append(sub, '\n'))
		if !first {
			p.Log.Printf("herdr is back — repainting all panes")
			go p.Sweep()
		}
		first = false
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				break
			}
		}
		conn.Close()
	}
}

// AcquireSingleton takes the painter flock; ok=false means another painter
// is live (two same-source writers would flap seq ordering).
func AcquireSingleton() (release func(), ok bool) {
	base := facts.CacheDir()
	if base == "" {
		return func() {}, true
	}
	dir := filepath.Join(filepath.Dir(base), "ccc-herdr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, true
	}
	f, err := os.OpenFile(filepath.Join(dir, "painter.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}
