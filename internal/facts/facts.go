// Package facts reads the ccc fact surfaces the painter renders from: the
// statusd session cache, the ccc-cli session map, and agent-file frontmatter.
// Read-only — ccc-herdr never writes a fact.
package facts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Cache is the painter's subset of the statusd session cache
// (ccc-statusd/internal/cache/models.go StatusCache — field tags must match).
type Cache struct {
	SessionID       string    `json:"session_id"`
	CustomTitle     string    `json:"custom_title"`
	Status          string    `json:"status"`
	ContextTokens   int       `json:"context_tokens"`
	ContextPercent  int       `json:"context_percent"`
	Cost            float64   `json:"cost"`
	LastKnownRole   string    `json:"last_known_role"`
	ClaudeConfigDir string    `json:"claude_config_dir"`
	LaunchProfile   string    `json:"launch_profile"`
	TranscriptPath  string    `json:"transcript_path"`
	HerdrPaneID     string    `json:"herdr_pane_id"`
	HerdrSocketPath string    `json:"herdr_socket_path"`
	AUQPending      int       `json:"auq_pending"`
	LastHookEvent   time.Time `json:"last_hook_event_time"`
	Model           struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

// MapEntry is one ccc-cli session-map record (Decision #10 schema).
type MapEntry struct {
	SessionDir string `json:"sessionDir"`
	AgentFile  string `json:"agentFile"`
}

// uccHome resolves $UCC_HOME with the standard fallback.
func uccHome() string {
	if base := strings.TrimSpace(os.Getenv("UCC_HOME")); base != "" {
		return base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "ucc")
}

// CacheDir is the statusd session-cache directory (one JSON per session).
// Cleaned: callers compare it against filepath.Dir of fsnotify event paths,
// and a trailing slash in the env var would silently fail every comparison.
func CacheDir() string {
	if dir := strings.TrimSpace(os.Getenv("CCC_CACHE_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	base := uccHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "cache", "ccc-status")
}

// SessionMapPath mirrors ccc-cli's cachePath resolution.
func SessionMapPath() string {
	if override := strings.TrimSpace(os.Getenv("CCC_CLI_CACHE_DIR")); override != "" {
		return filepath.Join(filepath.Clean(override), "session-map.json")
	}
	base := uccHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "cache", "ccc-cli", "session-map.json")
}

// ReadCache parses one session cache file.
func ReadCache(path string) (Cache, error) {
	var c Cache
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// LoadSessionMap reads the whole session map; nil on any miss (lenient
// reader — the TS side is the validator that rebuilds a corrupt file).
func LoadSessionMap() map[string]MapEntry {
	path := SessionMapPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]MapEntry
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

// Role resolves the session's role: agent-file frontmatter, then the sticky
// last_known_role cache fallback (a transiently missing agent file must not
// blank the label).
func Role(agentFile string, c Cache) string {
	if r := roleFromAgentFile(agentFile); r != "" {
		return r
	}
	return c.LastKnownRole
}

var knownRoles = map[string]bool{"advisor": true, "leader": true, "worker": true}

func roleFromAgentFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm := ParseFrontmatter(string(data))
	if r := strings.TrimSpace(fm["role"]); r != "" {
		return r
	}
	if r := strings.TrimSpace(fm["agent_role"]); r != "" {
		return r
	}
	// The frozen `type` field counts only when it holds a known role — the
	// default `type: agent` note-type is never a role.
	if t := strings.TrimSpace(fm["type"]); knownRoles[t] {
		return t
	}
	return ""
}

// ParseFrontmatter matches ccc-cli parseFrontmatter: first `---` pair from
// line 0; first-colon split; indented lines are continuations (skipped); last
// value wins; an unclosed opener is NOT a block.
func ParseFrontmatter(content string) map[string]string {
	fields := map[string]string{}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !isDelimiter(lines[0]) {
		return fields
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if isDelimiter(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return fields
	}
	for i := 1; i < end; i++ {
		line := lines[i]
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon == -1 {
			continue
		}
		fields[strings.TrimSpace(line[:colon])] = strings.TrimSpace(line[colon+1:])
	}
	return fields
}

func isDelimiter(line string) bool {
	line = strings.TrimPrefix(line, "\ufeff") // tolerate leading BOM
	return line == "---" || line == "---\r"
}

// Profile resolves the SESSION's ucc profile from session facts only —
// launcher-declared, else derived from the transcript path, else the
// session's own CLAUDE_CONFIG_DIR. Never the painter's process env: the
// painter composes for sessions it did not spawn.
func Profile(c Cache) string {
	if c.LaunchProfile != "" {
		return c.LaunchProfile
	}
	if p := profileFromTranscriptPath(c.TranscriptPath); p != "" {
		return p
	}
	if cd := strings.TrimRight(c.ClaudeConfigDir, "/"); cd != "" {
		return filepath.Base(cd)
	}
	return ""
}

func profileFromTranscriptPath(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		if part == "profiles" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// Vars builds the {{VAR}} values for template interpolation. All values are
// SESSION facts. A fact that does not exist yet is the empty string, which
// the composer maps onto herdr's token-clear convention.
func Vars(sessionID string, c Cache, entry MapEntry) map[string]string {
	vars := map[string]string{
		"SESSION_ID":       sessionID,
		"SESSION_ID_SHORT": sessionID,
		"CCC_SESSION":      "ad-hoc", // unjoined default
		"ROLE":             Role(entry.AgentFile, c),
		"TITLE":            c.CustomTitle,
		"MODEL":            c.Model.ID,
		"PROFILE":          Profile(c),
		"STATUS":           c.Status,
		"CONTEXT_TOKENS":   itoa(c.ContextTokens),
		"CONTEXT_PERCENT":  itoa(c.ContextPercent),
		"COST":             trimFloat(c.Cost),
	}
	if len(sessionID) >= 8 {
		vars["SESSION_ID_SHORT"] = sessionID[:8]
	}
	if entry.SessionDir != "" {
		vars["CCC_SESSION"] = filepath.Base(entry.SessionDir)
	}
	// NAME is the convenience resolution the default row uses: the /rename
	// title while one exists, else the ucc profile.
	vars["NAME"] = vars["TITLE"]
	if vars["NAME"] == "" {
		vars["NAME"] = vars["PROFILE"]
	}
	// PROFILE_IF_UNNAMED: the profile only while no title exists, empty (→
	// token clear) once one does — style title and profile differently without
	// ever showing both. The {{VAR}} engine is pure substitution, so the
	// conditional lives here. Pair with {{TITLE}}, never {{NAME}}.
	vars["PROFILE_IF_UNNAMED"] = ""
	if strings.TrimSpace(vars["TITLE"]) == "" {
		vars["PROFILE_IF_UNNAMED"] = vars["PROFILE"]
	}
	return vars
}

// itoa / trimFloat render numeric facts; zero renders empty (unknown fact →
// token clear), matching the string facts' convention.
func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func trimFloat(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}
