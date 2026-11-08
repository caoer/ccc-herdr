package reap

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	faintStyle  = lipgloss.NewStyle().Faint(true)
	idStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	markStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	selectedBg  = lipgloss.Color("237")
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// Closer closes one pane; it re-checks idleness first, so a pane that
// started a program after the list was built is refused, not killed.
type Closer func(paneID string) error

// Previewer returns a pane's visible screen with its colors.
type Previewer func(paneID string) (string, error)

type closedMsg struct {
	ids  []string
	errs map[string]error
}

type previewMsg struct {
	id   string
	text string
	err  error
}

// Model is the reaper popup: idle panes on the left, the selected pane's
// screen on the right; enter closes the marked panes, or the selected one.
type Model struct {
	panes    []Idle
	marked   map[string]bool
	previews map[string]string
	errs     map[string]error
	cursor   int
	width    int
	height   int
	closer   Closer
	preview  Previewer
	closed   []string
	busy     bool
}

func New(panes []Idle, closer Closer, preview Previewer) Model {
	return Model{
		panes:    panes,
		marked:   map[string]bool{},
		previews: map[string]string{},
		errs:     map[string]error{},
		width:    120,
		height:   30,
		closer:   closer,
		preview:  preview,
	}
}

// Closed returns the pane ids this run closed.
func (m Model) Closed() []string { return m.closed }

func (m Model) Init() tea.Cmd { return m.fetchPreview() }

func (m Model) current() (Idle, bool) {
	if m.cursor < 0 || m.cursor >= len(m.panes) {
		return Idle{}, false
	}
	return m.panes[m.cursor], true
}

// fetchPreview reads the selected pane once; later visits use the cache.
func (m Model) fetchPreview() tea.Cmd {
	p, ok := m.current()
	if !ok || m.preview == nil {
		return nil
	}
	if _, cached := m.previews[p.PaneID]; cached {
		return nil
	}
	read := m.preview
	return func() tea.Msg {
		text, err := read(p.PaneID)
		return previewMsg{id: p.PaneID, text: text, err: err}
	}
}

// targets is the marked set in list order, or the selected pane alone.
func (m Model) targets() []string {
	var ids []string
	for _, p := range m.panes {
		if m.marked[p.PaneID] {
			ids = append(ids, p.PaneID)
		}
	}
	if len(ids) == 0 {
		if p, ok := m.current(); ok {
			ids = []string{p.PaneID}
		}
	}
	return ids
}

func (m Model) closeCmd(ids []string) tea.Cmd {
	closer := m.closer
	return func() tea.Msg {
		msg := closedMsg{errs: map[string]error{}}
		for _, id := range ids {
			if err := closer(id); err != nil {
				msg.errs[id] = err
				continue
			}
			msg.ids = append(msg.ids, id)
		}
		return msg
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case previewMsg:
		if msg.err != nil {
			m.previews[msg.id] = errStyle.Render("preview failed: " + msg.err.Error())
		} else {
			m.previews[msg.id] = msg.text
		}
		return m, nil

	case closedMsg:
		m.busy = false
		gone := map[string]bool{}
		for _, id := range msg.ids {
			gone[id] = true
			delete(m.marked, id)
		}
		m.closed = append(m.closed, msg.ids...)
		for id, err := range msg.errs {
			m.errs[id] = err
		}
		kept := m.panes[:0:0]
		for _, p := range m.panes {
			if !gone[p.PaneID] {
				kept = append(kept, p)
			}
		}
		m.panes = kept
		if len(m.panes) == 0 {
			return m, tea.Quit
		}
		if m.cursor >= len(m.panes) {
			m.cursor = len(m.panes) - 1
		}
		return m, m.fetchPreview()

	case tea.KeyPressMsg:
		if m.busy {
			return m, nil
		}
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			ids := m.targets()
			if len(ids) == 0 {
				return m, tea.Quit
			}
			m.busy = true
			return m, m.closeCmd(ids)
		case "space":
			if p, ok := m.current(); ok {
				m.marked[p.PaneID] = !m.marked[p.PaneID]
				if m.cursor < len(m.panes)-1 {
					m.cursor++
				}
			}
			return m, m.fetchPreview()
		case "a":
			all := len(m.marked) < len(m.panes)
			m.marked = map[string]bool{}
			if all {
				for _, p := range m.panes {
					m.marked[p.PaneID] = true
				}
			}
			return m, nil
		case "up", "k", "ctrl+p", "shift+tab":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, m.fetchPreview()
		case "down", "j", "ctrl+n", "tab":
			if m.cursor < len(m.panes)-1 {
				m.cursor++
			}
			return m, m.fetchPreview()
		}
	}
	return m, nil
}

// listWidth is the left column; the preview takes the rest.
func (m Model) listWidth() int {
	w := m.width * 2 / 5
	if w < 28 {
		w = 28
	}
	if w > 56 {
		w = 56
	}
	if w > m.width-10 {
		w = m.width - 10
	}
	return w
}

func (m Model) View() tea.View {
	if len(m.panes) == 0 {
		v := tea.NewView(faintStyle.Render(" no idle panes · esc to dismiss"))
		v.AltScreen = true
		return v
	}
	body := m.height - 2
	if body < 3 {
		body = 3
	}
	lw := m.listWidth()
	pw := m.width - lw - 3
	if pw < 1 {
		pw = 1
	}

	header := titleStyle.Render(fmt.Sprintf(" idle panes %d", len(m.panes)))
	if n := len(m.marked); n > 0 {
		header += markStyle.Render(fmt.Sprintf("  %d marked", n))
	}
	if m.busy {
		header += faintStyle.Render("  closing…")
	}

	left := m.listLines(lw, body)
	right := m.previewLines(pw, body)
	sep := borderStyle.Render(" │ ")
	lines := make([]string, 0, body+2)
	lines = append(lines, header)
	for i := 0; i < body; i++ {
		lines = append(lines, padANSI(left[i], lw)+sep+right[i])
	}
	lines = append(lines, faintStyle.Render(" ↑↓ move · space mark · a mark all · enter close marked (or selected) · esc dismiss"))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// listLines renders each idle pane as two rows: id, location and seat, then
// the cwd and any close error.
func (m Model) listLines(w, h int) []string {
	rows := make([]string, 0, len(m.panes)*2)
	cursorRow := 0
	for i, p := range m.panes {
		sel := i == m.cursor
		if sel {
			cursorRow = len(rows)
		}
		sty := func(s lipgloss.Style) lipgloss.Style {
			if sel {
				return s.Background(selectedBg).Bold(true)
			}
			return s
		}
		mark := sty(lipgloss.NewStyle()).Render("  ")
		if m.marked[p.PaneID] {
			mark = sty(markStyle).Render("✕ ")
		}
		seat := ""
		if p.Seat != "" {
			seat = " was " + p.Seat
		}
		first := sty(lipgloss.NewStyle()).Render(" ") + mark + sty(idStyle).Render(p.PaneID) +
			sty(lipgloss.NewStyle()).Render(" "+p.Location) + sty(faintStyle).Render(seat)
		second := "    " + faintStyle.Render(truncLeft(p.Cwd, w-4))
		if err := m.errs[p.PaneID]; err != nil {
			second = "    " + errStyle.Render(ansi.Truncate(err.Error(), w-4, "…"))
		}
		rows = append(rows, fill(ansi.Truncate(first, w, "…"), w, sel), second)
	}
	offset := 0
	if cursorRow+2 > h {
		offset = cursorRow + 2 - h
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if j := offset + i; j < len(rows) {
			out[i] = rows[j]
		}
	}
	return out
}

// previewLines shows the bottom of the selected pane's screen, where its
// prompt and last output are, cut to the preview's width.
func (m Model) previewLines(w, h int) []string {
	out := make([]string, h)
	p, ok := m.current()
	if !ok {
		return out
	}
	text, ok := m.previews[p.PaneID]
	if !ok {
		out[0] = faintStyle.Render("reading " + p.PaneID + "…")
		return out
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for i, l := range lines {
		// Reset after each line so a colour left open never bleeds into the
		// next row's list column.
		out[i] = ansi.Truncate(l, w, "") + "\x1b[0m"
	}
	return out
}

func padANSI(s string, w int) string {
	if d := w - ansi.StringWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func fill(s string, w int, selected bool) string {
	d := w - ansi.StringWidth(s)
	if d <= 0 {
		return s
	}
	pad := strings.Repeat(" ", d)
	if selected {
		return s + lipgloss.NewStyle().Background(selectedBg).Render(pad)
	}
	return s + pad
}

func truncLeft(s string, w int) string {
	if w <= 1 || ansi.StringWidth(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}
