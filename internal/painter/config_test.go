package painter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccc-herdr.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigMissingFileIsDefaults(t *testing.T) {
	cfg := LoadConfig(filepath.Join(t.TempDir(), "absent.toml"), nil)
	def := DefaultConfig()
	if !cfg.Enabled || cfg.Method != def.Method || cfg.AUQLabel != def.AUQLabel {
		t.Fatalf("missing file must resolve to defaults, got %+v", cfg)
	}
}

func TestLoadConfigPerKeyOverlay(t *testing.T) {
	path := writeConfig(t, "[auq]\nblocked_label = \"问\"\n")
	cfg := LoadConfig(path, nil)
	if cfg.AUQLabel != "问" {
		t.Fatalf("auq label not applied: %q", cfg.AUQLabel)
	}
	if cfg.Tokens["id"] != "{{SESSION_ID_SHORT}}" {
		t.Fatal("[auq]-only file must leave identity defaults intact")
	}
}

func TestLoadConfigTokensReplaceWholeMap(t *testing.T) {
	path := writeConfig(t, "[painter.tokens]\nid = \"{{SESSION_ID_SHORT}}\"\n")
	cfg := LoadConfig(path, nil)
	if len(cfg.Tokens) != 1 {
		t.Fatalf("authoring tokens must replace the whole map, got %v", cfg.Tokens)
	}
}

func TestLoadConfigWrongTypeWarnsAndKeepsDefault(t *testing.T) {
	path := writeConfig(t, "[painter]\nttl = 1800\nenabled = \"yes\"\n")
	var diags []string
	cfg := LoadConfig(path, func(format string, args ...any) {
		diags = append(diags, format)
	})
	if cfg.TTL != 6*time.Hour {
		t.Fatalf("int ttl must keep default, got %s", cfg.TTL)
	}
	if !cfg.Enabled {
		t.Fatal("kill switch must fail OPEN on a type slip")
	}
	if len(diags) != 2 {
		t.Fatalf("want 2 diagnostics, got %d: %v", len(diags), diags)
	}
}

func TestLoadConfigClampsTTLAtHerdrCeiling(t *testing.T) {
	path := writeConfig(t, "[painter]\nttl = \"48h\"\n[auq]\nlease = \"30h\"\n")
	var diags []string
	cfg := LoadConfig(path, func(format string, args ...any) { diags = append(diags, format) })
	if cfg.TTL != herdrMaxTTL || cfg.AUQLease != herdrMaxTTL {
		t.Fatalf("beyond-ceiling durations must clamp to 24h, got ttl=%s lease=%s", cfg.TTL, cfg.AUQLease)
	}
	if len(diags) != 2 {
		t.Fatalf("clamps must diagnose, got %v", diags)
	}
}

func TestLoadConfigWhitespaceLabelDisables(t *testing.T) {
	path := writeConfig(t, "[auq]\nblocked_label = \"  \"\n")
	var diags []string
	cfg := LoadConfig(path, func(format string, args ...any) { diags = append(diags, format) })
	if cfg.AUQLabel != "" {
		t.Fatalf("whitespace label must disable the AUQ writer, got %q", cfg.AUQLabel)
	}
	if len(diags) != 1 {
		t.Fatalf("the disable must diagnose, got %v", diags)
	}
}

func TestLoadConfigUnknownKeysDiagnose(t *testing.T) {
	path := writeConfig(t, "[painter]\nbogus = 1\n[mystery]\nx = 1\n")
	var diags []string
	LoadConfig(path, func(format string, args ...any) { diags = append(diags, format) })
	joined := strings.Join(diags, "\n")
	if !strings.Contains(joined, "unknown key") || !strings.Contains(joined, "unknown table") {
		t.Fatalf("unknown key/table must diagnose, got %v", diags)
	}
}
