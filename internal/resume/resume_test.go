package resume

import (
	"errors"
	"strings"
	"testing"
)

const sid = "0199f00d-4e5a-7c3b-9d2e-5ad0b1e5c0de"

// stub installs a holder answer and an exec capture; it returns the captured
// argv (nil while the launcher was never exec'd).
func stub(t *testing.T, holders []Holder, herr error) *[]string {
	t.Helper()
	oldH, oldE := Holders, execLauncher
	t.Cleanup(func() { Holders, execLauncher = oldH, oldE })
	Holders = func(id string) ([]Holder, error) {
		if id != sid {
			t.Fatalf("holder query for %q, want %q", id, sid)
		}
		return holders, herr
	}
	var got []string
	execLauncher = func(path string, argv, env []string) error {
		got = append([]string{path}, argv...)
		return nil
	}
	return &got
}

// A seat a wake already resumed is not resumed a second time by the restore.
func TestLiveHolderRefusesTheResume(t *testing.T) {
	execd := stub(t, []Holder{{PID: 4242, Command: "/x/ucc-vows --launch k --resume " + sid}}, nil)
	var stderr strings.Builder
	if code := Run(sid, "/u/bin/ucc-auto", &stderr); code == 0 {
		t.Fatalf("exit %d, want non-zero so the pane keeps its shell", code)
	}
	if *execd != nil {
		t.Fatalf("launcher exec'd beside a live holder: %v", *execd)
	}
	if !strings.Contains(stderr.String(), "pid 4242") {
		t.Fatalf("refusal must name the holder's pid: %q", stderr.String())
	}
}

func TestNoHolderExecsTheLauncher(t *testing.T) {
	execd := stub(t, nil, nil)
	var stderr strings.Builder
	Run(sid, "/u/bin/ucc-auto", &stderr)
	want := "/u/bin/ucc-auto /u/bin/ucc-auto --resume " + sid
	if got := strings.Join(*execd, " "); got != want {
		t.Fatalf("exec %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

// An unreadable answer resumes anyway: the restore is the only door most
// seats come back through.
func TestUnreadableHoldersResumesWithAWarning(t *testing.T) {
	execd := stub(t, nil, errors.New("unknown command \"pids\""))
	var stderr strings.Builder
	Run(sid, "/u/bin/ucc-auto", &stderr)
	if *execd == nil {
		t.Fatal("launcher not exec'd when the holder check failed")
	}
	if !strings.Contains(stderr.String(), "without it") {
		t.Fatalf("missing warning: %q", stderr.String())
	}
}

func TestParseHolders(t *testing.T) {
	got, err := parseHolders([]byte(`{"session":"` + sid + `","processes":[{"pid":7,"command":"claude --resume ` + sid + `"}]}` + "\n"))
	if err != nil || len(got) != 1 || got[0].PID != 7 {
		t.Fatalf("parse = %v, %v", got, err)
	}
	if got, err := parseHolders([]byte(`{"session":"x","processes":[]}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	for _, bad := range []string{``, `not json`, `{"session":"x"}`} {
		if _, err := parseHolders([]byte(bad)); err == nil {
			t.Fatalf("parse %q: want an error", bad)
		}
	}
}
