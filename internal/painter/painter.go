package painter

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// livePanesTTL bounds how stale the pane-liveness set may be for the
	// event path; the sweep refreshes it every interval anyway.
	livePanesTTL = 5 * time.Minute
	// failureRetryCooldown bounds the failure brake: a braked report retries
	// at this cadence instead of never — SendReport cannot distinguish a
	// transient transport failure from a permanent rejection, and "never"
	// would strand a near-static row past its TTL (herdr drops the metadata,
	// the pane goes dark with no content change coming to fix it).
	failureRetryCooldown = 5 * time.Minute
	// deadSweepsBeforeRetire is how many consecutive sweeps must find every
	// bound socket unreachable before the painter exits — 3 sweeps ≈ 3
	// minutes, long enough to ride out a herdr restart.
	deadSweepsBeforeRetire = 3
)

// sessState is the in-memory dedup state for one session. Its own mutex
// covers the whole compose→decide→send→record window — Repaint is reachable
// concurrently from debounce timers, sweeps, and the CLI (statusd used a
// flock for exactly this window).
type sessState struct {
	mu               sync.Mutex
	hash             string
	sentAt           time.Time
	sockDev, sockIno uint64
	// failedHash + the socket identity it failed against: don't hammer the
	// exact content herdr rejected. The brake releases on a moved socket
	// inode (restarted herdr, metadata gone) and expires after
	// failureRetryCooldown (transient failures must not outlast the lease).
	failedHash           string
	failedDev, failedIno uint64
	failedAt             time.Time

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

	// Pane-liveness sets from the last snapshot fetch (sweep-refreshed), one
	// per herdr socket: a cache binding whose pane is gone must not be
	// painted — 48h of session churn otherwise leaves hundreds of dead
	// bindings fighting live panes. Keyed by socket because pane ids are
	// per-server (two servers both have a w1:p1), and because a host runs
	// one painter across every herdr session.
	livePanes map[string]livePaneSet

	// sweepMu makes acquire/release/record one critical section — two
	// independent atomics left a window where a coalesced request landed
	// after the release check and stranded until the next ticker.
	sweepMu      sync.Mutex
	sweeping     bool
	sweepPending bool

	// deadSweeps counts consecutive sweeps in which bindings existed and not
	// one of their sockets answered. quit fires when that count hits the
	// threshold — see retire().
	deadSweeps int
	quit       chan struct{}
	quitOnce   sync.Once
}

func New(cfgPath string, logger *log.Logger) *Painter {
	p := &Painter{
		Log:      logger,
		CfgPath:  cfgPath,
		sessions: map[string]*sessState{},
		timers:   map[string]*time.Timer{},
		quit:     make(chan struct{}),
	}
	p.reloadConfig() // eager: one-shot callers (paint) never call Run
	return p
}

// Run is the resident loop; it returns when ctx is canceled.
func (p *Painter) Run(ctx context.Context) error {
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
	go p.Sweep() // start = repaint all (fresh memory always sends)

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-p.quit:
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
			// Off the event loop: a sweep over a large fleet must never
			// stall fsnotify delivery (the channel is unbuffered).
			go p.Sweep()
		}
	}
}

func (p *Painter) handleEvent(ev fsnotify.Event) {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	name := filepath.Clean(ev.Name)
	if p.isConfigEvent(name) {
		// Re-resolve before reloading: authoring ccc-herdr.star next to the
		// live ccc-herdr.toml (or deleting it again) switches the effective
		// file, and painting on from the dead path would be a silent no-op.
		resolved := ConfigPath()
		p.mu.Lock()
		if resolved != p.CfgPath {
			p.Log.Printf("config path now %s", resolved)
			p.CfgPath = resolved
		}
		p.mu.Unlock()
		p.Log.Printf("config changed — reloading")
		p.reloadConfig()
		go p.Sweep()
		return
	}
	if !strings.HasSuffix(ev.Name, ".json") || filepath.Dir(name) != facts.CacheDir() {
		return
	}
	sid := strings.TrimSuffix(filepath.Base(ev.Name), ".json")
	p.debounced(sid)
}

// isConfigEvent matches the active config path plus its format sibling in
// the same directory — the pair a format migration flips between.
func (p *Painter) isConfigEvent(name string) bool {
	p.mu.Lock()
	current := p.CfgPath
	p.mu.Unlock()
	if name == current {
		return true
	}
	base := filepath.Base(name)
	return (base == "ccc-herdr.star" || base == "ccc-herdr.toml") && filepath.Dir(name) == filepath.Dir(current)
}

// debounced schedules one repaint per session per debounce window. The
// callback deletes only ITS OWN map entry — Reset can revive an
// already-fired AfterFunc, and its late invocation must not delete a
// successor timer's entry.
func (p *Painter) debounced(sid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.timers[sid]; ok {
		t.Reset(debounce)
		return
	}
	var t *time.Timer
	t = time.AfterFunc(debounce, func() {
		p.mu.Lock()
		if p.timers[sid] == t {
			delete(p.timers, sid)
		}
		p.mu.Unlock()
		p.Repaint(sid, false)
	})
	p.timers[sid] = t
}

func (p *Painter) reloadConfig() {
	p.mu.Lock()
	path := p.CfgPath
	p.mu.Unlock()
	cfg := LoadConfig(path, func(format string, args ...any) {
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

// livePaneSet is one herdr server's pane set, with the time it was read.
type livePaneSet struct {
	panes map[string]bool
	at    time.Time
}

// refreshLivePanes snapshots EVERY socket the cache binds to — one snapshot
// per socket per sweep. A host runs one painter over every herdr session, so
// asking only $HERDR_SOCKET_PATH left every other server's stale bindings
// unvetoed: they were sent and rejected `pane_not_found` until the failure
// brake cooled them. A socket that answers nothing is omitted, not empty:
// unreachable means UNKNOWN (never dead), and sends fail on their own.
func (p *Painter) refreshLivePanes(sockets []string) map[string]map[string]bool {
	live := make(map[string]map[string]bool, len(sockets))
	for _, sock := range sockets {
		snap, err := herdr.NewClientFor(sock).Snapshot()
		if err != nil || snap == nil {
			continue
		}
		panes := make(map[string]bool, len(snap.Panes))
		for _, pane := range snap.Panes {
			panes[pane.PaneID] = true
		}
		live[sock] = panes
	}
	now := time.Now()
	p.mu.Lock()
	if p.livePanes == nil {
		p.livePanes = map[string]livePaneSet{}
	}
	for sock, panes := range live {
		p.livePanes[sock] = livePaneSet{panes: panes, at: now}
	}
	p.mu.Unlock()
	return live
}

// paneKnownDead consults the last liveness set for the event path; unknown
// or stale data — or a socket never snapshotted — never blocks a paint (the
// dedup brake handles dead sends).
func (p *Painter) paneKnownDead(paneID, sockPath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	set, ok := p.livePanes[sockPath]
	if !ok || time.Since(set.at) > livePanesTTL {
		return false
	}
	return !set.panes[paneID]
}

// Sweep repaints every live session: lease renewal, decay, takeover.
// Overlapping requests coalesce into a deferred re-run — reconnect and
// config edges are edge-triggered, so a dropped request would lose the edge.
func (p *Painter) Sweep() {
	p.sweepMu.Lock()
	if p.sweeping {
		p.sweepPending = true
		p.sweepMu.Unlock()
		return
	}
	p.sweeping = true
	for {
		p.sweepPending = false
		p.sweepMu.Unlock()
		p.sweepOnce()
		p.sweepMu.Lock()
		if !p.sweepPending {
			break
		}
	}
	p.sweeping = false
	p.sweepMu.Unlock()
}

func (p *Painter) sweepOnce() {
	dir := facts.CacheDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	sessionMap := facts.LoadSessionMap() // once per sweep, not per session

	// Pass 1: read fresh bound caches and collect the sockets they bind to.
	type claimant struct {
		sid   string
		cache facts.Cache
	}
	type paneKey struct{ sock, pane string }
	var bound []claimant
	sockSet := map[string]bool{}
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
		cache, err := facts.ReadCache(filepath.Join(dir, name))
		if err != nil || cache.HerdrPaneID == "" || cache.HerdrSocketPath == "" {
			continue
		}
		bound = append(bound, claimant{sid, cache})
		sockSet[cache.HerdrSocketPath] = true
	}
	sockets := make([]string, 0, len(sockSet))
	for sock := range sockSet {
		sockets = append(sockets, sock)
	}
	live := p.refreshLivePanes(sockets)
	p.retireIfHerdrIsGone(len(sockets), len(live))

	// Pass 2: veto bindings whose pane its OWN server says is gone, then
	// keep one winner per (socket, pane). A pane claimed by several session
	// caches (churned panes re-used) is painted only by the claimant with
	// the most recent hook activity — same-source seq ordering would
	// otherwise let a dead session's row overwrite the live one's every
	// sweep. Keyed with the socket because pane ids are per-herdr-instance:
	// two instances both have a w1:p1.
	best := map[paneKey]claimant{}
	vetoed := 0
	for _, c := range bound {
		if panes, known := live[c.cache.HerdrSocketPath]; known && !panes[c.cache.HerdrPaneID] {
			vetoed++
			continue // pane is gone — labels decay via TTL
		}
		k := paneKey{c.cache.HerdrSocketPath, c.cache.HerdrPaneID}
		if cur, taken := best[k]; !taken || c.cache.LastHookEvent.After(cur.cache.LastHookEvent) {
			best[k] = c
		}
	}
	if vetoed > 0 {
		p.Log.Printf("sweep: %d session(s) skipped — bound pane no longer exists", vetoed)
	}
	for _, c := range best {
		p.repaint(c.sid, c.cache, sessionMap[c.sid], false)
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

// Repaint is the single-session entry (debounce path, CLI). force bypasses
// dedup and the pane-liveness veto.
func (p *Painter) Repaint(sid string, force bool) {
	cache, err := facts.ReadCache(filepath.Join(facts.CacheDir(), sid+".json"))
	if err != nil || cache.HerdrPaneID == "" || cache.HerdrSocketPath == "" {
		return
	}
	if !force && p.paneKnownDead(cache.HerdrPaneID, cache.HerdrSocketPath) {
		return
	}
	p.repaint(sid, cache, facts.LoadSessionMap()[sid], force)
}

// repaint composes and (dedup permitting) sends both reports. The per-
// session lock spans the whole sample→compose→decide→send→record window.
func (p *Painter) repaint(sid string, cache facts.Cache, entry facts.MapEntry, force bool) {
	cfg := p.config()
	if !cfg.Enabled {
		return
	}
	st := p.state(sid)
	st.mu.Lock()
	defer st.mu.Unlock()

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
		// The failure brake holds only against the same socket incarnation
		// (a moved inode is a restarted herdr that dropped all metadata) and
		// only inside the cooldown — a transient failure must not outlive
		// the lease and strand a near-static row dark forever. The cooldown
		// honors that literally: it never exceeds half a configured lease.
		cooldown := failureRetryCooldown
		if cfg.TTL > 0 && cfg.TTL/2 < cooldown {
			cooldown = cfg.TTL / 2
		}
		braked := hash == st.failedHash && dev == st.failedDev && ino == st.failedIno &&
			now.Sub(st.failedAt) < cooldown
		if braked {
			return
		}
		if !shouldSend(st, hash, dev, ino, now, cfg.TTL) {
			return
		}
	}
	if err := herdr.SendReport(cache.HerdrSocketPath, report); err != nil {
		st.failedHash, st.failedDev, st.failedIno, st.failedAt = hash, dev, ino, now
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
	renew := pending > 0 && cfg.AUQLease > 0 && now.Sub(st.auqSentAt) > cfg.AUQLease/4
	if !force && !edge && !renew {
		return
	}
	err := herdr.SendReport(cache.HerdrSocketPath, ComposeAUQ(cache.HerdrPaneID, cfg, pending))
	if err != nil && cfg.AUQLease <= 0 {
		// No lease means no TTL self-heal exists: keep the edge armed so the
		// next cache write retries, or the label is lost for the question's
		// whole life.
		p.Log.Printf("auq %s → %s: %v", cache.SessionID, cache.HerdrPaneID, err)
		return
	}
	// Advance state even on failure: with a lease, the TTL is the AUQ
	// self-heal contract, so a failed paint retries at renewal cadence (or
	// the next real edge) — never per cache write, which on a dead pane is
	// a permanent resend storm.
	st.lastPending = pending
	st.auqSentAt = now
	if err != nil {
		p.Log.Printf("auq %s → %s: %v", cache.SessionID, cache.HerdrPaneID, err)
	}
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
// tokens, display_agent, or top-level string params. A star config's
// render(v) is opaque, so it is assumed to reference everything; the
// HERDR_TITLE fetch behind that answer stays throttled to titleFetchTTL.
func configReferencesVar(cfg Config, name string) bool {
	if cfg.Star != nil {
		return true
	}
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
// probe: herdr drops all metadata on restart, so every reconnect triggers a
// full repaint. Event content is irrelevant — the connection lifecycle is
// the signal; dedup's socket-inode check backstops anything missed.
func (p *Painter) herdrLink(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		socketPath := p.linkSocket()
		if socketPath == "" {
			return
		}
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			first = false
			if !sleepCtx(ctx, 2*time.Second) {
				return
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
		// Floor between redials: an accept-then-close herdr must not spin
		// the loop (each reconnect arms a full-fleet sweep).
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

// retireIfHerdrIsGone exits the painter once every socket its bindings name
// has stopped answering. The painter outlives the session whose [[startup]]
// hook launched it — deliberately, since it serves every session on the host
// — but "every herdr on this host is gone" leaves a resident with nothing to
// paint, holding the flock, invisible to `herdr session list`. Coming back is
// automatic: any herdr start runs the startup hook again.
//
// Threshold, not first miss: a wedged or restarting server must not retire a
// painter the rest of the fleet still needs, and zero BINDINGS (fresh host,
// hooks not fired yet) is not zero herdrs.
func (p *Painter) retireIfHerdrIsGone(sockets, reachable int) {
	p.mu.Lock()
	if sockets == 0 || reachable > 0 {
		p.deadSweeps = 0
		p.mu.Unlock()
		return
	}
	p.deadSweeps++
	n := p.deadSweeps
	p.mu.Unlock()
	if n < deadSweepsBeforeRetire {
		p.Log.Printf("no herdr answered on any of %d socket(s) — retiring after %d more quiet sweep(s)",
			sockets, deadSweepsBeforeRetire-n)
		return
	}
	p.Log.Printf("no herdr left on this host after %d sweeps — exiting; a herdr start re-runs the [[startup]] hook", n)
	p.quitOnce.Do(func() { close(p.quit) })
}

// linkSocket picks the endpoint for the liveness probe, re-resolved on every
// redial. The painter outlives the herdr session whose [[startup]] hook
// launched it, so $HERDR_SOCKET_PATH can name a server that is never coming
// back; pinning to it would cost the reconnect edge (repaint-all after a
// herdr restart) for every OTHER session, forever. Falls back to the most
// recently reachable socket the sweep found — no extra I/O, the sweep
// already talks to all of them.
func (p *Painter) linkSocket() string {
	if env := os.Getenv("HERDR_SOCKET_PATH"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
	}
	p.mu.Lock()
	best, bestAt := "", time.Time{}
	for sock, set := range p.livePanes {
		if set.at.After(bestAt) {
			best, bestAt = sock, set.at
		}
	}
	p.mu.Unlock()
	if best != "" {
		return best
	}
	if env := os.Getenv("HERDR_SOCKET_PATH"); env != "" {
		return env // gone right now, but it is the only name we have
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// StateDir is the painter's own directory (lock, log), a sibling of the
// statusd cache dir. "" when no cache dir resolves.
func StateDir() string {
	base := facts.CacheDir()
	if base == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(base), "ccc-herdr")
}

// LogPath is where a detached painter writes (`painter start`). Never /tmp:
// the log outlives the boot that produced the bug.
func LogPath() string {
	dir := StateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "painter.log")
}

// AcquireSingleton takes the painter flock; ok=false means another painter
// is live (two same-source writers would flap seq ordering).
func AcquireSingleton() (release func(), ok bool) {
	dir := StateDir()
	if dir == "" {
		return func() {}, true
	}
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
	// Stamp the pid INSIDE the lock: an upgrade has to reach the incumbent,
	// and the flock alone names nobody. Best-effort — a lock file that fails
	// to write still locks, takeover just falls back to "cannot find it".
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, true
}

// LockPath is the painter's flock file. Its CONTENT is load-bearing: the pid
// stamped inside is how a newly installed binary reaches the incumbent it has
// to replace.
func LockPath() string {
	dir := StateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "painter.lock")
}

// IncumbentPID reads the pid the live painter stamped in its lock file. 0 =
// no lock file, an unparsable one (a build older than pid-stamping leaves it
// EMPTY), or a pid nobody answers to.
func IncumbentPID() int {
	path := LockPath()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0
	}
	if syscall.Kill(pid, 0) != nil { // gone, or not ours to signal
		return 0
	}
	return pid
}

// Takeover reports what StopIncumbent found. Unidentified and Stuck are
// FAILURES the caller must surface: an upgrade that cannot replace the
// running painter leaves the host painting with the old code, and silence
// there is the bug takeover exists to prevent.
type Takeover int

const (
	NoIncumbent  Takeover = iota // the lock was free
	Stopped                      // incumbent exited, lock free again
	Unidentified                 // lock held, holder cannot be named
	Stuck                        // holder signalled, lock still held
)

// StopIncumbent asks the live painter to exit and waits for it to release the
// lock. Upgrades need this: the flock makes a NEW binary a silent no-op while
// the OLD process keeps painting, so "installed" and "running" drift apart
// with nothing on screen to say so.
//
// The pid stamp is the first answer and the only exact one, but it is absent
// for exactly the incumbents that most need replacing — those from builds
// before stamping existed. So an unstamped lock falls back to naming the
// holder from the process table, and refuses when it cannot: plugin roots are
// keyed by SOURCE not commit (`plugins/github/<id>-<hash>` survives an
// upgrade, measured on ws-nyc-2), so after an upgrade the new
// binary cannot rely on a path match either: several installs share one
// source-keyed root, so a path says "a ccc-herdr", not "the incumbent".
func StopIncumbent(wait time.Duration) (Takeover, int) {
	if release, free := AcquireSingleton(); free {
		release()
		return NoIncumbent, 0
	}
	pid := IncumbentPID()
	if pid == 0 {
		pid = findIncumbent()
	}
	if pid == 0 {
		return Unidentified, 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return Unidentified, pid
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if release, free := AcquireSingleton(); free {
			release()
			return Stopped, pid
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Stuck, pid
}

// findIncumbent names the holder of an unstamped lock. lsof answers exactly
// (it reads the open file); the process table is the fallback and stays
// conservative — several candidates means we do not know, and SIGTERMing the
// wrong painter (another UCC_HOME on the same host) is worse than refusing.
func findIncumbent() int {
	if pid := lsofLockHolder(); pid != 0 {
		return pid
	}
	if pid := procLockHolder(); pid != 0 {
		return pid
	}
	if cands := PainterProcesses(); len(cands) == 1 {
		return cands[0]
	}
	return 0
}

// procLockHolder answers exactly on Linux without lsof — the shape of a
// minimal NixOS host, and the only tier left when a second painter (another
// UCC_HOME) makes the process-table heuristic ambiguous.
func procLockHolder() int { return procLockHolderFor(PainterProcesses()) }

func procLockHolderFor(pids []int) int {
	path := LockPath()
	if path == "" {
		return 0
	}
	for _, pid := range pids {
		fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // not Linux, or not ours to read
		}
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && target == path {
				return pid
			}
		}
	}
	return 0
}

func lsofLockHolder() int {
	path := LockPath()
	if path == "" {
		return 0
	}
	out, err := exec.Command("lsof", "-t", path).Output() // absent on minimal hosts
	if err != nil {
		return 0
	}
	self := os.Getpid()
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil && pid != self {
			return pid
		}
	}
	return 0
}

// PainterProcesses lists running `ccc-herdr painter run` processes by pid.
// Matched on the BASENAME: a dev checkout, a linked plugin and an installed
// one all run the same tool from different paths.
func PainterProcesses() []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=,args=").Output()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == self {
			continue
		}
		if filepath.Base(fields[1]) == "ccc-herdr" && fields[2] == "painter" && fields[3] == "run" {
			pids = append(pids, pid)
		}
	}
	return pids
}
