package jump

import (
	"testing"

	"github.com/caoer/ccc-herdr/internal/herdr"
)

func snapshotFixture() *herdr.Snapshot {
	return &herdr.Snapshot{
		Panes: []herdr.Pane{
			{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Cwd: "/tmp/shell"},
			{
				PaneID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1",
				Agent: "claude", AgentStatus: "working",
				TerminalTitle: "Spawn lineage backfill",
				Tokens: map[string]string{
					"id": "ad3009b4", "role": "worker",
					"session": "02-00-adhoc", "profile": "grid-mullein1b",
				},
			},
			{
				PaneID: "w2:p1", TabID: "w2:t1", WorkspaceID: "w2",
				Agent: "claude", AgentStatus: "idle",
				TerminalTitle: "reader",
				Tokens: map[string]string{
					"id": "94485806", "name": "reader", "role": "advisor",
					"session": "02-00-adhoc",
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

func TestBuildExcludesSelfAndOrdersAgentsFirst(t *testing.T) {
	c := Build(snapshotFixture(), "w2:p9")
	if len(c) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(c))
	}
	if c[0].PaneID != "w2:p1" {
		t.Fatalf("most recent agent first, got %s", c[0].PaneID)
	}
	if c[1].PaneID != "w1:p2" {
		t.Fatalf("older agent second, got %s", c[1].PaneID)
	}
	if c[2].IsAgent {
		t.Fatal("plain pane must sort last")
	}
}

func TestBuildResolvesNameAndLabels(t *testing.T) {
	c := Build(snapshotFixture(), "")
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
}

func TestFilterFindsById(t *testing.T) {
	c := Build(snapshotFixture(), "")
	got := Filter(c, "ad3009b4")
	if len(got) != 1 || c[got[0]].PaneID != "w1:p2" {
		t.Fatalf("id lookup failed: %v", got)
	}
}

func TestFilterFindsByRoleAndTitle(t *testing.T) {
	c := Build(snapshotFixture(), "")
	got := Filter(c, "advisor")
	if len(got) == 0 || c[got[0]].PaneID != "w2:p1" {
		t.Fatalf("role lookup failed: %v", got)
	}
	got = Filter(c, "lineage backfill")
	if len(got) == 0 || c[got[0]].PaneID != "w1:p2" {
		t.Fatalf("title lookup failed: %v", got)
	}
}

func TestFilterEmptyQueryKeepsOrder(t *testing.T) {
	c := Build(snapshotFixture(), "")
	got := Filter(c, "")
	if len(got) != len(c) {
		t.Fatalf("empty query must keep all %d, got %d", len(c), len(got))
	}
	for i, idx := range got {
		if idx != i {
			t.Fatalf("empty query must keep Build order, got %v", got)
		}
	}
}
