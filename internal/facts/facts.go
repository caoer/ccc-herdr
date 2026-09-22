// Package facts reads the ccc fact surfaces the painter renders from: the
// statusd session cache, the daemon's session map, and agent-card frontmatter.
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
	// LastSessionStart / LastSessionEnd are the daemon's SessionStart and
	// SessionEnd stamps; a live session carries Go's zero time as its end.
	LastSessionStart time.Time `json:"last_session_start_time"`
	LastSessionEnd   time.Time `json:"last_session_end_time"`
	Model            struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

// Ended reports a session whose last SessionEnd is at or after its last
// SessionStart: the process left the pane (exit, /clear, a resume under a new
// id). Its cache is still rewritten — on its SessionEnd, and wholesale on every
// daemon restart — and those writes must never carry its id onto the pane.
func (c Cache) Ended() bool {
	return !c.LastSessionEnd.IsZero() && !c.LastSessionEnd.Before(c.LastSessionStart)
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

// SessionMapPaths lists where session-map.json may live, in read order: the
// daemon-owned file under its cache dir (ccc-statusd internal/sessionmap,
// daemon-identity-absorb 2026-08-21), then the retired ccc-cli writer's file —
// read only while the daemon file does not exist, so a host that never crossed
// the deploy still resolves. Neither is ever written here.
func SessionMapPaths() []string {
	var paths []string
	if dir := CacheDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "session-map.json"))
	}
	if override := strings.TrimSpace(os.Getenv("CCC_CLI_CACHE_DIR")); override != "" {
		return append(paths, filepath.Join(filepath.Clean(override), "session-map.json"))
	}
	if base := uccHome(); base != "" {
		paths = append(paths, filepath.Join(base, "cache", "ccc-cli", "session-map.json"))
	}
	return paths
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

// LoadSessionMap reads the whole session map — the first file of
// SessionMapPaths that exists; nil on any miss (lenient reader — the daemon
// rebuilds a corrupt file one entry at a time, never the painter).
func LoadSessionMap() map[string]MapEntry {
	for _, path := range SessionMapPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m map[string]MapEntry
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		return m
	}
	return nil
}

// readCard parses the agent card's frontmatter; empty on any miss.
func readCard(path string) map[string]string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseFrontmatter(string(data))
}

// Role resolves the session's role: agent-file frontmatter, then the sticky
// last_known_role cache fallback (a transiently missing agent file must not
// blank the label).
func Role(agentFile string, c Cache) string {
	return roleFrom(readCard(agentFile), c)
}

func roleFrom(fm map[string]string, c Cache) string {
	if r := roleFromFrontmatter(fm); r != "" {
		return r
	}
	return c.LastKnownRole
}

var knownRoles = map[string]bool{"advisor": true, "leader": true, "worker": true}

func roleFromFrontmatter(fm map[string]string) string {
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

// sessionFrom resolves the session slug the pane shows. The CARD is the seat
// record (ccc-base § Boot): the map's stored dir is only the birth dir — the
// day's adhoc tree — and a seat re-seats itself by putting `session:` / `seat:`
// on its card. Precedence mirrors the daemon's sessionmap.Resolve:
//
//  1. `session:` — the slug itself.
//  2. `seat:` — `[[<session-rel>/rosters/<name>]]`; the slug is the last path
//     segment before /rosters/.
//  3. the stored SessionDir's basename.
//  4. "" — unjoined; the caller shows the ad-hoc default.
func sessionFrom(fm map[string]string, entry MapEntry) string {
	if slug := fm["session"]; slug != "" {
		return slug
	}
	if seat := fm["seat"]; seat != "" {
		seat = strings.TrimSuffix(strings.TrimPrefix(seat, "[["), "]]")
		if i := strings.Index(seat, "/rosters/"); i > 0 {
			return filepath.Base(seat[:i])
		}
	}
	if entry.SessionDir != "" {
		return filepath.Base(entry.SessionDir)
	}
	return ""
}

// Unquote strips one pair of matching surrounding quotes from a frontmatter
// scalar (`role: "worker"` is how the engine writes it). Applied by
// ParseFrontmatter to EVERY value, so no reader can forget: a quoted `role`
// reached the panes as `"worker"` — visibly wrong, and invisibly wrong too,
// since the config matches ROLE against advisor/leader/worker to pick the
// per-role color token and a quoted value matches none of them (2026-08-23).
//
// The interior must hold no bare quote of the same kind, so a prose value like
// `"a" and "b"` is left alone rather than mangled into `a" and "b`.
func Unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] &&
		!strings.ContainsRune(v[1:len(v)-1], rune(v[0])) {
		return v[1 : len(v)-1]
	}
	return v
}

// ParseFrontmatter matches ccc-cli parseFrontmatter: first `---` pair from
// line 0; first-colon split; indented lines are continuations (skipped); last
// value wins; an unclosed opener is NOT a block. Values are Unquoted — the
// quotes the engine writes are YAML syntax, never content, and every reader
// here renders the value.
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
		fields[strings.TrimSpace(line[:colon])] = Unquote(line[colon+1:])
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
	card := readCard(entry.AgentFile) // one read serves role and session
	vars := map[string]string{
		"SESSION_ID":       sessionID,
		"SESSION_ID_SHORT": sessionID,
		"CCC_SESSION":      "ad-hoc", // unjoined default
		"ROLE":             roleFrom(card, c),
		"TITLE":            c.CustomTitle,
		"MODEL":            c.Model.ID,
		"PROFILE":          Profile(c),
		"STATUS":           c.Status,
		"CONTEXT_TOKENS":   itoa(c.ContextTokens),
		"CONTEXT_PERCENT":  itoa(c.ContextPercent),
		"COST":             trimFloat(c.Cost),
		"IDLE":             formatIdle(time.Since(c.LastHookEvent), c.LastHookEvent.IsZero()),
	}
	if len(sessionID) >= 8 {
		vars["SESSION_ID_SHORT"] = sessionID[:8]
	}
	if s := sessionFrom(card, entry); s != "" {
		vars["CCC_SESSION"] = s
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

// formatIdle renders time since the last hook event, minute-truncated so the
// identity content hash changes at most once a minute (one sweep-driven send
// per session per minute, not per repaint). Under a minute — an ACTIVE
// session — renders empty (token clear): idle is a marker for quiet panes,
// not a stopwatch on busy ones. Unknown or future timestamps render empty.
func formatIdle(d time.Duration, unknown bool) string {
	if unknown || d < time.Minute {
		return ""
	}
	m := int(d.Minutes())
	switch {
	case m < 60:
		return strconv.Itoa(m) + "m"
	case m < 24*60:
		if m%60 == 0 {
			return strconv.Itoa(m/60) + "h"
		}
		return strconv.Itoa(m/60) + "h" + strconv.Itoa(m%60) + "m"
	default:
		days, hours := m/(24*60), m%(24*60)/60
		if hours == 0 {
			return strconv.Itoa(days) + "d"
		}
		return strconv.Itoa(days) + "d" + strconv.Itoa(hours) + "h"
	}
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
