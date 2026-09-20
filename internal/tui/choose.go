package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ChooseConfig is a one-question dialog: a title, what is at stake, and a row
// of buttons.
type ChooseConfig struct {
	Title    string
	Subtitle string
	Hazards  []string // drawn as a warning list, when there is something to lose
	Detail   []string // plain context lines
	Choices  []string // button labels, left to right
	Default  int      // which button starts focused
}

// Choose shows the dialog and returns the index of the chosen button, or -1
// when the user escapes.
func Choose(cfg ChooseConfig) (int, error) {
	if len(cfg.Choices) == 0 {
		return -1, fmt.Errorf("a dialog needs at least one choice")
	}
	final, err := tea.NewProgram(newChooser(cfg)).Run()
	if err != nil {
		return -1, err
	}
	model, ok := final.(chooser)
	if !ok {
		return -1, fmt.Errorf("unexpected model %T returned from the dialog", final)
	}
	return model.result, nil
}

type chooser struct {
	cfg    ChooseConfig
	cursor int
	result int
	width  int
	done   bool
}

func newChooser(cfg ChooseConfig) chooser {
	cursor := cfg.Default
	if cursor < 0 || cursor >= len(cfg.Choices) {
		cursor = 0
	}
	return chooser{cfg: cfg, cursor: cursor, result: -1, width: 80}
}

func (m chooser) Init() tea.Cmd { return nil }

func (m chooser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h", "shift+tab":
			m.cursor = clamp(m.cursor-1, 0, len(m.cfg.Choices)-1)
		case "right", "l", "tab":
			m.cursor = clamp(m.cursor+1, 0, len(m.cfg.Choices)-1)
		case "enter":
			m.result, m.done = m.cursor, true
			return m, tea.Quit
		case "esc", "q", "ctrl+c":
			m.result, m.done = -1, true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m chooser) View() string {
	if m.done {
		return ""
	}

	var b strings.Builder
	b.WriteString(styleTitle.Render(m.cfg.Title) + "\n")
	if m.cfg.Subtitle != "" {
		b.WriteString(styleMuted.Render(m.cfg.Subtitle) + "\n")
	}
	for _, line := range m.cfg.Detail {
		b.WriteString(styleMuted.Render(line) + "\n")
	}
	if len(m.cfg.Hazards) > 0 {
		b.WriteString("\n" + styleDanger.Render("This will destroy work that exists nowhere else:") + "\n")
		for _, hazard := range m.cfg.Hazards {
			b.WriteString(styleWarnText.Render("  • "+hazard) + "\n")
		}
	}

	buttons := make([]string, 0, len(m.cfg.Choices)*2)
	for i, label := range m.cfg.Choices {
		if i > 0 {
			buttons = append(buttons, "  ")
		}
		if i == m.cursor {
			buttons = append(buttons, styleFocus.Render(label))
		} else {
			buttons = append(buttons, styleButton.Render(label))
		}
	}
	b.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, buttons...) + "\n")
	b.WriteString(styleHelp.Render("←/→ choose · enter confirm · esc cancel"))

	return styleDialog.Render(b.String())
}
