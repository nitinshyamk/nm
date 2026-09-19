package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func altKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

func ctrl(name string) tea.KeyMsg {
	switch name {
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}
	case "ctrl+a":
		return tea.KeyMsg{Type: tea.KeyCtrlA}
	case "ctrl+e":
		return tea.KeyMsg{Type: tea.KeyCtrlE}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}
	case "ctrl+y":
		return tea.KeyMsg{Type: tea.KeyCtrlY}
	case "ctrl+b":
		return tea.KeyMsg{Type: tea.KeyCtrlB}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	default:
		panic("unmapped key " + name)
	}
}

func send(t *testing.T, m prompt, msgs ...tea.Msg) (prompt, bool) {
	t.Helper()
	quit := false
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		updated, ok := next.(prompt)
		if !ok {
			t.Fatalf("Update returned %T", next)
		}
		m = updated
		// Commands are run in the background: tea.Quit returns instantly, while
		// the textarea's cursor-blink timer would otherwise sleep for half a
		// second on every keystroke.
		if cmd != nil {
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			select {
			case out := <-done:
				if _, isQuit := out.(tea.QuitMsg); isQuit {
					quit = true
				}
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return m, quit
}

func typeText(s string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return msgs
}

func editor() prompt {
	return newPrompt(PromptConfig{Title: "Prompt for auth-9c31a0", Placeholder: "what should the agent do?"})
}

func TestPromptSubmit(t *testing.T) {
	m, _ := send(t, editor(), typeText("refactor auth")...)
	m, quit := send(t, m, ctrl("ctrl+s"))
	if !quit {
		t.Fatal("ctrl+s did not submit")
	}
	if !m.result.Submitted || m.result.Text != "refactor auth" {
		t.Errorf("result = %+v, want the typed text submitted", m.result)
	}
}

func TestPromptCancelOnEmptyNeedsNoConfirmation(t *testing.T) {
	m, quit := send(t, editor(), ctrl("ctrl+g"))
	if !quit {
		t.Fatal("cancelling an empty prompt did not quit")
	}
	if m.result.Submitted {
		t.Error("cancelling reported a submission")
	}
}

func TestPromptCancelWithTextAsksFirst(t *testing.T) {
	m, _ := send(t, editor(), typeText("half a thought")...)
	m, quit := send(t, m, ctrl("ctrl+g"))
	if quit {
		t.Fatal("cancelling with text quit without asking")
	}
	if !m.confirmCancel {
		t.Fatal("no confirmation appeared")
	}
	if m.discard != 0 {
		t.Error("the confirmation does not default to keeping the text")
	}

	// Enter on the default returns to editing with the text intact.
	kept, quit := send(t, m, pressKey("enter"))
	if quit {
		t.Fatal("the default choice discarded the prompt")
	}
	if kept.confirmCancel {
		t.Error("the dialog stayed open")
	}
	if !strings.Contains(kept.area.Value(), "half a thought") {
		t.Error("the text was lost")
	}

	// Moving to Discard and confirming quits without submitting.
	discarded, quit := send(t, m, pressKey("right"), pressKey("enter"))
	if !quit {
		t.Fatal("discarding did not quit")
	}
	if discarded.result.Submitted {
		t.Error("discarding reported a submission")
	}
}

func TestEmacsMotionAndKillRing(t *testing.T) {
	m, _ := send(t, editor(), typeText("hello world")...)

	// C-a to the start, C-k kills the line, C-y puts it back.
	m, _ = send(t, m, ctrl("ctrl+a"), ctrl("ctrl+k"))
	if got := m.area.Value(); got != "" {
		t.Fatalf("ctrl+k from the line start left %q, want an empty line", got)
	}
	if m.kill != "hello world" {
		t.Fatalf("kill ring holds %q, want the killed line", m.kill)
	}

	m, _ = send(t, m, ctrl("ctrl+y"))
	if got := m.area.Value(); got != "hello world" {
		t.Errorf("ctrl+y restored %q, want the killed text", got)
	}
}

func TestKillWordBackwardFeedsTheRing(t *testing.T) {
	m, _ := send(t, editor(), typeText("alpha beta")...)
	m, _ = send(t, m, ctrl("ctrl+w"))
	if got := m.area.Value(); !strings.HasPrefix(got, "alpha") || strings.Contains(got, "beta") {
		t.Fatalf("ctrl+w left %q, want the last word removed", got)
	}
	if m.kill != "beta" {
		t.Errorf("kill ring holds %q, want beta", m.kill)
	}

	m, _ = send(t, m, ctrl("ctrl+y"))
	if got := m.area.Value(); got != "alpha beta" {
		t.Errorf("yanking restored %q, want alpha beta", got)
	}
}

func TestPlainDeleteDoesNotClobberTheKillRing(t *testing.T) {
	m, _ := send(t, editor(), typeText("keep this")...)
	m, _ = send(t, m, ctrl("ctrl+w")) // kill "this"
	if m.kill != "this" {
		t.Fatalf("kill ring holds %q, want this", m.kill)
	}
	// Backspace is deletion, not killing: emacs leaves the ring alone.
	m, _ = send(t, m, pressKey("backspace"), pressKey("backspace"))
	if m.kill != "this" {
		t.Errorf("backspace overwrote the kill ring with %q", m.kill)
	}
}

func TestWordMotionWithAlt(t *testing.T) {
	m, _ := send(t, editor(), typeText("one two three")...)
	// Two words back, then kill forward to the end of the line.
	m, _ = send(t, m, altKey('b'), altKey('b'), ctrl("ctrl+k"))
	if got := m.area.Value(); got != "one " {
		t.Errorf("after alt+b alt+b ctrl+k the buffer is %q, want %q", got, "one ")
	}
	if m.kill != "two three" {
		t.Errorf("kill ring holds %q, want two three", m.kill)
	}
}

func TestMultilineEntry(t *testing.T) {
	m, _ := send(t, editor(), typeText("first line")...)
	m, _ = send(t, m, pressKey("enter"))
	m, _ = send(t, m, typeText("second line")...)
	if got := m.area.Value(); got != "first line\nsecond line" {
		t.Errorf("buffer = %q, want two lines", got)
	}
	m, quit := send(t, m, ctrl("ctrl+s"))
	if !quit || m.result.Text != "first line\nsecond line" {
		t.Errorf("submitted %q", m.result.Text)
	}
}

func TestRemovedText(t *testing.T) {
	cases := []struct{ before, after, want string }{
		{"hello world", "hello ", "world"},
		{"hello world", "world", "hello "},
		{"hello world", "held world", "lo"},
		{"same", "same", ""},
		{"short", "much longer", ""},
		{"abc", "", "abc"},
		{"héllo wörld", "héllo ", "wörld"},
	}
	for _, tc := range cases {
		if got := removedText(tc.before, tc.after); got != tc.want {
			t.Errorf("removedText(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestPromptViewShowsContextAndHelp(t *testing.T) {
	m := newPrompt(PromptConfig{
		Title:   "Prompt for auth-9c31a0",
		Context: []string{"~/projects/tasks/auth-9c31a0", "repos: nm, site"},
	})
	view := m.View()
	for _, want := range []string{"auth-9c31a0", "repos: nm, site", "ctrl+s submit", "ctrl+y yank"} {
		if !strings.Contains(view, want) {
			t.Errorf("the editor does not show %q:\n%s", want, view)
		}
	}
}

func TestEditorErasesItselfOnTheWayOut(t *testing.T) {
	// Submitted.
	m, _ := send(t, editor(), typeText("do the thing")...)
	m, _ = send(t, m, ctrl("ctrl+s"))
	if got := m.View(); got != "" {
		t.Errorf("the editor still renders after submitting:\n%s", got)
	}

	// Cancelled while empty.
	m, _ = send(t, editor(), ctrl("ctrl+g"))
	if got := m.View(); got != "" {
		t.Errorf("the editor still renders after cancelling:\n%s", got)
	}

	// Discarded through the confirmation.
	m, _ = send(t, editor(), typeText("half")...)
	m, _ = send(t, m, ctrl("ctrl+g"), pressKey("right"), pressKey("enter"))
	if got := m.View(); got != "" {
		t.Errorf("the editor still renders after discarding:\n%s", got)
	}

	// Still editing: the pane stays.
	m, _ = send(t, editor(), typeText("still writing")...)
	if m.View() == "" {
		t.Error("the editor erased itself while still in use")
	}
}

func TestEditorHeightIsBounded(t *testing.T) {
	m := newPrompt(PromptConfig{Title: "p"})
	m.width, m.height = 100, 200
	m.resize()
	if got := m.area.Height(); got != DefaultPromptRows {
		t.Errorf("editor height = %d in a 200-line terminal, want %d", got, DefaultPromptRows)
	}

	m = newPrompt(PromptConfig{Title: "p", Rows: 4})
	m.width, m.height = 100, 200
	m.resize()
	if got := m.area.Height(); got != 4 {
		t.Errorf("editor height = %d, want the configured 4", got)
	}
}
