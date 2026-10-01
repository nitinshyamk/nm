package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// MultiSelectConfig describes an autocompleting multi-select, used to pick the
// repositories a new task spans. It is a filter box over a fixed set of
// candidates, with a running list of what has been chosen so far.
type MultiSelectConfig struct {
	Title       string
	Context     []string // lines shown above the input
	Placeholder string
	Candidates  []string // everything that can be chosen, e.g. repository names
	MaxRows     int      // suggestions visible at once; DefaultMaxRows when zero

	// MinChoices is how many must be chosen before the list can be submitted.
	// Zero lets an empty selection through, which a task with a definition wants
	// and a task without one does not.
	MinChoices int
}

// MultiSelectResult is what the picker returned.
type MultiSelectResult struct {
	Chosen    []string
	Submitted bool
}

// RunMultiSelect opens the picker inline, below the command that started it,
// the way the single-select picker and the prompt editor do.
func RunMultiSelect(cfg MultiSelectConfig) (MultiSelectResult, error) {
	final, err := tea.NewProgram(newMultiSelect(cfg)).Run()
	if err != nil {
		return MultiSelectResult{}, err
	}
	model, ok := final.(multiSelect)
	if !ok {
		return MultiSelectResult{}, fmt.Errorf("unexpected model %T returned from the multi-select", final)
	}
	return model.result, nil
}

type multiSelect struct {
	cfg    MultiSelectConfig
	input  textinput.Model
	result MultiSelectResult

	chosen  []string        // in the order they were picked
	picked  map[string]bool // membership, for a fast "already in?"
	matches []string        // candidates matching the current filter, minus the chosen
	cursor  int             // index into matches
	width   int
	height  int
	status  string // a transient note, e.g. "already added"
	done    bool
}

func newMultiSelect(cfg MultiSelectConfig) multiSelect {
	in := textinput.New()
	in.Placeholder = cfg.Placeholder
	in.Prompt = "› "
	in.Focus()

	m := multiSelect{
		cfg:    cfg,
		input:  in,
		picked: make(map[string]bool),
		width:  80,
		height: 24,
	}
	m.recompute()
	return m
}

func (m multiSelect) Init() tea.Cmd { return textinput.Blink }

func (m multiSelect) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "ctrl+g":
			// Esc with nothing chosen and nothing typed is a cancel; otherwise it
			// clears the filter first, so a stray keystroke does not throw away a
			// half-made selection.
			if m.input.Value() == "" && len(m.chosen) == 0 {
				m.done = true
				return m, tea.Quit
			}
			if msg.String() == "ctrl+c" {
				m.done = true
				return m, tea.Quit
			}
			m.input.SetValue("")
			m.recompute()
			return m, nil

		case "ctrl+s", "alt+enter":
			return m.submit()

		case "up", "ctrl+p":
			m.cursor = clamp(m.cursor-1, 0, max(len(m.matches)-1, 0))
			return m, nil
		case "down", "ctrl+n":
			m.cursor = clamp(m.cursor+1, 0, max(len(m.matches)-1, 0))
			return m, nil

		case "enter", "tab":
			// Enter on a highlighted suggestion adds it. Enter on an empty match
			// list with something already chosen submits, so a fast path exists for
			// "that's all of them".
			if len(m.matches) > 0 {
				return m.add(m.matches[m.cursor])
			}
			if msg.String() == "enter" {
				return m.submit()
			}
			return m, nil

		case "ctrl+w", "backspace":
			// Backspace on an empty box removes the last chosen repo, so the list is
			// editable without reaching for the mouse. With text in the box it falls
			// through to the input, which deletes a character.
			if m.input.Value() == "" && len(m.chosen) > 0 {
				return m.removeLast()
			}
		}
	}

	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.status = ""
		m.recompute()
	}
	return m, cmd
}

// add chooses a candidate and clears the filter, so the next one is typed from
// scratch rather than against the leftover text of the last.
func (m multiSelect) add(name string) (tea.Model, tea.Cmd) {
	if m.picked[name] {
		m.status = name + " is already added"
		return m, nil
	}
	m.chosen = append(m.chosen, name)
	m.picked[name] = true
	m.input.SetValue("")
	m.status = ""
	m.recompute()
	return m, nil
}

func (m multiSelect) removeLast() (tea.Model, tea.Cmd) {
	last := m.chosen[len(m.chosen)-1]
	m.chosen = m.chosen[:len(m.chosen)-1]
	delete(m.picked, last)
	m.recompute()
	return m, nil
}

func (m multiSelect) submit() (tea.Model, tea.Cmd) {
	if len(m.chosen) < m.cfg.MinChoices {
		m.status = fmt.Sprintf("choose at least %d", m.cfg.MinChoices)
		return m, nil
	}
	m.result = MultiSelectResult{Chosen: append([]string(nil), m.chosen...), Submitted: true}
	m.done = true
	return m, tea.Quit
}

// recompute rebuilds the suggestion list from the filter and the candidates not
// yet chosen, keeping the cursor in range. A prefix match ranks ahead of a
// substring match, because the first thing someone types is the start of a name.
func (m *multiSelect) recompute() {
	needle := strings.ToLower(strings.TrimSpace(m.input.Value()))

	var prefix, contains []string
	for _, cand := range m.cfg.Candidates {
		if m.picked[cand] {
			continue
		}
		lower := strings.ToLower(cand)
		switch {
		case needle == "":
			prefix = append(prefix, cand)
		case strings.HasPrefix(lower, needle):
			prefix = append(prefix, cand)
		case strings.Contains(lower, needle):
			contains = append(contains, cand)
		}
	}
	sort.Strings(prefix)
	sort.Strings(contains)

	m.matches = append(prefix, contains...)
	m.cursor = clamp(m.cursor, 0, max(len(m.matches)-1, 0))
}

func (m multiSelect) View() string {
	if m.done {
		return ""
	}

	var b strings.Builder
	b.WriteString(styleTitle.Render(m.cfg.Title) + "\n")
	for _, line := range m.cfg.Context {
		b.WriteString(styleMuted.Render(line) + "\n")
	}

	// The chosen row is the running answer, drawn as chips so it reads as a set
	// rather than a sentence.
	if len(m.chosen) > 0 {
		chips := make([]string, 0, len(m.chosen))
		for _, name := range m.chosen {
			chips = append(chips, badgeStyle(BadgeOK).Render("✓ "+name))
		}
		b.WriteString("\n" + wrapParts(chips, max(m.width-2, 20)) + "\n")
	}

	b.WriteString("\n" + m.input.View() + "\n")

	switch {
	case len(m.matches) > 0:
		b.WriteString(m.renderMatches())
	case strings.TrimSpace(m.input.Value()) != "":
		b.WriteString(styleMuted.Render("  nothing matches "+strings.TrimSpace(m.input.Value())) + "\n")
	default:
		b.WriteString(styleMuted.Render("  everything is chosen") + "\n")
	}

	if m.status != "" {
		b.WriteString(styleWarnText.Render(m.status) + "\n")
	}

	help := wrapParts([]string{
		"enter add", "C-s submit", "⌫ remove last", "C-g cancel", "↑/↓ move",
	}, m.width-2)
	b.WriteString(styleHelp.Render(help))
	return b.String()
}

func (m multiSelect) renderMatches() string {
	visible := m.cfg.MaxRows
	if visible <= 0 {
		visible = DefaultMaxRows
	}
	if fits := m.height - len(m.cfg.Context) - 8; fits > 0 && fits < visible {
		visible = fits
	}
	if visible < 1 {
		visible = 1
	}

	start := 0
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}
	end := min(start+visible, len(m.matches))

	var b strings.Builder
	if start > 0 {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
	}
	for i := start; i < end; i++ {
		name := m.matches[i]
		if i == m.cursor {
			b.WriteString(styleSelected.Render("▸ "+name) + "\n")
		} else {
			b.WriteString("  " + name + "\n")
		}
	}
	if end < len(m.matches) {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  ↓ %d more", len(m.matches)-end)) + "\n")
	}
	return lipgloss.NewStyle().Render(b.String())
}
