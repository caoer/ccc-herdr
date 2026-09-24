package jump

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/caoer/ccc-herdr/internal/tui"
)

var (
	promptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	countStyle  = lipgloss.NewStyle().Faint(true)
	helpStyle   = lipgloss.NewStyle().Faint(true)
	selectedBg  = lipgloss.Color("237")
	idStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	// roleStyles match the herdr sidebar's $role_* token colors.
	roleStyles = map[string]lipgloss.Style{
		"worker":  lipgloss.NewStyle().Foreground(lipgloss.Color("#31824d")),
		"leader":  lipgloss.NewStyle().Foreground(lipgloss.Color("#4275b6")),
		"advisor": lipgloss.NewStyle().Foreground(lipgloss.Color("#8c63aa")).Bold(true),
	}
	otherRoleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	faintStyle     = lipgloss.NewStyle().Faint(true)
)

// Model is the jumper TUI: a query line over a fuzzy-filtered candidate list.
type Model struct {
	candidates []Candidate
	view       []int
	query      string
	tab        tab            // the active filter tab
	counts     [bandCount]int // per band, over the query's matches
	roleCounts map[string]int // per role, over the query's matches
	cursor     int
	width      int
	height     int
	choice     string
}

func New(candidates []Candidate) Model {
	m := Model{
		candidates: candidates,
		tab:        allTab,
		width:      100,
		height:     20,
	}
	m.refilter()
	return m
}

// Choice returns the selected pane id, or "" when dismissed.
func (m Model) Choice() string { return m.choice }

func (m Model) Init() tea.Cmd { return nil }

// tab is one filter on the tab row: all, one cache band, or one role.
type tab struct {
	band Band   // AnyBand unless a band tab
	role string // "" unless a role tab
}

var allTab = tab{band: AnyBand}

func (t tab) keeps(c Candidate) bool {
	switch {
	case t.role != "":
		return c.Role == t.role
	case t.band != AnyBand:
		return c.Band == t.band
	}
	return true
}

// tabs is the tab row: all, each non-empty band in the LED's order, then
// worker, leader, advisor — always present and last, so ← from all lands on
// advisor.
func (m Model) tabs() []tab {
	out := []tab{allTab}
	for b := Band(0); b < bandCount; b++ {
		if m.counts[b] > 0 || m.tab.band == b {
			out = append(out, tab{band: b})
		}
	}
	for _, r := range Roles {
		out = append(out, tab{band: AnyBand, role: r})
	}
	return out
}

func (m *Model) refilter() {
	matched := Filter(m.candidates, m.query, AnyBand)
	m.counts = [bandCount]int{}
	m.roleCounts = map[string]int{}
	m.view = m.view[:0:0]
	for _, i := range matched {
		c := m.candidates[i]
		m.counts[c.Band]++
		m.roleCounts[c.Role]++
		if m.tab.keeps(c) {
			m.view = append(m.view, i)
		}
	}
	m.cursor = 0
}

// cycleTab steps the tab row, wrapping at both ends.
func (m *Model) cycleTab(step int) {
	tabs := m.tabs()
	cur := 0
	for i, t := range tabs {
		if t == m.tab {
			cur = i
		}
	}
	m.tab = tabs[(cur+step+len(tabs))%len(tabs)]
	m.refilter()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.PasteMsg:
		if s := tui.SanitizePaste(msg.Content); s != "" {
			m.query += s
			m.refilter()
		}
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
		case "right":
			m.cycleTab(1)
			return m, nil
		case "left":
			m.cycleTab(-1)
			return m, nil
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

	rows := m.height - 3
	if rows < 1 {
		rows = 1
	}
	offset := 0
	if m.cursor >= rows {
		offset = m.cursor - rows + 1
	}

	lines := make([]string, 0, rows+2)
	lines = append(lines, header, m.bandBar())
	for i := offset; i < len(m.view) && i < offset+rows; i++ {
		lines = append(lines, m.row(m.candidates[m.view[i]], i == m.cursor))
	}
	if len(m.view) == 0 {
		lines = append(lines, faintStyle.Render("   no matching pane"))
	}
	lines = append(lines, helpStyle.Render(" ↑↓ move · ←→ tab · enter jump · esc dismiss"))

	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

// bandBar is the tab row: all, each non-empty cache band in the LED's
// colors, then the roles in the sidebar's colors; the active tab is reversed.
func (m Model) bandBar() string {
	total := 0
	for _, n := range m.counts {
		total += n
	}
	parts := make([]string, 0, 12)
	for _, t := range m.tabs() {
		label, s := fmt.Sprintf("all %d", total), lipgloss.NewStyle()
		switch {
		case t.role != "":
			label, s = fmt.Sprintf("%s %d", t.role, m.roleCounts[t.role]), roleStyles[t.role]
		case t.band != AnyBand:
			label, s = fmt.Sprintf("● %d %s", m.counts[t.band], t.band.Name()), t.band.Style()
		}
		if t == m.tab {
			s = s.Reverse(true).Bold(true)
		}
		parts = append(parts, s.Render(" "+label+" "))
	}
	return truncate(" "+strings.Join(parts, " "), m.width)
}

// row renders one candidate line: band dot, id, role, name, activity age,
// cache time left, working directory when the popup is wide enough, title,
// and a right-aligned workspace·tab location. Every segment's Render ends in
// a full SGR reset, so a selection background must ride on EACH segment —
// wrapping the finished line in one background style paints only up to the
// first styled token.
func (m Model) row(c Candidate, selected bool) string {
	sty := func(s lipgloss.Style) lipgloss.Style {
		if selected {
			return s.Bold(true).Background(selectedBg)
		}
		return s
	}
	plain := sty(lipgloss.NewStyle())

	bandStyle := sty(c.Band.Style())
	dot := "·"
	if c.IsAgent {
		dot = "●"
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

	left := plain.Render(" ") + bandStyle.Render(dot) + plain.Render(" ") +
		sty(idStyle).Render(pad(id, 9)) + plain.Render(" ") +
		sty(roleStyle(c.Role)).Render(pad(c.Role, 7)) + plain.Render(" ") +
		plain.Render(pad(name, 16)) + plain.Render(" ") +
		bandStyle.Render(pad(c.AgeText(), 6)) + plain.Render(" ") +
		bandStyle.Render(pad(c.TTLText(), 8)) + plain.Render(" ")
	locRendered := sty(faintStyle).Render(location) + plain.Render(" ")
	titleWidth := m.width - lipgloss.Width(left) - lipgloss.Width(locRendered) - 1
	// The path column only when the title keeps minTitle cells beside it;
	// a title that already is the path (no terminal title) needs no copy.
	path := ""
	if c.Title != "" && c.Dir != "" && titleWidth-pathWidth-1 >= minTitle {
		path = sty(faintStyle).Render(pad(truncateLeft(c.Dir, pathWidth), pathWidth)) + plain.Render(" ")
		titleWidth -= pathWidth + 1
	}
	if titleWidth < 4 {
		titleWidth = 4
	}
	line := left + path + plain.Render(pad(truncate(title, titleWidth), titleWidth)+" ") + locRendered
	// Fill to full width so the selection band spans the whole popup row.
	if fill := m.width - lipgloss.Width(line); fill > 0 {
		line += plain.Render(strings.Repeat(" ", fill))
	}
	return truncate(line, m.width)
}

// pathWidth is the path column; minTitle the title room it must leave.
const (
	pathWidth = 32
	minTitle  = 24
)

func roleStyle(role string) lipgloss.Style {
	if s, ok := roleStyles[role]; ok {
		return s
	}
	return otherRoleStyle
}

// truncateLeft keeps a path's tail — the directory names that identify it.
func truncateLeft(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[1:]
	}
	return "…" + string(runes)
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
