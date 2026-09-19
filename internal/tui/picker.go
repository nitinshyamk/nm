package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Row is one selectable entry in a picker.
type Row struct {
	ID       string
	Group    string   // optional heading the row is listed under
	Title    string   // primary text, e.g. a branch or task name
	Subtitle string   // secondary text, e.g. a path
	Badges   []Badge  // status chips drawn after the title
	Hazards  []string // what a destructive action would destroy
	Note     string   // extra line shown in a confirmation dialog
	Data     any      // caller payload, returned untouched
}

// Action is a key the picker responds to.
type Action struct {
	Key     string // key binding, e.g. "d"; the first action also gets enter
	Name    string // identifier returned in Outcome.Action
	Help    string // short description for the help line
	Confirm bool   // ask before returning
	Verb    string // button label in the confirmation dialog, e.g. "Delete"
}

// DefaultMaxRows is how many entries the picker shows at once when the
// caller does not say. The picker draws inline, below the command that
// started it, so it stays a small pane rather than taking the screen.
const DefaultMaxRows = 8

// Config describes a picker.
type Config struct {
	Title   string
	Rows    []Row
	Actions []Action
	Empty   string
	MaxRows int // entries visible at once; DefaultMaxRows when zero
}

// Outcome is what the user chose. Action is empty when they quit.
type Outcome struct {
	Action string
	Row    Row
}

// Run displays the picker and blocks until the user chooses or quits.
func Run(cfg Config) (Outcome, error) {
	m := newPicker(cfg)
	// No alt screen: the picker renders in place under the prompt and leaves
	// the scrollback intact.
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return Outcome{}, err
	}
	result, ok := final.(picker)
	if !ok {
		return Outcome{}, fmt.Errorf("unexpected model %T returned from the picker", final)
	}
	return result.outcome, nil
}

type picker struct {
	cfg     Config
	rows    []Row // filtered view of cfg.Rows
	cursor  int
	width   int
	height  int
	outcome Outcome

	filtering bool
	filter    string

	confirmRow    *Row
	confirmAction Action
	confirmChoice int // 0 = cancel, 1 = proceed

	done bool // set on the way out, so the pane erases itself
}

func newPicker(cfg Config) picker {
	return picker{cfg: cfg, rows: cfg.Rows, width: 80, height: 24}
}

func (m picker) Init() tea.Cmd { return nil }

func (m picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch {
		case m.confirmRow != nil:
			return m.updateConfirm(msg)
		case m.filtering:
			return m.updateFilter(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

func (m picker) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.done = true
		return m, tea.Quit
	case "up", "k", "ctrl+p":
		m.cursor = clamp(m.cursor-1, 0, len(m.rows)-1)
		return m, nil
	case "down", "j", "ctrl+n":
		m.cursor = clamp(m.cursor+1, 0, len(m.rows)-1)
		return m, nil
	case "home", "g":
		m.cursor = 0
		return m, nil
	case "end", "G":
		m.cursor = len(m.rows) - 1
		return m, nil
	case "pgup":
		m.cursor = clamp(m.cursor-m.pageSize(), 0, len(m.rows)-1)
		return m, nil
	case "pgdown":
		m.cursor = clamp(m.cursor+m.pageSize(), 0, len(m.rows)-1)
		return m, nil
	case "/":
		m.filtering = true
		return m, nil
	}

	if len(m.rows) == 0 {
		return m, nil
	}
	key := msg.String()
	for i, action := range m.cfg.Actions {
		if key == action.Key || (i == 0 && key == "enter") {
			row := m.rows[m.cursor]
			if action.Confirm {
				m.confirmRow, m.confirmAction, m.confirmChoice = &row, action, 0
				return m, nil
			}
			m.outcome = Outcome{Action: action.Name, Row: row}
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m picker) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.filtering, m.filter = false, ""
		m.applyFilter()
		return m, nil
	case "enter":
		m.filtering = false
		return m, nil
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
		}
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.filter += string(msg.Runes)
		m.applyFilter()
	}
	return m, nil
}

func (m picker) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.confirmRow = nil
		return m, nil
	case "left", "right", "tab", "shift+tab", "h", "l":
		// Two buttons, so any movement key toggles between them.
		m.confirmChoice = 1 - m.confirmChoice
		return m, nil
	case "enter":
		if m.confirmChoice == 0 {
			m.confirmRow = nil
			return m, nil
		}
		m.outcome = Outcome{Action: m.confirmAction.Name, Row: *m.confirmRow}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *picker) applyFilter() {
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	if needle == "" {
		m.rows = m.cfg.Rows
	} else {
		var kept []Row
		for _, row := range m.cfg.Rows {
			haystack := strings.ToLower(row.Title + " " + row.Subtitle + " " + row.Group)
			if strings.Contains(haystack, needle) {
				kept = append(kept, row)
			}
		}
		m.rows = kept
	}
	m.cursor = clamp(m.cursor, 0, len(m.rows)-1)
}

// pageSize is how many rows are drawn at once: the configured maximum, or
// fewer when the terminal itself is short.
func (m picker) pageSize() int {
	size := m.cfg.MaxRows
	if size <= 0 {
		size = DefaultMaxRows
	}
	// Two lines per row, leaving space for the title, help, and scroll hints.
	if fits := (m.height - 6) / 2; fits > 0 && fits < size {
		size = fits
	}
	if size < 1 {
		return 1
	}
	return size
}

func (m picker) View() string {
	// Once the choice is made the pane has served its purpose; erasing it
	// leaves the terminal showing the command and its output, nothing else.
	if m.done {
		return ""
	}

	var b strings.Builder

	title := m.cfg.Title
	if m.filtering || m.filter != "" {
		title += styleMuted.Render(fmt.Sprintf("   filter: %s_", m.filter))
	}
	b.WriteString(styleTitle.Render(title) + "\n")

	switch {
	case len(m.cfg.Rows) == 0:
		b.WriteString("\n" + styleMuted.Render(m.cfg.Empty) + "\n")
	case len(m.rows) == 0:
		b.WriteString("\n" + styleMuted.Render("nothing matches "+m.filter) + "\n")
	default:
		b.WriteString(m.renderRows())
	}

	if m.confirmRow != nil {
		b.WriteString(m.renderConfirm())
		return b.String()
	}
	b.WriteString(styleHelp.Render(m.helpLine()))
	return b.String()
}

func (m picker) renderRows() string {
	// The window follows the cursor: enough rows above it to fill the page.
	visible := m.pageSize()
	start := 0
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}
	end := min(start+visible, len(m.rows))

	var b strings.Builder
	lastGroup := ""
	if start > 0 {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		row := m.rows[i]
		if row.Group != "" && row.Group != lastGroup {
			b.WriteString(styleGroup.Render(row.Group) + "\n")
			lastGroup = row.Group
		}

		marker := "  "
		title := row.Title
		if i == m.cursor {
			marker = styleSelected.Render("▸ ")
			title = styleSelected.Render(title)
		}
		b.WriteString(marker + title + renderBadges(row.Badges) + "\n")
		if row.Subtitle != "" {
			b.WriteString("    " + styleMuted.Render(truncate(row.Subtitle, max(m.width-6, 20))) + "\n")
		}
	}
	if end < len(m.rows) {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  ↓ %d more", len(m.rows)-end)) + "\n")
	}
	return b.String()
}

func renderBadges(badges []Badge) string {
	if len(badges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(badges))
	for _, badge := range badges {
		parts = append(parts, badgeStyle(badge.Kind).Render(badge.Text))
	}
	return "  " + strings.Join(parts, " ")
}

func (m picker) renderConfirm() string {
	row := *m.confirmRow
	var b strings.Builder

	b.WriteString(styleTitle.Render(fmt.Sprintf("%s %s?", m.confirmAction.Verb, row.Title)) + "\n")
	if row.Subtitle != "" {
		b.WriteString(styleMuted.Render(row.Subtitle) + "\n")
	}

	verb := m.confirmAction.Verb
	if len(row.Hazards) > 0 {
		b.WriteString("\n" + styleDanger.Render("This will destroy work that exists nowhere else:") + "\n")
		for _, hazard := range row.Hazards {
			b.WriteString(styleWarnText.Render("  • "+hazard) + "\n")
		}
		verb += " anyway"
	}
	if row.Note != "" {
		b.WriteString("\n" + styleMuted.Render(row.Note) + "\n")
	}

	cancel, proceed := styleButton.Render("Cancel"), styleButton.Render(verb)
	if m.confirmChoice == 0 {
		cancel = styleFocus.Render("Cancel")
	} else {
		proceed = styleFocus.Render(verb)
	}
	b.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, cancel, "  ", proceed) + "\n")
	b.WriteString(styleHelp.Render("←/→ choose · enter confirm · esc cancel"))

	return "\n" + styleDialog.Render(b.String())
}

func (m picker) helpLine() string {
	if m.filtering {
		return "type to filter · enter accept · esc clear"
	}
	parts := make([]string, 0, len(m.cfg.Actions)+3)
	for i, action := range m.cfg.Actions {
		key := action.Key
		if i == 0 {
			key = "enter"
		}
		parts = append(parts, fmt.Sprintf("%s %s", key, action.Help))
	}
	parts = append(parts, "↑/↓ move", "/ filter", "q quit")
	return wrapParts(parts, m.width)
}

// truncate keeps the tail of a string, which is the informative end of a path.
func truncate(s string, width int) string {
	r := []rune(s)
	if width <= 1 || len(r) <= width {
		return s
	}
	return "…" + string(r[len(r)-width+1:])
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}
