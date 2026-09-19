package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DefaultPromptRows is the editor's height when the caller does not say.
const DefaultPromptRows = 10

// PromptConfig describes the prompt editor.
type PromptConfig struct {
	Title       string
	Context     []string // lines of context shown above the editor
	Placeholder string
	Initial     string
	Rows        int // editor height; DefaultPromptRows when zero
}

// PromptResult is what the editor returned.
type PromptResult struct {
	Text      string
	Submitted bool
}

// RunPrompt opens the prompt editor, which uses emacs keybindings.
func RunPrompt(cfg PromptConfig) (PromptResult, error) {
	// Inline, like the picker: a pane under the command rather than a
	// takeover of the terminal.
	final, err := tea.NewProgram(newPrompt(cfg)).Run()
	if err != nil {
		return PromptResult{}, err
	}
	model, ok := final.(prompt)
	if !ok {
		return PromptResult{}, fmt.Errorf("unexpected model %T returned from the prompt editor", final)
	}
	return model.result, nil
}

type prompt struct {
	cfg    PromptConfig
	area   textarea.Model
	result PromptResult

	kill          string // the kill ring: what C-k, C-u, C-w, and M-d removed
	confirmCancel bool
	discard       int // 0 = keep editing, 1 = discard
	width, height int
	done          bool // set on the way out, so the pane erases itself
}

func newPrompt(cfg PromptConfig) prompt {
	area := textarea.New()
	area.Placeholder = cfg.Placeholder
	area.SetValue(cfg.Initial)
	area.CharLimit = 0
	area.ShowLineNumbers = false
	area.KeyMap = emacsKeyMap()
	area.Focus()

	m := prompt{cfg: cfg, area: area, width: 80, height: 24}
	m.resize()
	return m
}

// emacsKeyMap states every binding explicitly rather than inheriting the
// library's defaults, so an upstream change cannot quietly move a key.
func emacsKeyMap() textarea.KeyMap {
	b := func(keys ...string) key.Binding { return key.NewBinding(key.WithKeys(keys...)) }
	return textarea.KeyMap{
		CharacterForward:           b("right", "ctrl+f"),
		CharacterBackward:          b("left", "ctrl+b"),
		WordForward:                b("alt+f", "alt+right"),
		WordBackward:               b("alt+b", "alt+left"),
		LineNext:                   b("down", "ctrl+n"),
		LinePrevious:               b("up", "ctrl+p"),
		LineStart:                  b("home", "ctrl+a"),
		LineEnd:                    b("end", "ctrl+e"),
		InputBegin:                 b("alt+<", "ctrl+home"),
		InputEnd:                   b("alt+>", "ctrl+end"),
		DeleteAfterCursor:          b("ctrl+k"),
		DeleteBeforeCursor:         b("ctrl+u"),
		DeleteWordBackward:         b("ctrl+w", "alt+backspace"),
		DeleteWordForward:          b("alt+d", "alt+delete"),
		DeleteCharacterBackward:    b("backspace"),
		DeleteCharacterForward:     b("ctrl+d", "delete"),
		TransposeCharacterBackward: b("ctrl+t"),
		InsertNewline:              b("enter"),
		Paste:                      b("ctrl+v"),
		UppercaseWordForward:       b("alt+u"),
		LowercaseWordForward:       b("alt+l"),
		CapitalizeWordForward:      b("alt+c"),
	}
}

// killKeys remove text and therefore feed the kill ring, the way emacs
// distinguishes killing from plain deletion.
var killKeys = map[string]bool{
	"ctrl+k": true, "ctrl+u": true, "ctrl+w": true,
	"alt+backspace": true, "alt+d": true, "alt+delete": true,
}

func (m prompt) Init() tea.Cmd { return textarea.Blink }

func (m *prompt) resize() {
	rows := m.cfg.Rows
	if rows <= 0 {
		rows = DefaultPromptRows
	}
	// Shrink when the terminal cannot spare the room for the context lines,
	// title, and help.
	if fits := m.height - len(m.cfg.Context) - 8; fits > 0 && fits < rows {
		rows = fits
	}
	m.area.SetWidth(max(m.width-4, 20))
	m.area.SetHeight(max(rows, 3))
}

func (m prompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case tea.KeyMsg:
		pressed := msg.String()

		if m.confirmCancel {
			switch pressed {
			case "left", "right", "tab", "shift+tab":
				m.discard = 1 - m.discard
			case "enter":
				if m.discard == 1 {
					m.done = true
					return m, tea.Quit
				}
				m.confirmCancel = false
			case "esc", "ctrl+g":
				m.confirmCancel = false
			}
			return m, nil
		}

		switch pressed {
		case "ctrl+s", "alt+enter":
			m.result = PromptResult{Text: strings.TrimSpace(m.area.Value()), Submitted: true}
			m.done = true
			return m, tea.Quit
		case "esc", "ctrl+g", "ctrl+c":
			if strings.TrimSpace(m.area.Value()) == "" {
				m.done = true
				return m, tea.Quit
			}
			m.confirmCancel, m.discard = true, 0
			return m, nil
		case "ctrl+y":
			if m.kill != "" {
				m.area.InsertString(m.kill)
			}
			return m, nil
		}

		// Killing must record what it removed. The textarea does not report
		// that, so compare the text before and after it handles the key.
		before := m.area.Value()
		var cmd tea.Cmd
		m.area, cmd = m.area.Update(msg)
		if killKeys[pressed] {
			if removed := removedText(before, m.area.Value()); removed != "" {
				m.kill = removed
			}
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return m, cmd
}

// removedText returns the run of characters that disappeared between two
// versions of the buffer, or "" if nothing was removed.
func removedText(before, after string) string {
	if len(after) >= len(before) {
		return ""
	}
	b, a := []rune(before), []rune(after)

	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	return string(b[prefix : len(b)-suffix])
}

func (m prompt) View() string {
	// The editor has done its job; leave the terminal to the command output.
	if m.done {
		return ""
	}

	var b strings.Builder
	b.WriteString(styleTitle.Render(m.cfg.Title) + "\n")
	for _, line := range m.cfg.Context {
		b.WriteString(styleMuted.Render(line) + "\n")
	}
	b.WriteString("\n" + m.area.View() + "\n")

	if m.confirmCancel {
		keep, discard := styleButton.Render("Keep editing"), styleButton.Render("Discard")
		if m.discard == 0 {
			keep = styleFocus.Render("Keep editing")
		} else {
			discard = styleFocus.Render("Discard")
		}
		dialog := styleTitle.Render("Discard this prompt?") + "\n" +
			styleMuted.Render("The task keeps its worktrees; it just starts without an agent.") + "\n\n" +
			lipgloss.JoinHorizontal(lipgloss.Top, keep, "  ", discard)
		b.WriteString(styleDialog.Render(dialog) + "\n")
		return b.String()
	}

	count := len([]rune(strings.TrimSpace(m.area.Value())))
	status := fmt.Sprintf("%d characters", count)
	if m.kill != "" {
		status += " · ctrl+y yanks the last kill"
	}
	help := wrapParts([]string{
		"ctrl+s submit", "ctrl+g cancel", "enter newline",
		"ctrl+a/e line", "alt+b/f word", "ctrl+k/u/w kill", "ctrl+y yank",
	}, m.width-2)
	b.WriteString(styleHelp.Render(help + "\n" + status))
	return b.String()
}
