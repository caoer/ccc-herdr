package jump

import (
	"testing"
	"time"

	"github.com/caoer/ccc-herdr/internal/facts"
	"github.com/caoer/ccc-herdr/internal/herdr"
)

var fixtureNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// cachesFixture: the worker is working per statusd; the advisor's hook stamp
// is fresh (a daemon event) but its last API message is 3h49m old.
func cachesFixture() map[string]facts.Cache {
	return map[string]facts.Cache{
		"ad3009b4": {Status: "working", LastAPIMessage: fixtureNow.Add(-5 * time.Second)},
		"94485806": {Status: "waiting", LastHookEvent: fixtureNow, LastAPIMessage: fixtureNow.Add(-3*time.Hour - 49*time.Minute)},
	}
}

func build(self string) []Candidate {
	return Build(snapshotFixture(), self, cachesFixture(), fixtureNow)
}

func snapshotFixture() *herdr.Snapshot {
	return &herdr.Snapshot{
		Panes: []herdr.Pane{
			{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Cwd: "/tmp/shell"},
			{
				PaneID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1",
				Agent: "claude", AgentStatus: "working",
				TerminalTitle: "Spawn lineage backfill", Cwd: "/Users/x/work/ad3009b4/meridian-rs",
				Tokens: map[string]string{
					"id": "ad3009b4", "role_worker": "worker",
					"session": "02-00-adhoc", "profile": "grid-mullein1b",
				},
			},
			{
				PaneID: "w2:p1", TabID: "w2:t1", WorkspaceID: "w2",
				Agent: "claude", AgentStatus: "idle",
				TerminalTitle: "reader",
				Tokens: map[string]string{
					"id": "94485806", "name": "reader", "role": "advisor",
					"session": "02-00-adhoc", "idle": "3h49m",
				},
			},
			{PaneID: "w2:p9", TabID: "w2:t1", WorkspaceID: "w2"},
		},
		Agents: []herdr.AgentSeq{
			{PaneID: "w1:p2", Seq: 10},
			{PaneID: "w2:p1", Seq: 40},
		},
		Tabs: []herdr.Tab{
			{TabID: "w1:t1", WorkspaceID: "w1", Label: "2"},
			{TabID: "w2:t1", WorkspaceID: "w2", Label: "ccc/leader"},
		},
		Workspaces: []herdr.Workspace{
			{WorkspaceID: "w1", Label: "meridian-rs"},
			{WorkspaceID: "w2", Label: "osfiles"},
		},
	}
}

func TestBuildExcludesSelfAndOrdersByBand(t *testing.T) {
	c := build("w2:p9")
	if len(c) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(c))
	}
	if c[0].PaneID != "w1:p2" || c[0].Band != BandWorking {
		t.Fatalf("working agent first, got %s %s", c[0].PaneID, c[0].Band.Name())
	}
	if c[1].PaneID != "w2:p1" || c[1].Band != BandExpired {
		t.Fatalf("expired agent second, got %s %s", c[1].PaneID, c[1].Band.Name())
	}
	if c[2].IsAgent {
		t.Fatal("plain pane must sort last")
	}
}

func TestBuildResolvesNameAndLabels(t *testing.T) {
	c := build("")
	byPane := map[string]Candidate{}
	for _, cand := range c {
		byPane[cand.PaneID] = cand
	}
	worker := byPane["w1:p2"]
	if worker.Name != "grid-mullein1b" {
		t.Fatalf("name falls back to profile, got %q", worker.Name)
	}
	if worker.Workspace != "meridian-rs" || worker.Tab != "2" {
		t.Fatalf("labels not resolved: %q %q", worker.Workspace, worker.Tab)
	}
	if advisor := byPane["w2:p1"]; advisor.AgeText() != "3h49m" || advisor.TTLText() != "expired" {
		t.Fatalf("age must come from the API-message clock, not the hook stamp: %q %q", advisor.AgeText(), advisor.TTLText())
	}
}

func TestFilterFindsById(t *testing.T) {
	c := build("")
	got := Filter(c, "ad3009b4", AnyBand)
	if len(got) != 1 || c[got[0]].PaneID != "w1:p2" {
		t.Fatalf("id lookup failed: %v", got)
	}
}

func TestFilterFindsByRoleAndTitle(t *testing.T) {
	c := build("")
	got := Filter(c, "advisor", AnyBand)
	if len(got) == 0 || c[got[0]].PaneID != "w2:p1" {
		t.Fatalf("role lookup failed: %v", got)
	}
	got = Filter(c, "lineage backfill", AnyBand)
	if len(got) == 0 || c[got[0]].PaneID != "w1:p2" {
		t.Fatalf("title lookup failed: %v", got)
	}
}

func TestFilterEmptyQueryKeepsOrder(t *testing.T) {
	c := build("")
	got := Filter(c, "", AnyBand)
	if len(got) != len(c) {
		t.Fatalf("empty query must keep all %d, got %d", len(c), len(got))
	}
	for i, idx := range got {
		if idx != i {
			t.Fatalf("empty query must keep Build order, got %v", got)
		}
	}
}

func TestClassifyMatchesLEDBands(t *testing.T) {
	cases := []struct {
		blocked, working bool
		age              time.Duration
		want             Band
	}{
		{true, true, 0, BandBlocked},
		{false, true, 2 * time.Hour, BandWorking},
		{false, false, 9 * time.Minute, BandIdle10},
		{false, false, 10 * time.Minute, BandIdle30},
		{false, false, 49 * time.Minute, BandIdle50},
		{false, false, 59 * time.Minute, BandIdle60},
		{false, false, 60 * time.Minute, BandExpired},
	}
	for _, tc := range cases {
		if got := classify(tc.blocked, tc.working, tc.age, true); got != tc.want {
			t.Errorf("classify(%v,%v,%v) = %s, want %s", tc.blocked, tc.working, tc.age, got.Name(), tc.want.Name())
		}
	}
	if got := classify(false, false, 0, false); got != BandNone {
		t.Errorf("unknown age must be BandNone, got %s", got.Name())
	}
}

func TestTTLText(t *testing.T) {
	c := Candidate{Band: BandIdle50, Age: 42*time.Minute + 30*time.Second, AgeKnown: true}
	if got := c.TTLText(); got != "17m left" {
		t.Fatalf("TTLText: %q", got)
	}
}

func TestFilterByBand(t *testing.T) {
	c := build("")
	got := Filter(c, "", BandExpired)
	if len(got) != 1 || c[got[0]].PaneID != "w2:p1" {
		t.Fatalf("band filter: %v", got)
	}
}
