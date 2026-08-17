package painter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Diag receives one human-readable diagnostic about a config key. The painter
// routes it to its log; `ccc-herdr check` collects them for the authoring
// agent — one resolver, so check can never disagree with runtime.
type Diag func(format string, args ...any)

// Config is the effective ccc-herdr configuration. Template values
// ({{VAR}}) are expanded at compose time, not here. Star is non-nil when the
// config came from a .star file — the per-pane surfaces (tokens,
// display_agent, params) then come from its render(v) function and the
// template fields stay empty.
type Config struct {
	Enabled      bool
	Method       string
	Agent        string
	TTL          time.Duration // identity lease; 0 = live until replaced
	DisplayAgent string
	Tokens       map[string]string
	Params       map[string]any
	Star         *StarProgram

	AUQLabel string        // state_labels{blocked} text; "" disables the AUQ writer
	AUQLease time.Duration // blocked-label lease, renewed by the sweep
}

// DefaultConfig matches the statusd [herdr] defaults, so cutover with no
// config file paints the rows the fleet already shows.
func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		Method:       "pane.report_metadata",
		Agent:        "claude",
		TTL:          6 * time.Hour,
		DisplayAgent: "{{PROFILE}}",
		Tokens: map[string]string{
			"id":      "{{SESSION_ID_SHORT}}",
			"session": "{{CCC_SESSION}}",
			"role":    "{{ROLE}}",
			"name":    "{{NAME}}",
		},
		AUQLabel: "AUQ",
		AUQLease: 20 * time.Minute,
	}
}

// ConfigPath resolves the config file: $CCC_HERDR_CONFIG, else
// $UCC_HOME/config/ccc-herdr.star when one exists, else the TOML sibling.
// Cleaned — hot-reload compares this against cleaned fsnotify event paths.
func ConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("CCC_HERDR_CONFIG")); p != "" {
		return filepath.Clean(p)
	}
	base := strings.TrimSpace(os.Getenv("UCC_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share", "ucc")
	}
	star := filepath.Join(base, "config", "ccc-herdr.star")
	if _, err := os.Stat(star); err == nil {
		return star
	}
	return filepath.Join(base, "config", "ccc-herdr.toml")
}

// ConfigNote renders the config path for logs and `check`. A missing file is
// legal (built-in defaults) but must never be INVISIBLE: an unconfigured host
// and a mis-pathed one look identical in the panes, so the difference has to
// be readable without opening the source.
func ConfigNote(path string) string {
	if strings.TrimSpace(path) == "" {
		return "none resolvable (UCC_HOME unset?) — built-in defaults"
	}
	if _, err := os.Stat(path); err != nil {
		if strings.HasSuffix(path, ".toml") {
			// ConfigPath only falls back to the .toml sibling when no .star
			// exists, so a missing .toml means BOTH are absent. Naming just
			// the fallback reads as "this host wanted a toml".
			return fmt.Sprintf("no ccc-herdr.star or ccc-herdr.toml in %s — built-in defaults", filepath.Dir(path))
		}
		return path + " (missing — built-in defaults)"
	}
	return path
}

// LoadConfig reads and resolves the config file — Starlark when the path
// ends in .star, TOML otherwise. A missing file is the defaults (not an
// error); a parse failure degrades to defaults LOUDLY via diag — an
// unreadable config must never dark a whole fleet's labels.
func LoadConfig(path string, diag Diag) Config {
	if diag == nil {
		diag = func(string, ...any) {}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			diag("config %s: %v — using defaults", path, err)
		}
		return DefaultConfig()
	}
	if strings.HasSuffix(path, ".star") {
		return loadStarConfig(path, data, diag)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		diag("config %s: %v — using defaults", path, err)
		return DefaultConfig()
	}
	return resolveConfig(raw, diag)
}

// resolveConfig overlays the raw file onto the defaults PER KEY: a file
// carrying only [auq] leaves the identity report intact. A present-but-
// wrong-typed key warns and keeps the default — an authored value must never
// vanish silently, and the kill switch fails OPEN.
func resolveConfig(raw map[string]any, diag Diag) Config {
	cfg := DefaultConfig()

	painter, ok := tableOf(raw, "painter", diag)
	if ok {
		for key, v := range painter {
			switch key {
			case "enabled":
				if b, isBool := v.(bool); isBool {
					cfg.Enabled = b
				} else {
					diag("[painter] enabled: must be a bare bool, got %s — painter stays ON", tomlType(v))
				}
			case "method":
				resolveString(&cfg.Method, "painter."+key, v, false, diag)
			case "agent":
				resolveString(&cfg.Agent, "painter."+key, v, true, diag)
			case "ttl":
				resolveDuration(&cfg.TTL, "painter.ttl", v, diag)
			case "display_agent":
				resolveString(&cfg.DisplayAgent, "painter."+key, v, true, diag)
			case "tokens":
				m, isTable := v.(map[string]any)
				if !isTable {
					diag("[painter] tokens: must be a table ([painter.tokens]), got %s — keeping default tokens", tomlType(v))
					continue
				}
				// Authoring [painter.tokens] REPLACES the whole default map —
				// a row is designed as a whole, never merged.
				tokens := map[string]string{}
				for name, tv := range m {
					s, isStr := tv.(string)
					if !isStr {
						diag("[painter] tokens.%s: value must be a string template, got %s — token dropped", name, tomlType(tv))
						continue
					}
					tokens[name] = s
				}
				cfg.Tokens = tokens
			case "params":
				m, isTable := v.(map[string]any)
				if !isTable {
					diag("[painter] params: must be a table ([painter.params]), got %s — ignored", tomlType(v))
					continue
				}
				cfg.Params = m
			default:
				diag("[painter] %s: unknown key — ignored (known: enabled, method, agent, ttl, display_agent, tokens, params)", key)
			}
		}
	}

	auq, ok := tableOf(raw, "auq", diag)
	if ok {
		for key, v := range auq {
			switch key {
			case "blocked_label":
				resolveString(&cfg.AUQLabel, "auq."+key, v, true, diag)
				// herdr drops a label that normalizes to empty and then
				// rejects the whole report — whitespace means disable.
				if cfg.AUQLabel != "" && strings.TrimSpace(cfg.AUQLabel) == "" {
					diag("[auq.blocked_label]: whitespace-only — treating as disabled (\"\")")
					cfg.AUQLabel = ""
				}
			case "lease":
				resolveDuration(&cfg.AUQLease, "auq.lease", v, diag)
			default:
				diag("[auq] %s: unknown key — ignored (known: blocked_label, lease)", key)
			}
		}
	}

	for key := range raw {
		if key != "painter" && key != "auq" {
			diag("[%s]: unknown table — ignored (known: painter, auq)", key)
		}
	}
	return cfg
}

func tableOf(raw map[string]any, name string, diag Diag) (map[string]any, bool) {
	v, present := raw[name]
	if !present {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		diag("[%s]: must be a table, got %s — ignored", name, tomlType(v))
		return nil, false
	}
	return m, true
}

func resolveString(dst *string, key string, raw any, allowEmpty bool, diag Diag) {
	s, ok := raw.(string)
	if !ok {
		diag("[%s]: must be a string, got %s — keeping default %q", key, tomlType(raw), *dst)
		return
	}
	if s == "" && !allowEmpty {
		diag("[%s]: must not be empty — keeping default %q", key, *dst)
		return
	}
	*dst = s
}

// herdrMaxTTL is herdr's ttl_ms ceiling (86,400,000 ms) — a larger lease is
// rejected on EVERY send (invalid_metadata_ttl), darkening the whole fleet
// from one config edit, so the resolver bounds it here with a diagnostic.
const herdrMaxTTL = 24 * time.Hour

func resolveDuration(dst *time.Duration, key string, raw any, diag Diag) {
	s, ok := raw.(string)
	if !ok {
		diag("[%s]: must be a duration string like \"6h\" (\"0\" = none), got %s — keeping default %s", key, tomlType(raw), *dst)
		return
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		diag("[%s] %q: not a valid duration — keeping default %s", key, s, *dst)
		return
	}
	if d > herdrMaxTTL {
		diag("[%s] %q: herdr caps metadata TTL at 24h — clamped", key, s)
		d = herdrMaxTTL
	}
	*dst = d
}

// tomlType names a decoded TOML value's type in author vocabulary.
func tomlType(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a bool"
	case int64, int, float64:
		return "a number"
	case map[string]any:
		return "a table"
	case []any:
		return "an array"
	default:
		return fmt.Sprintf("%T", v)
	}
}
