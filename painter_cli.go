package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/caoer/ccc-herdr/internal/facts"
	"github.com/caoer/ccc-herdr/internal/herdr"
	"github.com/caoer/ccc-herdr/internal/painter"
)

// runPainter dispatches the painter verbs: `run` (resident loop, foreground)
// and `start` (idempotent detach — what the herdr [[startup]] hook calls).
func runPainter(args []string) int {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "run":
		return runPainterLoop()
	case "start":
		return startPainter(false)
	case "restart":
		return startPainter(true)
	default:
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr painter run|start|restart")
		return 2
	}
}

// startPainter launches the resident loop detached and exits — herdr's
// [[startup]] hook is one-shot, not a supervisor, and it reads the hook's
// stdout/stderr pipes to EOF. So the child gets its own session (setsid) and
// a log FILE: an inherited pipe would leave herdr's reader threads (and the
// plugin command log entry) hanging for the painter's whole life.
// Idempotent by flock probe — a second herdr server, a live handoff, or a
// hand start finds the painter already up and no-ops.
// takeover=true (`painter restart`) stops the incumbent first — what an
// upgrade needs, since a running old painter holds the lock and a freshly
// installed binary would no-op against it.
func startPainter(takeover bool) int {
	if takeover {
		if stopped, pid := painter.StopIncumbent(5 * time.Second); pid != 0 {
			if !stopped {
				fmt.Fprintf(os.Stderr, "ccc-herdr: painter %d did not release the lock — not starting a second one\n", pid)
				return 1
			}
			fmt.Printf("ccc-herdr: stopped painter %d\n", pid)
		}
	}
	release, free := painter.AcquireSingleton()
	if !free {
		fmt.Println("ccc-herdr: painter already running")
		return 0
	}
	release() // the child takes the lock for real

	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	var out *os.File
	logPath := painter.LogPath()
	if logPath == "" {
		fmt.Fprintln(os.Stderr, "ccc-herdr: no cache dir resolvable (UCC_HOME unset?) — painter log discarded")
		out, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	} else if err = os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
		out, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}
	if err != nil {
		return fail(err)
	}
	defer out.Close()

	cmd := exec.Command(exe, "painter", "run")
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	pid := cmd.Process.Pid // Release() zeroes it — read first
	_ = cmd.Process.Release()
	fmt.Printf("ccc-herdr: painter started (pid %d) — log %s\n", pid, logPath)
	return 0
}

// runPainterLoop is the resident loop: `ccc-herdr painter run`.
func runPainterLoop() int {
	release, ok := painter.AcquireSingleton()
	if !ok {
		fmt.Fprintln(os.Stderr, "ccc-herdr: another painter is already running")
		return 1
	}
	defer release()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	p := painter.New(painter.ConfigPath(), logger)
	logger.Printf("painter up — config %s, cache %s", painter.ConfigNote(p.CfgPath), facts.CacheDir())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := p.Run(ctx); err != nil {
		return fail(err)
	}
	return 0
}

// resolveSessionIDs expands an optional id/prefix argument against the cache
// dir. No argument = every session with a herdr pane binding.
func resolveSessionIDs(arg string) ([]string, error) {
	dir := facts.CacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cache dir %s: %w", dir, err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		sid := strings.TrimSuffix(name, ".json")
		if arg != "" && !strings.HasPrefix(sid, arg) {
			continue
		}
		ids = append(ids, sid)
	}
	if arg != "" && len(ids) == 0 {
		return nil, fmt.Errorf("no session cache matches %q", arg)
	}
	return ids, nil
}

// runCheck prints config diagnostics and, per session, the exact wire lines a
// paint would send. Exit 1 on any diagnostic — same resolver as the painter,
// so check can never disagree with runtime.
func runCheck(args []string) int {
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	diags := 0
	fmt.Printf("config: %s\n", painter.ConfigNote(painter.ConfigPath()))
	cfg := painter.LoadConfig(painter.ConfigPath(), func(format string, a ...any) {
		diags++
		fmt.Fprintf(os.Stderr, "diag: "+format+"\n", a...)
	})
	if !cfg.Enabled {
		fmt.Println("painter disabled (enabled = false)")
	}

	ids, err := resolveSessionIDs(arg)
	if err != nil {
		return fail(err)
	}
	sessionMap := facts.LoadSessionMap()
	shown := 0
	for _, sid := range ids {
		cache, err := facts.ReadCache(filepath.Join(facts.CacheDir(), sid+".json"))
		if err != nil || cache.HerdrPaneID == "" || cache.HerdrSocketPath == "" {
			continue
		}
		shown++
		vars := facts.Vars(sid, cache, sessionMap[sid])
		vars["HERDR_TITLE"] = ""
		if title, ok := herdr.FetchPaneTitle(cache.HerdrSocketPath, cache.HerdrPaneID); ok {
			vars["HERDR_TITLE"] = title
		}
		report, ok := painter.ComposeIdentity(cache.HerdrPaneID, cfg, vars, func(format string, a ...any) {
			diags++
			fmt.Fprintf(os.Stderr, "diag: "+format+"\n", a...)
		})
		if ok {
			line, err := herdr.WireLine(report)
			if err == nil {
				fmt.Printf("%s identity: %s\n", shortID(sid), line)
			}
		}
		if cfg.AUQLabel != "" {
			line, err := herdr.WireLine(painter.ComposeAUQ(cache.HerdrPaneID, cfg, cache.AUQPending))
			if err == nil {
				fmt.Printf("%s auq:      %s\n", shortID(sid), line)
			}
		}
	}
	fmt.Printf("%d paintable session(s), %d diagnostic(s)\n", shown, diags)
	if diags > 0 {
		return 1
	}
	return 0
}

// runPaint force-sends one repaint for the matched sessions, bypassing the
// resident painter's dedup (same content → herdr dedups by rendering, and
// the painter's next compose agrees by hash).
func runPaint(args []string) int {
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	if cfg := painter.LoadConfig(painter.ConfigPath(), nil); !cfg.Enabled {
		fmt.Fprintln(os.Stderr, "ccc-herdr: painter disabled (enabled = false) — nothing painted")
		return 1
	}
	ids, err := resolveSessionIDs(arg)
	if err != nil {
		return fail(err)
	}
	logger := log.New(os.Stderr, "", 0)
	p := painter.New(painter.ConfigPath(), logger)
	painted := 0
	for _, sid := range ids {
		cache, err := facts.ReadCache(filepath.Join(facts.CacheDir(), sid+".json"))
		if err != nil || cache.HerdrPaneID == "" || cache.HerdrSocketPath == "" {
			continue
		}
		p.Repaint(sid, true)
		painted++
	}
	fmt.Printf("painted %d session(s)\n", painted)
	return 0
}

// shortID trims a session id for display without assuming its length.
func shortID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}
