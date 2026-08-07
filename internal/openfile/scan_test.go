package openfile

import (
	"os"
	"path/filepath"
	"testing"
)

// mkfiles creates each relative path under dir and returns dir.
func mkfiles(t *testing.T, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func paths(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func TestScanResolvesRelativeAndAbsolute(t *testing.T) {
	dir := mkfiles(t, "main.go", "sub/config.yaml")
	text := "edited main.go\nwrote " + filepath.Join(dir, "sub/config.yaml")

	entries := Scan(text, dir)

	want := []string{filepath.Join(dir, "sub/config.yaml"), filepath.Join(dir, "main.go")}
	got := paths(entries)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v (most recent line first)", got, want)
	}
}

func TestScanLineSuffix(t *testing.T) {
	dir := mkfiles(t, "a.go")
	entries := Scan("a.go:12:5 error", dir)
	if len(entries) != 1 || entries[0].Line != 12 {
		t.Fatalf("got %+v, want one entry with Line 12", entries)
	}
}

func TestScanDedupKeepsMostRecent(t *testing.T) {
	dir := mkfiles(t, "a.go")
	entries := Scan("a.go:3\na.go:7", dir)
	if len(entries) != 1 || entries[0].Line != 7 {
		t.Fatalf("got %+v, want one entry with Line 7 (most recent occurrence)", entries)
	}
}

func TestScanSkipsMissingDirsAndUnknownExtensions(t *testing.T) {
	dir := mkfiles(t, "real.go", "pkg/keep.md")
	text := "ghost.go pkg real.go archive.tar.zz pkg/keep.md"

	entries := Scan(text, dir)

	if len(entries) != 2 {
		t.Fatalf("got %v, want only real.go and pkg/keep.md", paths(entries))
	}
}

func TestScanTildeAndDisplay(t *testing.T) {
	home := mkfiles(t, "notes/todo.md")
	t.Setenv("HOME", home)

	entries := Scan("~/notes/todo.md", "")

	if len(entries) != 1 {
		t.Fatalf("got %v, want one entry", paths(entries))
	}
	if entries[0].Path != filepath.Join(home, "notes/todo.md") {
		t.Fatalf("Path = %q, want resolved under home", entries[0].Path)
	}
	if entries[0].Display != "~/notes/todo.md" {
		t.Fatalf("Display = %q, want home-abbreviated", entries[0].Display)
	}
}

func TestScanRelativeWithoutCwd(t *testing.T) {
	if entries := Scan("orphan.go", ""); len(entries) != 0 {
		t.Fatalf("got %v, want none: relative paths need a cwd", paths(entries))
	}
}

func TestFilterEmptyQueryKeepsScanOrder(t *testing.T) {
	entries := []Entry{
		{haystack: "b.go"}, {haystack: "a.go"}, {haystack: "c.md"},
	}
	got := Filter(entries, "")
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("got %v, want [0 1 2]", got)
	}
}

func TestFilterMatchesAndExcludes(t *testing.T) {
	entries := []Entry{
		{haystack: "internal/openfile/scan.go"},
		{haystack: "readme.md"},
	}
	got := Filter(entries, "scango")
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("got %v, want only index 0", got)
	}
}
