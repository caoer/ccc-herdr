package jump

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caoer/ccc-herdr/internal/facts"
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
	ID        string        // ccc session short id (tokens.id)
	Name      string        // tokens.name, else profile, else display_agent
	Role      string        // tokens.role
	Session   string        // tokens.session
	Band      Band          // prompt-cache band, the desk LED's classes
	Age       time.Duration // since last activity (statusd's LIVENESS clock)
	AgeKnown  bool
	Title     string // stripped terminal title
	Dir       string // foreground cwd, home-abbreviated
	Seq       int    // agent activity counter, recency proxy

	haystack string
}

// Build flattens a snapshot into candidates, excluding selfPane (the popup's
// own pane). caches (facts.LiveByShort) joins each pane's `id` token to its
// session cache for the activity clock, status and pending AUQ; a pane
// without one falls back to herdr's agent_status and has no age.
// Order: band (the LED's order), then freshest activity, then recency, with
// plain panes after agents.
func Build(snap *herdr.Snapshot, selfPane string, caches map[string]facts.Cache, now time.Time) []Candidate {
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
			Role:      roleOf(p.Tokens),
			Session:   p.Tokens["session"],
			Title:     p.TerminalTitle,
			Dir:       abbreviate(dir, home),
			Seq:       seqs[p.PaneID],
		}
		blocked, working := p.AgentStatus == "blocked", p.AgentStatus == "working"
		if cache, ok := caches[c.ID]; ok && c.ID != "" {
			blocked = blocked || cache.AUQPending > 0
			working = cache.Status == "working"
			if last := cache.LastActivity(); !last.IsZero() {
				c.Age, c.AgeKnown = max(now.Sub(last), 0), true
			}
		}
		if c.IsAgent {
			c.Band = classify(blocked, working, c.Age, c.AgeKnown)
		} else {
			c.Band = BandNone
		}
		c.haystack = strings.ToLower(strings.Join([]string{
			c.ID, name, p.Tokens["profile"], c.Role, c.Session,
			c.Title, p.Agent, c.Workspace, c.Tab, dir, c.PaneID,
		}, " "))
		candidates = append(candidates, c)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Band != b.Band {
			return a.Band < b.Band
		}
		if a.IsAgent != b.IsAgent {
			return a.IsAgent
		}
		if a.AgeKnown && b.AgeKnown && a.Age != b.Age {
			return a.Age < b.Age
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
// band < 0 keeps every band; otherwise only candidates in that band.
func Filter(candidates []Candidate, query string, band Band) []int {
	type hit struct{ idx, score int }
	hits := make([]hit, 0, len(candidates))
	for i, c := range candidates {
		if band >= 0 && c.Band != band {
			continue
		}
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

// AgeText is the time since last activity ("<1m", "42m", "2h39m", "1d3h");
// "" when unknown.
func (c Candidate) AgeText() string {
	if !c.AgeKnown {
		return ""
	}
	if c.Age < time.Minute {
		return "<1m"
	}
	return facts.FormatIdle(c.Age, false)
}

// TTLText is what is left of the prompt cache: "12m left" in an idle band,
// "expired" past it, "" for blocked, working and unknown.
func (c Candidate) TTLText() string {
	switch {
	case c.Band == BandExpired:
		return "expired"
	case c.Band >= BandIdle10 && c.Band <= BandIdle60:
		left := (cacheTTL - c.Age).Truncate(time.Minute)
		if left < time.Minute {
			return "<1m left"
		}
		return facts.FormatIdle(left, false) + " left"
	}
	return ""
}

// Roles are the per-role painter tokens (role_worker / role_leader /
// role_advisor, exactly one nonempty), in tab order.
var Roles = []string{"worker", "leader", "advisor"}

// roleOf reads the pane's role: the painter writes a known role into its
// per-role token and only an unknown one into plain `role`.
func roleOf(tokens map[string]string) string {
	for _, r := range Roles {
		if v := tokens["role_"+r]; v != "" {
			return v
		}
	}
	return tokens["role"]
}
