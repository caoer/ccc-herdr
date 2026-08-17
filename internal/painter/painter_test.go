package painter

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caoer/ccc-herdr/internal/facts"
)

// shortTmpDir mints a directory under /tmp — unix socket paths cap at ~104
// bytes on macOS, and t.TempDir() under /var/folders blows past it.
func shortTmpDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// stubHerdr accepts connections and answers every request line with one
// canned reply that serves both as a send ack and as a session.snapshot
// (panes p1/p2 live). Received lines land on the channel.
func stubHerdr(t *testing.T) (string, chan string) {
	t.Helper()
	sock := filepath.Join(shortTmpDir(t), "herdr.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	lines := make(chan string, 256)
	reply := []byte(`{"id":"stub","result":{"snapshot":{"panes":[{"pane_id":"p1"},{"pane_id":"p2"}]}}}` + "\n")
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				dec := json.NewDecoder(c)
				for {
					var raw json.RawMessage
					if err := dec.Decode(&raw); err != nil {
						return
					}
					select {
					case lines <- string(raw):
					default:
					}
					if _, err := c.Write(reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sock, lines
}

func writeSessCache(t *testing.T, sid, paneID, sock string, pending int) {
	t.Helper()
	body := map[string]any{
		"session_id":        sid,
		"custom_title":      "reader",
		"last_known_role":   "worker",
		"launch_profile":    "test-profile",
		"herdr_pane_id":     paneID,
		"herdr_socket_path": sock,
	}
	if pending > 0 {
		body["auq_pending"] = pending
	}
	data, _ := json.Marshal(body)
	if err := os.WriteFile(filepath.Join(facts.CacheDir(), sid+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestPainter(t *testing.T) (*Painter, string, chan string) {
	t.Helper()
	t.Setenv("CCC_CACHE_DIR", t.TempDir())
	t.Setenv("UCC_HOME", t.TempDir())
	sock, lines := stubHerdr(t)
	t.Setenv("HERDR_SOCKET_PATH", sock)
	logger := log.New(os.Stderr, "test ", 0)
	return New(filepath.Join(t.TempDir(), "ccc-herdr.toml"), logger), sock, lines
}

func drain(lines chan string) []string {
	var got []string
	for {
		select {
		case l := <-lines:
			got = append(got, l)
		case <-time.After(150 * time.Millisecond):
			return got
		}
	}
}

func TestRepaintSendsThenDedups(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	writeSessCache(t, "aaaabbbbccccdddd", "p1", sock, 0)

	p.Repaint("aaaabbbbccccdddd", false)
	first := drain(lines)
	if len(first) != 2 {
		t.Fatalf("first repaint: want identity+auq (2 sends), got %d: %v", len(first), first)
	}

	p.Repaint("aaaabbbbccccdddd", false)
	if again := drain(lines); len(again) != 0 {
		t.Fatalf("unchanged repaint must dedup to zero sends, got %v", again)
	}
}

func TestSweepPicksNewestClaimantPerPane(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	// Two sessions claim pane p1; the one with newer hook activity wins.
	old := map[string]any{
		"session_id": "old0000000000000", "herdr_pane_id": "p1", "herdr_socket_path": sock,
		"last_known_role": "worker", "last_hook_event_time": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
	}
	fresh := map[string]any{
		"session_id": "new0000000000000", "herdr_pane_id": "p1", "herdr_socket_path": sock,
		"last_known_role": "worker", "last_hook_event_time": time.Now().Format(time.RFC3339),
	}
	for sid, body := range map[string]map[string]any{"old0000000000000": old, "new0000000000000": fresh} {
		data, _ := json.Marshal(body)
		if err := os.WriteFile(filepath.Join(facts.CacheDir(), sid+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A third session bound to a pane the snapshot does not list: never painted.
	writeSessCache(t, "dead000000000000", "p9", sock, 0)

	p.Sweep()
	sent := drain(lines)
	// snapshot request + the winner's identity/auq only
	var ids []string
	for _, l := range sent {
		if !json.Valid([]byte(l)) {
			continue
		}
		var m struct {
			Params struct {
				Tokens map[string]any `json:"tokens"`
			} `json:"params"`
		}
		_ = json.Unmarshal([]byte(l), &m)
		if id, ok := m.Params.Tokens["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 || ids[0] != "new00000" {
		t.Fatalf("exactly the newest claimant must paint pane p1, got token ids %v (wire %d lines)", ids, len(sent))
	}
	for _, l := range sent {
		if json.Valid([]byte(l)) && strings.Contains(l, `"pane_id":"p9"`) {
			t.Fatalf("dead pane p9 must not be painted: %s", l)
		}
	}
}

func TestConcurrentSweepAndRepaint(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	// Panes alternate p1/p2 (both live in the stub snapshot) so the sweep's
	// claimant election paints BOTH panes and genuinely contends with the
	// direct Repaint goroutines on the same sessStates.
	for i := 0; i < 6; i++ {
		writeSessCache(t, fmt.Sprintf("sess%d000000000000", i), fmt.Sprintf("p%d", i%2+1), sock, i%2)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				p.Sweep()
			} else {
				p.Repaint(fmt.Sprintf("sess%d000000000000", n%6), false)
			}
		}(i)
	}
	wg.Wait()
	drain(lines) // no assertion on counts — this test exists for -race
}

func TestShouldSendBranches(t *testing.T) {
	now := time.Now()
	st := &sessState{hash: "h", sentAt: now, sockDev: 1, sockIno: 2}

	if shouldSend(st, "h", 1, 2, now, time.Hour) {
		t.Fatal("unchanged content + socket + fresh lease must not send")
	}
	if !shouldSend(st, "changed", 1, 2, now, time.Hour) {
		t.Fatal("content change must send")
	}
	if !shouldSend(st, "h", 1, 3, now, time.Hour) {
		t.Fatal("socket inode move (herdr restart) must send")
	}
	if !shouldSend(st, "h", 1, 2, now.Add(31*time.Minute), time.Hour) {
		t.Fatal("past the ttl/2 horizon must renew")
	}
	if shouldSend(st, "h", 1, 2, now.Add(45*time.Minute), 0) {
		t.Fatal("ttl 0 must never renew on time alone")
	}
	// horizon cap: ttl 24h → horizon clamps to 1h, not 12h
	if !shouldSend(st, "h", 1, 2, now.Add(61*time.Minute), 24*time.Hour) {
		t.Fatal("refresh horizon must cap at 1h")
	}
}

func TestPaintAUQStateMachine(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	cfg := p.config()
	st := &sessState{lastPending: -1}
	cache := facts.Cache{SessionID: "s", HerdrPaneID: "p1", HerdrSocketPath: sock, AUQPending: 0}

	p.paintAUQ(st, cfg, cache, false)
	if got := drain(lines); len(got) != 1 || !strings.Contains(got[0], "clear_state_labels") {
		t.Fatalf("takeover (lastPending=-1, pending=0) must send one clear, got %v", got)
	}

	p.paintAUQ(st, cfg, cache, false)
	if got := drain(lines); len(got) != 0 {
		t.Fatalf("no edge, no renew — no send, got %v", got)
	}

	cache.AUQPending = 2
	p.paintAUQ(st, cfg, cache, false)
	if got := drain(lines); len(got) != 1 || !strings.Contains(got[0], `"blocked":"AUQ"`) {
		t.Fatalf("pending edge must paint the label, got %v", got)
	}

	// Send failure must still advance state — a dead pane retries at
	// renewal cadence, never per cache write.
	cacheDead := cache
	cacheDead.HerdrSocketPath = filepath.Join(shortTmpDir(t), "gone.sock")
	cacheDead.AUQPending = 3
	p.paintAUQ(st, cfg, cacheDead, false)
	if st.lastPending != 3 {
		t.Fatalf("failed send must advance lastPending, got %d", st.lastPending)
	}
	p.paintAUQ(st, cfg, cacheDead, false)
	if got := drain(lines); len(got) != 0 {
		t.Fatalf("failed edge must not resend before renewal window, got %v", got)
	}
}

func TestIdentityFailureBrakeReleasesOnSocketMove(t *testing.T) {
	p, _, _ := newTestPainter(t)
	cfg := p.config()
	deadSock := filepath.Join(shortTmpDir(t), "dead.sock")
	// A path that stats but refuses connections: SocketIdentity succeeds,
	// dial fails — the C2 scenario (herdr crashed, socket file left behind).
	if err := os.WriteFile(deadSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	st := &sessState{lastPending: -1}
	cache := facts.Cache{SessionID: "s", HerdrPaneID: "p1", HerdrSocketPath: deadSock}
	vars := map[string]string{"SESSION_ID_SHORT": "s", "CCC_SESSION": "x", "ROLE": "worker", "NAME": "n", "TITLE": "n", "PROFILE": "pr", "PROFILE_IF_UNNAMED": ""}

	p.paintIdentity(st, cfg, cache, vars, false)
	if st.failedHash == "" {
		t.Fatal("refused dial must record the failure brake")
	}
	brake := st.failedHash

	// herdr "restarts": same path, new inode, live listener this time.
	os.Remove(deadSock)
	sock2, lines := stubHerdr(t)
	_ = sock2
	// Re-listen at the ORIGINAL path so the cache stays valid.
	ln2, err := net.Listen("unix", deadSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	go func() {
		for {
			c, err := ln2.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 65536)
				for {
					n, err := conn.Read(buf)
					if err != nil {
						return
					}
					select {
					case lines <- string(buf[:n]):
					default:
					}
					conn.Write([]byte(`{"id":"ok","result":{}}` + "\n"))
				}
			}(c)
		}
	}()

	p.paintIdentity(st, cfg, cache, vars, false)
	if st.failedHash == brake && st.hash == "" {
		t.Fatal("failure brake must release when the socket inode moves (herdr restart)")
	}
	if got := drain(lines); len(got) == 0 {
		t.Fatal("post-restart repaint must reach the wire")
	}
}

func TestCacheDirCleansTrailingSlash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCC_CACHE_DIR", dir+string(os.PathSeparator))
	if got := facts.CacheDir(); got != dir {
		t.Fatalf("CacheDir must clean trailing separators: %q", got)
	}
}

func TestIdentityBrakeHoldsThenCoolsDown(t *testing.T) {
	p, _, _ := newTestPainter(t)
	cfg := p.config()
	deadSock := filepath.Join(shortTmpDir(t), "dead.sock")
	if err := os.WriteFile(deadSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st := &sessState{lastPending: -1}
	cache := facts.Cache{SessionID: "s", HerdrPaneID: "p1", HerdrSocketPath: deadSock}
	vars := map[string]string{"SESSION_ID_SHORT": "s", "CCC_SESSION": "x", "ROLE": "worker", "NAME": "n", "TITLE": "n", "PROFILE": "pr", "PROFILE_IF_UNNAMED": ""}

	p.paintIdentity(st, cfg, cache, vars, false)
	if st.failedHash == "" {
		t.Fatal("failure must arm the brake")
	}
	firstFail := st.failedAt

	// Within the cooldown the brake holds: no state change on a repeat.
	p.paintIdentity(st, cfg, cache, vars, false)
	if st.failedAt != firstFail {
		t.Fatal("braked repeat must not re-attempt inside the cooldown")
	}

	// Past the cooldown the brake releases and the send is re-attempted
	// (still failing here — failedAt advances, proving the wire attempt).
	st.failedAt = time.Now().Add(-failureRetryCooldown - time.Second)
	p.paintIdentity(st, cfg, cache, vars, false)
	if !st.failedAt.After(firstFail) {
		t.Fatal("expired cooldown must re-attempt the send")
	}
}

func TestPaintAUQLeaseZeroRetriesFailedEdge(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	cfg := p.config()
	cfg.AUQLease = 0
	st := &sessState{lastPending: 0} // settled painter state, question arrives
	dead := filepath.Join(shortTmpDir(t), "gone.sock")
	cache := facts.Cache{SessionID: "s", HerdrPaneID: "p1", HerdrSocketPath: dead, AUQPending: 1}

	p.paintAUQ(st, cfg, cache, false)
	if st.lastPending != 0 {
		t.Fatal("lease=0: failed edge must stay armed (no TTL self-heal exists)")
	}

	// The pane comes back at the same path: the next cache write retries.
	cache.HerdrSocketPath = sock
	p.paintAUQ(st, cfg, cache, false)
	if st.lastPending != 1 {
		t.Fatal("retry after recovery must advance state")
	}
	if got := drain(lines); len(got) != 1 || !strings.Contains(got[0], `"blocked"`) {
		t.Fatalf("retried edge must reach the wire, got %v", got)
	}
}

func TestSweepBoundedAgainstWedgedHerdr(t *testing.T) {
	p, _, _ := newTestPainter(t)
	// A herdr that accepts and never answers: the snapshot call must hit its
	// deadline instead of latching the sweep guard forever.
	wedged := filepath.Join(shortTmpDir(t), "wedged.sock")
	ln, err := net.Listen("unix", wedged)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // hold open, never reply
		}
	}()
	t.Setenv("HERDR_SOCKET_PATH", wedged)

	done := make(chan struct{})
	go func() {
		p.Sweep()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep must be bounded against a wedged herdr")
	}
	p.sweepMu.Lock()
	stuck := p.sweeping
	p.sweepMu.Unlock()
	if stuck {
		t.Fatal("sweep guard must release")
	}
}

// One painter serves every herdr session on the host, so each binding must be
// judged by ITS OWN server: asking only $HERDR_SOCKET_PATH sent every other
// server's stale bindings and collected `pane_not_found`.
func TestLivenessVetoAsksEachPanesOwnSocket(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	otherSock, otherLines := stubHerdr(t)
	writeSessCache(t, "otherlive00000000", "p1", otherSock, 0) // live there → paints
	writeSessCache(t, "otherdead00000000", "p9", otherSock, 0) // gone there → vetoed
	writeSessCache(t, "sameherdr00000000", "p9", sock, 0)      // gone here  → vetoed

	p.Sweep()
	if reports := metadataReports(drain(lines)); len(reports) != 0 {
		t.Fatalf("pane gone on the probed socket must be vetoed, sent %d report(s)", len(reports))
	}
	reports := metadataReports(drain(otherLines))
	if len(reports) == 0 {
		t.Fatal("a live pane on another herdr socket must paint")
	}
	for _, line := range reports {
		if strings.Contains(line, `"p9"`) {
			t.Fatalf("pane gone on its own socket must be vetoed, got %s", line)
		}
	}
}

// metadataReports keeps the paint traffic and drops the snapshot probes.
func metadataReports(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "pane.report_metadata") {
			out = append(out, l)
		}
	}
	return out
}

func TestClaimantElectionScopedToSocket(t *testing.T) {
	p, sock, lines := newTestPainter(t)
	// Two LIVE sessions on different herdr instances sharing pane id p1
	// (pane ids are per-instance) — both must paint; keying the election on
	// pane id alone would silently drop one.
	otherSock, otherLines := stubHerdr(t)
	writeSessCache(t, "instancea00000000", "p1", sock, 0)
	writeSessCache(t, "instanceb00000000", "p1", otherSock, 0)

	p.Sweep()
	if got := drain(lines); len(got) < 2 { // snapshot probe + at least identity
		t.Fatalf("probed-socket session must paint, got %d lines", len(got))
	}
	if got := drain(otherLines); len(got) == 0 {
		t.Fatal("second instance's claimant must paint too — election must key on (socket, pane)")
	}
}

// The painter outlives the session that launched it by design, but once every
// herdr on the host is gone it is a resident with nothing to paint, holding
// the flock and invisible to `herdr session list`. It must retire itself —
// and not on the first miss, or a herdr restart would kill the fleet's
// painter.
func TestPainterRetiresWhenEveryHerdrIsGone(t *testing.T) {
	p, sock, _ := newTestPainter(t)
	writeSessCache(t, "bounddead0000000", "p1", sock, 0)
	p.Sweep() // herdr up: no retirement pressure

	os.Remove(sock) // every bound socket now unreachable
	for i := 0; i < deadSweepsBeforeRetire-1; i++ {
		p.Sweep()
		select {
		case <-p.quit:
			t.Fatalf("retired after %d quiet sweep(s) — a herdr restart must not kill the painter", i+1)
		default:
		}
	}
	p.Sweep()
	select {
	case <-p.quit:
	default:
		t.Fatal("every bound socket unreachable for the full threshold must retire the painter")
	}
}

// Zero bindings is not zero herdrs: a fresh host whose hooks have not fired
// yet would otherwise retire its painter seconds after the startup hook.
func TestNoBindingsNeverRetires(t *testing.T) {
	p, _, _ := newTestPainter(t)
	for i := 0; i < deadSweepsBeforeRetire+2; i++ {
		p.Sweep()
	}
	select {
	case <-p.quit:
		t.Fatal("an empty cache must never retire the painter")
	default:
	}
}
