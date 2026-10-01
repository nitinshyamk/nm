package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// InputConfig describes a single-line text input, used for a value that is one
// short string — a task name — rather than the prose RunPrompt is for.
type InputConfig struct {
	Title       string
	Context     []string // lines shown above the field
	Placeholder string
	Initial     string

	// Validate rejects a value with a message shown under the field. It runs on
	// submit, so a name is checked before the flow moves on rather than after the
	// task directory fails to create. Nil accepts anything non-empty.
	Validate func(string) error
}

// InputResult is what the field returned.
type InputResult struct {
	Text      string
	Submitted bool
}

// RunInput opens the field inline, below the command that started it.
func RunInput(cfg InputConfig) (InputResult, error) {
	final, err := tea.NewProgram(newInput(cfg)).Run()
	if err != nil {
		return InputResult{}, err
	}
	model, ok := final.(input)
	if !ok {
		return InputResult{}, fmt.Errorf("unexpected model %T returned from the input", final)
	}
	return model.result, nil
}

type input struct {
	cfg    InputConfig
	field  textinput.Model
	result InputResult
	errMsg string
	width  int
	done   bool
}

func newInput(cfg InputConfig) input {
	field := textinput.New()
	field.Placeholder = cfg.Placeholder
	field.Prompt = "› "
	field.SetValue(cfg.Initial)
	field.Focus()
	return input{cfg: cfg, field: field, width: 80}
}

func (m input) Init() tea.Cmd { return textinput.Blink }

func (m input) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+g", "ctrl+c":
			m.done = true
			return m, tea.Quit
		case "enter":
			value := strings.TrimSpace(m.field.Value())
			if value == "" {
				m.errMsg = "a value is required"
				return m, nil
			}
			if m.cfg.Validate != nil {
				if err := m.cfg.Validate(value); err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
			}
			m.result = InputResult{Text: value, Submitted: true}
			m.done = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	before := m.field.Value()
	m.field, cmd = m.field.Update(msg)
	if m.field.Value() != before {
		m.errMsg = ""
	}
	return m, cmd
}

func (m input) View() string {
	if m.done {
		return ""
	}

	var b strings.Builder
	b.WriteString(styleTitle.Render(m.cfg.Title) + "\n")
	for _, line := range m.cfg.Context {
		b.WriteString(styleMuted.Render(line) + "\n")
	}
	b.WriteString("\n" + m.field.View() + "\n")
	if m.errMsg != "" {
		b.WriteString(styleWarnText.Render(m.errMsg) + "\n")
	}
	b.WriteString(styleHelp.Render("enter accept · esc cancel"))
	return b.String()
}
