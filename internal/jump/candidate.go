package jump

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

// Candidate is one jumpable pane with everything the fuzzy search and the
// list rendering need.
type Candidate struct {
	PaneID    string
	Workspace string
	Tab       string
	Status    string // agent_status: working / idle / done / blocked / unknown
	IsAgent   bool
	ID        string // ccc session short id (tokens.id)
	Name      string // tokens.name, else profile, else display_agent
	Role      string // tokens.role
	Session   string // tokens.session
	Idle      string // tokens.idle — time since last hook event, "" while active
	Title     string // stripped terminal title
	Dir       string // foreground cwd, home-abbreviated
	Seq       int    // agent activity counter, recency proxy

	haystack string
}

// Build flattens a snapshot into candidates, excluding selfPane (the popup's
// own pane). Order: agents by recency, then plain panes.
func Build(snap *herdr.Snapshot, selfPane string) []Candidate {
	tabs := make(map[string]string, len(snap.Tabs))
	for _, t := range snap.Tabs {
		tabs[t.TabID] = t.Label
	}
	workspaces := make(map[string]string, len(snap.Workspaces))
	for _, w := range snap.Workspaces {
		workspaces[w.WorkspaceID] = w.Label
	}
	seqs := make(map[string]int, len(snap.Agents))
	for _, a := range snap.Agents {
		seqs[a.PaneID] = a.Seq
	}
	home, _ := os.UserHomeDir()

	candidates := make([]Candidate, 0, len(snap.Panes))
	for _, p := range snap.Panes {
		if p.PaneID == selfPane {
			continue
		}
		dir := p.ForegroundCwd
		if dir == "" {
			dir = p.Cwd
		}
		name := p.Tokens["name"]
		if name == "" {
			name = p.Tokens["profile"]
		}
		if name == "" {
			name = p.DisplayAgent
		}
		c := Candidate{
			PaneID:    p.PaneID,
			Workspace: workspaces[p.WorkspaceID],
			Tab:       tabs[p.TabID],
			Status:    p.AgentStatus,
			IsAgent:   p.Agent != "",
			ID:        p.Tokens["id"],
			Name:      name,
			Role:      p.Tokens["role"],
			Session:   p.Tokens["session"],
			Idle:      p.Tokens["idle"],
			Title:     p.TerminalTitle,
			Dir:       abbreviate(dir, home),
			Seq:       seqs[p.PaneID],
		}
		c.haystack = strings.ToLower(strings.Join([]string{
			c.ID, name, p.Tokens["profile"], c.Role, c.Session,
			c.Title, p.Agent, c.Workspace, c.Tab, dir, c.PaneID,
		}, " "))
		candidates = append(candidates, c)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.IsAgent != b.IsAgent {
			return a.IsAgent
		}
		if a.Seq != b.Seq {
			return a.Seq > b.Seq
		}
		return a.PaneID < b.PaneID
	})
	return candidates
}

// Filter returns indexes into candidates that match query, best first.
// Candidate order breaks score ties, so the empty query keeps Build's order.
func Filter(candidates []Candidate, query string) []int {
	type hit struct{ idx, score int }
	hits := make([]hit, 0, len(candidates))
	for i, c := range candidates {
		if s, ok := Score(query, c.haystack); ok {
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

func abbreviate(dir, home string) string {
	if home != "" && strings.HasPrefix(dir, home) {
		return "~" + dir[len(home):]
	}
	return dir
}

// Base returns the last path segment of the candidate's directory.
func (c Candidate) Base() string {
	return filepath.Base(c.Dir)
}
