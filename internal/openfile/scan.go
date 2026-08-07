// Package openfile scrapes openable file paths out of pane terminal output
// and picks one in a popup — the plugin-side replacement for the retired
// shell-integration approach (OSC 8 wrapping needed a filter on every
// command's stdout; herdr core drops file:// clicks before plugins see them).
package openfile

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/caoer/ccc-herdr/internal/jump"
)

// Entry is one openable file scraped from pane output.
type Entry struct {
	Path    string // absolute, cleaned
	Display string // home-abbreviated form shown in the list
	Line    int    // 1-based line from a path:line[:col] suffix, 0 = none

	haystack string
}

// Extensions the scanner recognizes — the allowlist the shell filter used,
// plus tsx/mdx siblings of listed extensions.
var extensions = map[string]bool{
	"md": true, "mdx": true, "rs": true, "toml": true, "json": true,
	"yaml": true, "yml": true, "py": true, "go": true, "ts": true,
	"tsx": true, "js": true, "jsx": true, "html": true, "css": true,
	"sh": true, "nix": true, "txt": true, "log": true, "csv": true,
	"lua": true, "conf": true, "cfg": true, "ini": true, "env": true,
	"rb": true, "php": true, "java": true, "kt": true, "swift": true,
	"c": true, "cpp": true, "h": true, "hpp": true, "zig": true,
	"scm": true, "sc": true, "el": true, "ex": true, "exs": true,
	"hs": true, "ml": true, "sql": true, "xml": true, "svg": true,
}

const maxEntries = 300

// tokenPattern captures path-shaped runs: no whitespace, quotes, brackets,
// or colons inside the path; an optional :line[:col] suffix after it.
var tokenPattern = regexp.MustCompile(`[~A-Za-z0-9._@%+/-]+\.[A-Za-z0-9]+(?::\d+(?::\d+)?)?`)

var suffixPattern = regexp.MustCompile(`^(.+\.([A-Za-z0-9]+))(?::(\d+)(?::\d+)?)?$`)

// Scan extracts existing files from terminal text, most recent line first.
// Relative paths resolve against cwd. Duplicate paths keep the most recent
// occurrence (and its line number).
func Scan(text, cwd string) []Entry {
	home, _ := os.UserHomeDir()
	lines := strings.Split(text, "\n")
	seen := make(map[string]bool)
	entries := make([]Entry, 0, 32)
	for i := len(lines) - 1; i >= 0 && len(entries) < maxEntries; i-- {
		for _, token := range tokenPattern.FindAllString(lines[i], -1) {
			entry, ok := resolve(token, cwd, home)
			if !ok || seen[entry.Path] {
				continue
			}
			seen[entry.Path] = true
			entries = append(entries, entry)
			if len(entries) == maxEntries {
				break
			}
		}
	}
	return entries
}

func resolve(token, cwd, home string) (Entry, bool) {
	match := suffixPattern.FindStringSubmatch(token)
	if match == nil || !extensions[strings.ToLower(match[2])] {
		return Entry{}, false
	}
	path := match[1]
	line := 0
	if match[3] != "" {
		line, _ = strconv.Atoi(match[3])
	}

	switch {
	case strings.HasPrefix(path, "~/"):
		if home == "" {
			return Entry{}, false
		}
		path = filepath.Join(home, path[2:])
	case !filepath.IsAbs(path):
		if cwd == "" {
			return Entry{}, false
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Entry{}, false
	}

	display := path
	if home != "" && strings.HasPrefix(display, home+string(filepath.Separator)) {
		display = "~" + display[len(home):]
	}
	entry := Entry{Path: path, Display: display, Line: line}
	entry.haystack = strings.ToLower(display)
	return entry, true
}

// Filter returns indexes into entries that match query, best first. Entry
// order breaks score ties, so the empty query keeps Scan's recency order.
func Filter(entries []Entry, query string) []int {
	type hit struct{ idx, score int }
	hits := make([]hit, 0, len(entries))
	for i, e := range entries {
		if s, ok := jump.Score(query, e.haystack); ok {
			hits = append(hits, hit{i, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.idx
	}
	return out
}
