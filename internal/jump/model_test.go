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
	m := New(Build(snapshotFixture(), ""))
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
	m := New(Build(snapshotFixture(), ""))
	m.width = 80
	if w := lipgloss.Width(m.row(m.candidates[0], false)); w != 80 {
		t.Fatalf("unselected row must fill the popup width: got %d", w)
	}
}
