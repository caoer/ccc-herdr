package openfile

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// OpenMode says where the chosen file goes.
type OpenMode int

const (
	OpenNone  OpenMode = iota // dismissed
	OpenEdit                  // replace the picker popup with the editor
	OpenSplit                 // split beside the origin pane
	OpenTab                   // new tab in the origin workspace
)

var (
	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	countStyle  = lipgloss.NewStyle().Faint(true)
	helpStyle   = lipgloss.NewStyle().Faint(true)
	faintStyle  = lipgloss.NewStyle().Faint(true)
	baseStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	lineStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	selectedBg  = lipgloss.Color("237")
)

// Model is the file picker TUI: a query line over a fuzzy-filtered path list.
type Model struct {
	entries []Entry
	view    []int
	query   string
	cursor  int
	width   int
	height  int
	choice  *Entry
	mode    OpenMode
}

func New(entries []Entry) Model {
	return Model{
		entries: entries,
		view:    Filter(entries, ""),
		width:   100,
		height:  20,
	}
}

// Choice returns the selected entry (nil when dismissed) and the open mode.
func (m Model) Choice() (*Entry, OpenMode) { return m.choice, m.mode }

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) refilter() {
	m.view = Filter(m.entries, m.query)
	m.cursor = 0
}

func (m *Model) choose(mode OpenMode) {
	if len(m.view) == 0 {
		return
	}
	entry := m.entries[m.view[m.cursor]]
	m.choice = &entry
	m.mode = mode
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			m.choose(OpenEdit)
			return m, tea.Quit
		case "ctrl+s":
			m.choose(OpenSplit)
			return m, tea.Quit
		case "ctrl+t":
			m.choose(OpenTab)
			return m, tea.Quit
		case "up", "ctrl+p", "shift+tab":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "ctrl+n", "tab":
			if m.cursor < len(m.view)-1 {
				m.cursor++
			}
			return m, nil
		case "backspace":
			if m.query != "" {
				runes := []rune(m.query)
				m.query = string(runes[:len(runes)-1])
				m.refilter()
			}
			return m, nil
		case "ctrl+u":
			m.query = ""
			m.refilter()
			return m, nil
		case "ctrl+w":
			m.query = strings.TrimRight(m.query, " ")
			if i := strings.LastIndex(m.query, " "); i >= 0 {
				m.query = m.query[:i+1]
			} else {
				m.query = ""
			}
			m.refilter()
			return m, nil
		default:
			if msg.Text != "" && msg.Mod&^tea.ModShift == 0 {
				m.query += msg.Text
				m.refilter()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) View() tea.View {
	prompt := promptStyle.Render(" ❯ ") + m.query + "▏"
	count := countStyle.Render(fmt.Sprintf("%d/%d", len(m.view), len(m.entries)))
	gap := m.width - lipgloss.Width(prompt) - lipgloss.Width(count) - 1
	if gap < 1 {
		gap = 1
	}
	header := prompt + strings.Repeat(" ", gap) + count

	rows := m.height - 2
	if rows < 1 {
		rows = 1
	}
	offset := 0
	if m.cursor >= rows {
		offset = m.cursor - rows + 1
	}

	lines := make([]string, 0, rows+2)
	lines = append(lines, header)
	for i := offset; i < len(m.view) && i < offset+rows; i++ {
		lines = append(lines, m.row(m.entries[m.view[i]], i == m.cursor))
	}
	if len(m.view) == 0 {
		lines = append(lines, faintStyle.Render("   no file paths in pane output"))
	}
	lines = append(lines, helpStyle.Render(" ↑↓ move · enter edit · ctrl+s split · ctrl+t tab · esc dismiss"))

	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// row renders one entry line: basename[:line], then the home-abbreviated
// directory. Every segment's Render ends in a full SGR reset, so a selection
// background must ride on EACH segment (see jump.Model.row).
func (m Model) row(e Entry, selected bool) string {
	sty := func(s lipgloss.Style) lipgloss.Style {
		if selected {
			return s.Bold(true).Background(selectedBg)
		}
		return s
	}
	plain := sty(lipgloss.NewStyle())

	base := filepath.Base(e.Path)
	lineTag := ""
	if e.Line > 0 {
		lineTag = fmt.Sprintf(":%d", e.Line)
	}
	dir := filepath.Dir(e.Display)

	left := plain.Render(" ") + sty(baseStyle).Render(pad(base, 32)) +
		sty(lineStyle).Render(pad(lineTag, 6)) + plain.Render(" ")
	dirWidth := m.width - lipgloss.Width(left) - 1
	if dirWidth < 4 {
		dirWidth = 4
	}
	line := left + sty(faintStyle).Render(pad(truncate(dir, dirWidth), dirWidth)+" ")
	if fill := m.width - lipgloss.Width(line); fill > 0 {
		line += plain.Render(strings.Repeat(" ", fill))
	}
	return truncate(line, m.width)
}

func pad(s string, w int) string {
	s = truncate(s, w)
	if diff := w - lipgloss.Width(s); diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
