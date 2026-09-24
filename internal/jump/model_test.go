package jump

import (
	"regexp"
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Every styled segment ends in a full SGR reset, so the selection background
// must be restated inside every segment — one outer wrap paints only up to
// the first styled token. This pins that every escape sequence in a selected
// row carries the background.
func TestRowSelectedBackgroundEverySegment(t *testing.T) {
	m := New(build(""))
	m.width = 80
	row := m.row(m.candidates[0], true)
	if w := lipgloss.Width(row); w != 80 {
		t.Fatalf("selected row must fill the popup width: got %d", w)
	}
	found := 0
	for _, seq := range sgr.FindAllString(row, -1) {
		if seq == "\x1b[m" || seq == "\x1b[0m" {
			continue
		}
		found++
		if !strings.Contains(seq, "48;5;237") {
			t.Fatalf("segment without selection background: %q in %q", seq, row)
		}
	}
	if found == 0 {
		t.Fatal("selected row rendered no styled segments")
	}
}

func TestRowUnselectedFillsWidth(t *testing.T) {
	m := New(build(""))
	m.width = 80
	if w := lipgloss.Width(m.row(m.candidates[0], false)); w != 80 {
		t.Fatalf("unselected row must fill the popup width: got %d", w)
	}
}

// ← from all lands on the last tab, advisor; → walks all, the non-empty
// bands in LED order, then worker, leader, advisor, and wraps.
func TestTabsEndWithRoles(t *testing.T) {
	m := New(build(""))
	m.cycleTab(-1)
	if m.tab.role != "advisor" || len(m.view) != 1 || m.candidates[m.view[0]].PaneID != "w2:p1" {
		t.Fatalf("← from all: tab %+v view %v", m.tab, m.view)
	}
	want := []tab{allTab, {band: BandWorking}, {band: BandExpired}, {band: BandNone},
		{band: AnyBand, role: "worker"}, {band: AnyBand, role: "leader"}, {band: AnyBand, role: "advisor"}}
	for _, w := range want {
		m.cycleTab(1)
		if m.tab != w {
			t.Fatalf("→: got %+v want %+v", m.tab, w)
		}
	}
	m.cycleTab(-2)
	if m.tab.role != "worker" || len(m.view) != 1 || m.candidates[m.view[0]].Role != "worker" {
		t.Fatalf("worker tab (role_worker token): %+v %v", m.tab, m.view)
	}
}

// The path column appears only when the title keeps its room beside it.
func TestPathColumnOnlyWhenWide(t *testing.T) {
	m := New(build(""))
	c := m.candidates[0] // the worker, with a terminal title and a cwd
	m.width = 100
	if strings.Contains(m.row(c, false), "ad3009b4/") {
		t.Fatal("narrow popup must skip the path column")
	}
	m.width = 180
	if !strings.Contains(m.row(c, false), "ad3009b4/meridian-rs") {
		t.Fatalf("wide popup must show the path: %q", m.row(c, false))
	}
}
