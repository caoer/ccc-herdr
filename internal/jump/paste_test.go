package jump

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPasteAppendsToQuery(t *testing.T) {
	m := New([]Candidate{{PaneID: "%1", ID: "3ced7646"}})
	out, _ := m.Update(tea.PasteMsg{Content: "3ced7646\n"})
	got := out.(Model)
	if got.query != "3ced7646" {
		t.Fatalf("query = %q, want %q", got.query, "3ced7646")
	}
	// refilter must run on paste: the literal candidate has an empty
	// haystack, so a non-empty query drops it from the view.
	if len(got.view) != 0 {
		t.Fatalf("view = %d rows, want 0 (refilter did not run)", len(got.view))
	}
}

func TestPasteMultiLineFlattens(t *testing.T) {
	m := New(nil)
	out, _ := m.Update(tea.PasteMsg{Content: "one\ttwo\nthree"})
	if q := out.(Model).query; q != "one two three" {
		t.Fatalf("query = %q, want %q", q, "one two three")
	}
}
