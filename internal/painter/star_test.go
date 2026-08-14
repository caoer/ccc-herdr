package painter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStarConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccc-herdr.star")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// perRoleStar is the shape the fleet config uses: static dicts plus one
// render function computing per-role tokens and the title omission.
const perRoleStar = `
painter = dict(enabled = True, agent = "claude", ttl = "6h")
auq = dict(blocked_label = "AUQ", lease = "20m")

ROLES = ("worker", "advisor", "leader")

def render(v):
    tokens = {
        "id": v.SESSION_ID_SHORT,
        "session": v.CCC_SESSION,
        "name": v.TITLE,
        "profile": v.PROFILE_IF_UNNAMED,
        "idle": v.IDLE,
    }
    for role in ROLES:
        tokens["role_" + role] = v.ROLE if v.ROLE == role else ""
    params = {}
    if v.HERDR_TITLE.strip():
        params["title"] = "%s %s|%s" % (v.CCC_SESSION, v.NAME, v.HERDR_TITLE)
    return dict(display_agent = v.PROFILE, tokens = tokens, params = params)
`

func starVars(role, title string) map[string]string {
	return map[string]string{
		"SESSION_ID":         "abcdef1234567890",
		"SESSION_ID_SHORT":   "abcdef12",
		"CCC_SESSION":        "14-00-adhoc",
		"ROLE":               role,
		"TITLE":              "",
		"NAME":               "vows_yogic_9k",
		"PROFILE":            "vows_yogic_9k",
		"PROFILE_IF_UNNAMED": "vows_yogic_9k",
		"IDLE":               "",
		"HERDR_TITLE":        title,
	}
}

func TestStarConfigStatics(t *testing.T) {
	path := writeStarConfig(t, perRoleStar)
	cfg := LoadConfig(path, func(format string, args ...any) {
		t.Fatalf("unexpected diag: "+format, args...)
	})
	if cfg.Star == nil {
		t.Fatal("star config must carry the render program")
	}
	if !cfg.Enabled || cfg.Agent != "claude" || cfg.TTL != 6*time.Hour {
		t.Fatalf("statics not resolved: %+v", cfg)
	}
	if cfg.AUQLabel != "AUQ" || cfg.AUQLease != 20*time.Minute {
		t.Fatalf("auq statics not resolved: %+v", cfg)
	}
	if cfg.Tokens != nil || cfg.Params != nil || cfg.DisplayAgent != "" {
		t.Fatalf("template surfaces must stay empty under star, got %+v", cfg)
	}
}

func TestStarPerRoleTokensAreExclusive(t *testing.T) {
	cfg := LoadConfig(writeStarConfig(t, perRoleStar), nil)
	for _, role := range []string{"worker", "advisor", "leader"} {
		report, ok := ComposeIdentity("w1:p1", cfg, starVars(role, ""), nil)
		if !ok {
			t.Fatalf("%s: compose must produce a report", role)
		}
		if got := report.Tokens["role_"+role]; got != role {
			t.Fatalf("%s: own token = %v, want %q", role, got, role)
		}
		for _, other := range []string{"worker", "advisor", "leader"} {
			if other != role && report.Tokens["role_"+other] != nil {
				t.Fatalf("%s: role_%s must clear (nil), got %v", role, other, report.Tokens["role_"+other])
			}
		}
	}
	// No role at all: every role token clears.
	report, ok := ComposeIdentity("w1:p1", cfg, starVars("", ""), nil)
	if !ok {
		t.Fatal("roleless compose must still report")
	}
	for _, role := range []string{"worker", "advisor", "leader"} {
		if report.Tokens["role_"+role] != nil {
			t.Fatalf("roleless: role_%s must clear, got %v", role, report.Tokens["role_"+role])
		}
	}
}

func TestStarTitleOmission(t *testing.T) {
	cfg := LoadConfig(writeStarConfig(t, perRoleStar), nil)
	report, _ := ComposeIdentity("w1:p1", cfg, starVars("worker", ""), nil)
	if report.Extra != nil {
		t.Fatalf("empty HERDR_TITLE must omit params, got %v", report.Extra)
	}
	report, _ = ComposeIdentity("w1:p1", cfg, starVars("worker", "fixing the build"), nil)
	if got := report.Extra["title"]; got != "14-00-adhoc vows_yogic_9k|fixing the build" {
		t.Fatalf("title param = %v", got)
	}
	if report.DisplayAgent != "vows_yogic_9k" {
		t.Fatalf("display_agent = %q", report.DisplayAgent)
	}
}

func TestStarSyntaxErrorDegradesToDefaults(t *testing.T) {
	var diags []string
	cfg := LoadConfig(writeStarConfig(t, "def render(v:\n"), func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf(format, args...))
	})
	if cfg.Star != nil || cfg.Tokens["id"] != "{{SESSION_ID_SHORT}}" {
		t.Fatalf("broken star must degrade to template defaults, got %+v", cfg)
	}
	if len(diags) == 0 {
		t.Fatal("the degrade must diagnose")
	}
}

func TestStarMissingRenderDegradesToDefaults(t *testing.T) {
	var diags []string
	cfg := LoadConfig(writeStarConfig(t, `painter = dict(agent = "claude")`), func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf(format, args...))
	})
	if cfg.Star != nil {
		t.Fatal("render-less star must not claim the star path")
	}
	if len(diags) != 1 || !strings.Contains(diags[0], "render") {
		t.Fatalf("want one render diagnostic, got %v", diags)
	}
}

func TestStarStaticSurfacesOwnedByRenderAreStripped(t *testing.T) {
	body := `
painter = dict(agent = "claude", tokens = {"id": "x"}, display_agent = "nope")
def render(v):
    return dict(tokens = {"id": v.SESSION_ID_SHORT})
`
	var diags []string
	cfg := LoadConfig(writeStarConfig(t, body), func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf(format, args...))
	})
	if cfg.Star == nil {
		t.Fatal("config must still load")
	}
	if cfg.Tokens != nil || cfg.DisplayAgent != "" {
		t.Fatalf("static per-pane surfaces must be stripped, got %+v", cfg)
	}
	joined := strings.Join(diags, "\n")
	if !strings.Contains(joined, "tokens") || !strings.Contains(joined, "display_agent") {
		t.Fatalf("stripping must diagnose both keys, got %v", diags)
	}
}

func TestStarRenderRuntimeErrorSkipsIdentity(t *testing.T) {
	body := `
def render(v):
    return dict(tokens = {"id": v.NO_SUCH_VAR})
`
	cfg := LoadConfig(writeStarConfig(t, body), nil)
	var diags []string
	_, ok := ComposeIdentity("w1:p1", cfg, starVars("worker", ""), func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf(format, args...))
	})
	if ok {
		t.Fatal("a failed render must skip the identity report")
	}
	if len(diags) != 1 || !strings.Contains(diags[0], "identity report skipped") {
		t.Fatalf("want one render diagnostic, got %v", diags)
	}
}

func TestStarBadValueTypesDropPerEntry(t *testing.T) {
	body := `
def render(v):
    return dict(
        tokens = {"good": "ok", "bad": 42},
        params = {"n": 3, "s": "text", "skip": ""},
        bogus = "x",
    )
`
	cfg := LoadConfig(writeStarConfig(t, body), nil)
	var diags []string
	report, ok := ComposeIdentity("w1:p1", cfg, starVars("worker", ""), func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf(format, args...))
	})
	if !ok {
		t.Fatal("partial validity must still report")
	}
	if report.Tokens["good"] != "ok" {
		t.Fatalf("good token dropped: %v", report.Tokens)
	}
	if _, present := report.Tokens["bad"]; present {
		t.Fatalf("non-string token must drop, got %v", report.Tokens)
	}
	if report.Extra["n"] != int64(3) || report.Extra["s"] != "text" {
		t.Fatalf("params conversion wrong: %v", report.Extra)
	}
	if _, present := report.Extra["skip"]; present {
		t.Fatal("empty param must be an omission")
	}
	joined := strings.Join(diags, "\n")
	if !strings.Contains(joined, "tokens.bad") || !strings.Contains(joined, "unknown key") {
		t.Fatalf("drops must diagnose, got %v", diags)
	}
}

func TestStarValueClampsAt80(t *testing.T) {
	body := `
def render(v):
    return dict(tokens = {"id": "x" * 200})
`
	cfg := LoadConfig(writeStarConfig(t, body), nil)
	report, _ := ComposeIdentity("w1:p1", cfg, starVars("", ""), nil)
	if got := len(report.Tokens["id"].(string)); got != 80 {
		t.Fatalf("token value must clamp to 80 runes, got %d", got)
	}
}

func TestConfigPathPrefersStar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCC_HERDR_CONFIG", "")
	t.Setenv("UCC_HOME", home)
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ConfigPath(); got != filepath.Join(dir, "ccc-herdr.toml") {
		t.Fatalf("no star file: want toml path, got %s", got)
	}
	star := filepath.Join(dir, "ccc-herdr.star")
	if err := os.WriteFile(star, []byte("def render(v):\n    return dict()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ConfigPath(); got != star {
		t.Fatalf("star file present: want star path, got %s", got)
	}
}
