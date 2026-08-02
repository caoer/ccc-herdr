package facts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVarsResolution(t *testing.T) {
	c := Cache{
		CustomTitle:   "reader",
		Status:        "working",
		LastKnownRole: "advisor",
		LaunchProfile: "models-turnip",
	}
	c.Model.ID = "fable-5"
	vars := Vars("03a42aff-505d-45f8-b491-6de2b3151f47", c, MapEntry{SessionDir: "/x/02-00-adhoc"})

	want := map[string]string{
		"SESSION_ID_SHORT":   "03a42aff",
		"CCC_SESSION":        "02-00-adhoc",
		"ROLE":               "advisor",
		"NAME":               "reader",
		"MODEL":              "fable-5",
		"STATUS":             "working",
		"PROFILE_IF_UNNAMED": "",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s: got %q want %q", k, vars[k], v)
		}
	}
}

func TestVarsUnjoinedAndUnnamed(t *testing.T) {
	c := Cache{LaunchProfile: "grid-mullein1b"}
	vars := Vars("abcd1234efgh", c, MapEntry{})
	if vars["CCC_SESSION"] != "ad-hoc" {
		t.Fatalf("unjoined session: %q", vars["CCC_SESSION"])
	}
	if vars["NAME"] != "grid-mullein1b" {
		t.Fatalf("NAME must fall back to profile: %q", vars["NAME"])
	}
	if vars["PROFILE_IF_UNNAMED"] != "grid-mullein1b" {
		t.Fatalf("PROFILE_IF_UNNAMED must show while untitled: %q", vars["PROFILE_IF_UNNAMED"])
	}
}

func TestFormatIdle(t *testing.T) {
	cases := []struct {
		d       time.Duration
		unknown bool
		want    string
	}{
		{0, true, ""},                          // no hook event yet
		{-5 * time.Minute, false, ""},          // future timestamp (clock skew)
		{30 * time.Second, false, ""},          // active session → token clear
		{5 * time.Minute, false, "5m"},
		{59 * time.Minute, false, "59m"},
		{60 * time.Minute, false, "1h"},
		{65 * time.Minute, false, "1h5m"},
		{23*time.Hour + 59*time.Minute, false, "23h59m"},
		{24 * time.Hour, false, "1d"},
		{51 * time.Hour, false, "2d3h"},
	}
	for _, tc := range cases {
		if got := formatIdle(tc.d, tc.unknown); got != tc.want {
			t.Errorf("formatIdle(%v, %v): got %q want %q", tc.d, tc.unknown, got, tc.want)
		}
	}
}

func TestVarsIdle(t *testing.T) {
	c := Cache{LastHookEvent: time.Now().Add(-10 * time.Minute)}
	if got := Vars("abcd1234", c, MapEntry{})["IDLE"]; got != "10m" {
		t.Fatalf("IDLE: got %q want 10m", got)
	}
	if got := Vars("abcd1234", Cache{}, MapEntry{})["IDLE"]; got != "" {
		t.Fatalf("zero LastHookEvent must clear the token, got %q", got)
	}
}

func TestProfileFromTranscriptPath(t *testing.T) {
	c := Cache{TranscriptPath: "/Users/x/.local/share/ucc/profiles/dewiest_gulp/projects/foo/bar.jsonl"}
	if got := Profile(c); got != "dewiest_gulp" {
		t.Fatalf("profile from transcript: %q", got)
	}
}

func TestRoleFromAgentFileFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.md")
	os.WriteFile(path, []byte("---\ntype: agent\nrole: leader\n---\nbody role: nope\n"), 0o644)
	if got := Role(path, Cache{LastKnownRole: "worker"}); got != "leader" {
		t.Fatalf("frontmatter role must win: %q", got)
	}
	if got := Role("", Cache{LastKnownRole: "worker"}); got != "worker" {
		t.Fatalf("sticky fallback: %q", got)
	}
}

func TestParseFrontmatterUnclosedIsNotABlock(t *testing.T) {
	fm := ParseFrontmatter("---\nrole: leader\nno closer\n")
	if len(fm) != 0 {
		t.Fatalf("unclosed opener must parse empty, got %v", fm)
	}
}
