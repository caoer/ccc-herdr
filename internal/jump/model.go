package jump

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

var (
	promptStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	countStyle    = lipgloss.NewStyle().Faint(true)
	helpStyle     = lipgloss.NewStyle().Faint(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("237"))
	idStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	roleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	faintStyle    = lipgloss.NewStyle().Faint(true)

	statusStyles = map[string]lipgloss.Style{
		"working": lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		"idle":    lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		"done":    lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
		"blocked": lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
	}
)

// Model is the jumper TUI: a query line over a fuzzy-filtered candidate list.
type Model struct {
	candidates []Candidate
	view       []int
	query      string
	cursor     int
	width      int
	height     int
	choice     string
}

func New(candidates []Candidate) Model {
	return Model{
		candidates: candidates,
		view:       Filter(candidates, ""),
		width:      100,
		height:     20,
	}
}

// Choice returns the selected pane id, or "" when dismissed.
func (m Model) Choice() string { return m.choice }

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) refilter() {
	m.view = Filter(m.candidates, m.query)
	m.cursor = 0
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
			if len(m.view) > 0 {
				m.choice = m.candidates[m.view[m.cursor]].PaneID
			}
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
	count := countStyle.Render(fmt.Sprintf("%d/%d", len(m.view), len(m.candidates)))
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
		lines = append(lines, m.row(m.candidates[m.view[i]], i == m.cursor))
	}
	if len(m.view) == 0 {
		lines = append(lines, faintStyle.Render("   no matching pane"))
	}
	lines = append(lines, helpStyle.Render(" ↑↓ move · enter jump · esc dismiss"))

	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// row renders one candidate line: status dot, id, role, name, title, and a
// right-aligned workspace·tab location.
func (m Model) row(c Candidate, selected bool) string {
	dot := "·"
	if style, ok := statusStyles[c.Status]; ok && c.IsAgent {
		dot = style.Render("●")
	}

	id := c.ID
	if id == "" {
		id = c.PaneID
	}
	name := c.Name
	if name == "" {
		name = c.Base()
	}
	title := c.Title
	if title == "" {
		title = c.Dir
	}

	location := c.Workspace
	if c.Tab != "" {
		location += "·" + c.Tab
	}

	left := fmt.Sprintf(" %s %s %s %s ",
		dot,
		idStyle.Render(pad(id, 9)),
		roleStyle.Render(pad(c.Role, 7)),
		pad(name, 16),
	)
	locRendered := faintStyle.Render(location) + " "
	titleWidth := m.width - lipgloss.Width(left) - lipgloss.Width(locRendered) - 1
	if titleWidth < 4 {
		titleWidth = 4
	}
	line := left + pad(truncate(title, titleWidth), titleWidth) + " " + locRendered

	if selected {
		return selectedStyle.Render(truncate(line, m.width))
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
