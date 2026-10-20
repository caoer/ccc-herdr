// Package resume is the `ccc-herdr resume` verb: the command herdr's pane
// restore types for a ccc seat. It resumes the session through its launcher
// only when no live process on the host already runs it.
//
// The restore is one of several doors that resume a seat after a host
// restart; a heartbeat wake (an agent folder's run-agent.sh) is another, and
// either can run first. ccc-statusd's wake and resume-session refuse a session
// a live process holds; without the same check here, a wake that resumed the
// seat before herdr's restore leaves two processes on one transcript, both
// answering every message (home-wiki-sessions 06-20-tooling-dev, cards
// host-restart-double-resumes-seat and herdr-restore-resume-unguarded).
//
// The holder answer is ccc-statusd's own (`cccd session pids <id> --json`,
// ccagent.SessionHolders), so every door reads the same process table rule.
package resume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Holder is a process that already runs the session.
type Holder struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// holdersTimeout bounds the holder query; a restore never waits longer for it.
const holdersTimeout = 10 * time.Second

// Holders asks ccc-statusd which processes already run id; a var so a test
// supplies the answer.
var Holders = cccdHolders

// execLauncher replaces this process with the launcher; a var so a test
// captures the argv instead.
var execLauncher = func(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}

// Main runs `ccc-herdr resume <session-id> <launcher>` and returns its exit
// code; on success it does not return, because the launcher replaces it.
func Main(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr resume <session-id> <launcher>")
		return 2
	}
	return Run(args[0], args[1], os.Stderr)
}

// Run resumes id through launcher (`<launcher> --resume <id>`) unless a live
// process already runs id. A holder refuses with exit 1 and a line naming its
// pid: herdr ends the typed command with the shell's exit-on-success form, so
// the refused pane keeps its shell and the line stays readable. A holder query
// that fails (cccd missing, or a build without `session pids`) resumes without
// the check and says so: the restore is the only door that brings most seats
// back, and failing closed would leave them down.
func Run(id, launcher string, stderr io.Writer) int {
	holders, err := Holders(id)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "ccc-herdr: resume %s: live-holder check unavailable (%v); resuming without it\n", id, err)
	case len(holders) > 0:
		h := holders[0]
		fmt.Fprintf(stderr, "ccc-herdr: session %s already runs as pid %d (%s); this pane does not resume it, since a second process would share its transcript\n", id, h.PID, h.Command)
		return 1
	}
	if err := execLauncher(launcher, []string{launcher, "--resume", id}, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "ccc-herdr: resume %s: exec %s: %v\n", id, launcher, err)
		return 1
	}
	return 0
}

// cccdHolders runs `cccd session pids <id> --json`.
func cccdHolders(id string) ([]Holder, error) {
	bin, err := cccdPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), holdersTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "session", "pids", id, "--json")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s session pids: %v: %s", bin, err, firstLine(msg))
		}
		return nil, fmt.Errorf("%s session pids: %v", bin, err)
	}
	return parseHolders(stdout.Bytes())
}

// parseHolders reads `session pids --json`:
// {"session": <id>, "processes": [{"pid", "command"}]}.
func parseHolders(out []byte) ([]Holder, error) {
	var answer struct {
		Processes *[]Holder `json:"processes"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("session pids answer: %v", err)
	}
	if answer.Processes == nil {
		return nil, errors.New("session pids answer carries no processes list")
	}
	return *answer.Processes, nil
}

// cccdPath finds cccd on PATH, else at its install location: a restored
// pane's shell may not have ~/.local/bin on PATH yet.
func cccdPath() (string, error) {
	if p, err := exec.LookPath("cccd"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "cccd")
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("cccd not found on PATH or at ~/.local/bin/cccd")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
