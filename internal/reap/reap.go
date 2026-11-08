// Package reap finds panes left at an empty shell prompt — a seat that
// exited or crashed back to its shell — so they can be previewed and closed.
package reap

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

// Idle is one pane whose terminal is held by nothing but its shell.
type Idle struct {
	PaneID   string
	Location string // workspace·tab
	Cwd      string
	Shell    string
	Seat     string // the ccc seat id the pane's tokens still name, if any
	Title    string
}

// shells are the process names an empty pane's root may have.
var shells = map[string]bool{
	"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true,
	"ksh": true, "tcsh": true, "csh": true, "nu": true, "elvish": true, "xonsh": true,
}

// ShellName returns a process name's shell basename ("-zsh" → "zsh"), or ""
// when the name is not a shell.
func ShellName(name string) string {
	name = filepath.Base(strings.TrimPrefix(name, "-"))
	if shells[name] {
		return name
	}
	return ""
}

// ChildCounter reports whether a pid has child processes. An error means
// the answer is unknown, and an unknown pane is never called idle.
type ChildCounter func(pid int) (bool, error)

// HasChildren asks pgrep: exit 0 = children, exit 1 = none. It works on
// Linux and macOS alike; it must run on the herdr server's machine, which
// is where plugin popups run.
func HasChildren(pid int) (bool, error) {
	err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// IsIdle decides from a pane's process info: the shell owns the terminal's
// foreground, it is the only foreground process, and it has no children —
// so no program runs in the pane, in the foreground or as a background job.
func IsIdle(info *herdr.ProcessInfo, children ChildCounter) (string, bool) {
	if info == nil || info.ShellPID <= 0 {
		return "", false
	}
	if info.ForegroundProcessGroup != info.ShellPID || len(info.ForegroundProcesses) != 1 {
		return "", false
	}
	proc := info.ForegroundProcesses[0]
	if proc.PID != info.ShellPID {
		return "", false
	}
	shell := ShellName(proc.Name)
	if shell == "" {
		return "", false
	}
	busy, err := children(info.ShellPID)
	if err != nil || busy {
		return "", false
	}
	return shell, true
}

// Prober is the slice of the herdr client Find needs.
type Prober interface {
	ProcessInfo(paneID string) (*herdr.ProcessInfo, error)
}

// Find returns the snapshot's idle panes in snapshot order. A pane whose
// process info cannot be read is skipped, never guessed.
func Find(snap *herdr.Snapshot, client Prober, children ChildCounter) []Idle {
	workspaces := map[string]string{}
	for _, w := range snap.Workspaces {
		workspaces[w.WorkspaceID] = w.Label
	}
	tabs := map[string]herdr.Tab{}
	for _, t := range snap.Tabs {
		tabs[t.TabID] = t
	}
	var out []Idle
	for _, p := range snap.Panes {
		info, err := client.ProcessInfo(p.PaneID)
		if err != nil {
			continue
		}
		shell, ok := IsIdle(info, children)
		if !ok {
			continue
		}
		cwd := p.ForegroundCwd
		if cwd == "" {
			cwd = p.Cwd
		}
		out = append(out, Idle{
			PaneID:   p.PaneID,
			Location: location(workspaces[p.WorkspaceID], tabs[p.TabID]),
			Cwd:      cwd,
			Shell:    shell,
			Seat:     p.Tokens["id"],
			Title:    p.TerminalTitle,
		})
	}
	return out
}

func location(workspace string, tab herdr.Tab) string {
	label := tab.Label
	if label == "" && tab.Number > 0 {
		label = strconv.Itoa(tab.Number)
	}
	switch {
	case workspace == "":
		return label
	case label == "":
		return workspace
	}
	return workspace + "·" + label
}
