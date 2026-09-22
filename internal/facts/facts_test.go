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
		{0, true, ""},                 // no hook event yet
		{-5 * time.Minute, false, ""}, // future timestamp (clock skew)
		{30 * time.Second, false, ""}, // active session → token clear
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

// The session the pane shows is the CARD's `session:` — the map's stored dir
// is only the birth dir (the day's adhoc tree); a seat that re-seated itself by
// putting session:/seat: on its card must show the session it joined.
func TestVarsSessionFollowsCard(t *testing.T) {
	card := filepath.Join(t.TempDir(), "agents", "1d320e04", "1d320e04.md")
	os.MkdirAll(filepath.Dir(card), 0o755)
	os.WriteFile(card, []byte("---\ntype: agent\nrole: leader\nseat: \"[[year=2026/month=08/22-18-hook-support-design/rosters/leader-wave-residue]]\"\nsession: \"22-18-hook-support-design\"\n---\n"), 0o644)
	entry := MapEntry{SessionDir: "/x/year=2026/month=08/23-00-adhoc", AgentFile: card}
	vars := Vars("1d320e04-3ad8", Cache{}, entry)
	if vars["CCC_SESSION"] != "22-18-hook-support-design" {
		t.Fatalf("card session must win over the stored birth dir: %q", vars["CCC_SESSION"])
	}
	if vars["ROLE"] != "leader" {
		t.Fatalf("role from the same card: %q", vars["ROLE"])
	}

	// seat: alone (no session: key) — the session is the path before /rosters/.
	os.WriteFile(card, []byte("---\nseat: \"[[year=2026/month=08/22-18-hook-support-design/rosters/worker-x]]\"\n---\n"), 0o644)
	if got := Vars("1d320e04-3ad8", Cache{}, entry)["CCC_SESSION"]; got != "22-18-hook-support-design" {
		t.Fatalf("seat-derived session: %q", got)
	}

	// Card names no session → the stored dir's basename, as before.
	os.WriteFile(card, []byte("---\ntype: agent\n---\n"), 0o644)
	if got := Vars("1d320e04-3ad8", Cache{}, entry)["CCC_SESSION"]; got != "23-00-adhoc" {
		t.Fatalf("stored dir fallback: %q", got)
	}
}

// The daemon owns session-map.json under its cache dir; the retired ccc-cli
// file is read only while the daemon file does not exist.
func TestSessionMapPrefersDaemonFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("UCC_HOME", home)
	t.Setenv("CCC_CACHE_DIR", "")
	t.Setenv("CCC_CLI_CACHE_DIR", "")
	legacy := filepath.Join(home, "cache", "ccc-cli", "session-map.json")
	daemon := filepath.Join(home, "cache", "ccc-status", "session-map.json")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.MkdirAll(filepath.Dir(daemon), 0o755)
	os.WriteFile(legacy, []byte(`{"sid":{"sessionDir":"/legacy","agentFile":"/legacy/a.md"}}`), 0o644)
	if got := LoadSessionMap()["sid"].SessionDir; got != "/legacy" {
		t.Fatalf("legacy fallback while daemon file absent: %q", got)
	}
	os.WriteFile(daemon, []byte(`{"sid":{"sessionDir":"/daemon","agentFile":"/daemon/a.md"}}`), 0o644)
	if got := LoadSessionMap()["sid"].SessionDir; got != "/daemon" {
		t.Fatalf("daemon file must win once present: %q", got)
	}
}

// The engine writes card scalars quoted (`role: "worker"`). A quoted ROLE
// reached the panes twice wrong: shown as `"worker"`, and matching none of the
// config's advisor/leader/worker arms, so the pane lost its per-role color.
func TestRoleIsUnquoted(t *testing.T) {
	card := filepath.Join(t.TempDir(), "agents", "7e9db8e1", "7e9db8e1.md")
	os.MkdirAll(filepath.Dir(card), 0o755)
	os.WriteFile(card, []byte("---\ntype: agent\nrole: \"worker\"\n---\n"), 0o644)
	if got := Vars("7e9db8e1-5fbc", Cache{}, MapEntry{AgentFile: card})["ROLE"]; got != "worker" {
		t.Fatalf("quoted role must render bare: %q", got)
	}
	// `type` as the role carrier goes through the same known-role gate.
	os.WriteFile(card, []byte("---\ntype: \"advisor\"\n---\n"), 0o644)
	if got := Vars("7e9db8e1-5fbc", Cache{}, MapEntry{AgentFile: card})["ROLE"]; got != "advisor" {
		t.Fatalf("quoted type-as-role must render bare: %q", got)
	}
}

func TestUnquote(t *testing.T) {
	for in, want := range map[string]string{
		`"22-18-x"`:   "22-18-x",
		`'a'`:         "a",
		`bare`:        "bare",
		`"`:           `"`,
		`"a" and "b"`: `"a" and "b"`, // interior quotes → leave the value alone
	} {
		if got := Unquote(in); got != want {
			t.Errorf("Unquote(%q) = %q want %q", in, got, want)
		}
	}
}

func TestParseFrontmatterUnclosedIsNotABlock(t *testing.T) {
	fm := ParseFrontmatter("---\nrole: leader\nno closer\n")
	if len(fm) != 0 {
		t.Fatalf("unclosed opener must parse empty, got %v", fm)
	}
}

// The daemon writes Go's zero time for "no SessionEnd yet" — the literal wire
// value on every live cache — so Ended must read that as live, an end at or
// after the start as ended, and a start after the end (a resumed id) as live.
func TestEndedFollowsTheDaemonWire(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		body  string
		ended bool
	}{
		"live":    {`{"last_session_start_time":"2026-09-22T17:33:55.039717-04:00","last_session_end_time":"0001-01-01T00:00:00Z"}`, false},
		"ended":   {`{"last_session_start_time":"2026-09-22T17:34:17.235089-04:00","last_session_end_time":"2026-09-22T17:34:18.947609-04:00"}`, true},
		"resumed": {`{"last_session_start_time":"2026-09-22T18:00:00-04:00","last_session_end_time":"2026-09-22T17:34:18-04:00"}`, false},
		"never":   {`{"session_id":"x"}`, false},
	}
	for name, tc := range cases {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := ReadCache(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Ended() != tc.ended {
			t.Errorf("%s: Ended()=%v want %v", name, c.Ended(), tc.ended)
		}
	}
}
