package reap

import (
	"errors"
	"testing"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

func info(shell, fg int, procs ...herdr.Process) *herdr.ProcessInfo {
	return &herdr.ProcessInfo{ShellPID: shell, ForegroundProcessGroup: fg, ForegroundProcesses: procs}
}

func TestIsIdle(t *testing.T) {
	none := func(int) (bool, error) { return false, nil }
	some := func(int) (bool, error) { return true, nil }
	unknown := func(int) (bool, error) { return false, errors.New("no pgrep") }
	zsh := herdr.Process{PID: 10, Name: "zsh"}
	cases := []struct {
		name     string
		info     *herdr.ProcessInfo
		children ChildCounter
		want     bool
	}{
		{"shell at prompt", info(10, 10, zsh), none, true},
		{"login shell name", info(10, 10, herdr.Process{PID: 10, Name: "-bash"}), none, true},
		{"program in foreground", info(10, 20, herdr.Process{PID: 20, Name: "claude"}), none, false},
		{"background job", info(10, 10, zsh), some, false},
		{"children unknown", info(10, 10, zsh), unknown, false},
		{"agent is the root process", info(10, 10, herdr.Process{PID: 10, Name: "codex"}), none, false},
		{"pipeline sharing the shell group", info(10, 10, zsh, herdr.Process{PID: 11, Name: "cat"}), none, false},
		{"no info", nil, none, false},
	}
	for _, c := range cases {
		if _, got := IsIdle(c.info, c.children); got != c.want {
			t.Errorf("%s: IsIdle = %v, want %v", c.name, got, c.want)
		}
	}
}
