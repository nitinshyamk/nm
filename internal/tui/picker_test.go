package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pressKey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// press feeds keys to the model and reports whether it asked to quit.
func press(t *testing.T, m picker, keys ...string) (picker, bool) {
	t.Helper()
	quit := false
	for _, k := range keys {
		next, cmd := m.Update(pressKey(k))
		updated, ok := next.(picker)
		if !ok {
			t.Fatalf("Update returned %T", next)
		}
		m = updated
		if cmd != nil {
			if msg := cmd(); msg != nil {
				if _, isQuit := msg.(tea.QuitMsg); isQuit {
					quit = true
				}
			}
		}
	}
	return m, quit
}

func demo() picker {
	return newPicker(Config{
		Title: "worktrees",
		Rows: []Row{
			{ID: "a", Title: "alpha-111111", Subtitle: "/w/nm-id-alpha-111111"},
			{ID: "b", Title: "beta-222222", Subtitle: "/w/site-id-beta-222222", Hazards: []string{"2 files modified"}},
			{ID: "c", Title: "gamma-333333", Subtitle: "/w/nm-id-gamma-333333"},
		},
		Actions: []Action{
			{Key: "enter", Name: "select", Help: "enter"},
			{Key: "d", Name: "delete", Help: "delete", Confirm: true, Verb: "Delete"},
		},
		Empty: "no worktrees",
	})
}

func TestEnterSelectsTheHighlightedRow(t *testing.T) {
	m, quit := press(t, demo(), "down", "enter")
	if !quit {
		t.Fatal("enter did not finish the picker")
	}
	if m.outcome.Action != "select" || m.outcome.Row.ID != "b" {
		t.Errorf("outcome = %+v, want select of row b", m.outcome)
	}
}

func TestNavigationStaysInBounds(t *testing.T) {
	m, _ := press(t, demo(), "up", "up", "up")
	if m.cursor != 0 {
		t.Errorf("cursor = %d after pressing up at the top, want 0", m.cursor)
	}
	m, _ = press(t, m, "down", "down", "down", "down", "down")
	if m.cursor != 2 {
		t.Errorf("cursor = %d after running off the bottom, want 2", m.cursor)
	}
}

func TestDestructiveActionRequiresDeliberateConfirmation(t *testing.T) {
	// Highlight the dirty row and ask to delete it.
	m, quit := press(t, demo(), "down", "d")
	if quit {
		t.Fatal("d quit immediately instead of asking for confirmation")
	}
	if m.confirmRow == nil {
		t.Fatal("no confirmation dialog appeared")
	}
	if m.confirmChoice != 0 {
		t.Error("the dialog does not open with Cancel focused")
	}

	view := m.View()
	if !strings.Contains(view, "2 files modified") {
		t.Errorf("the dialog does not list what would be lost:\n%s", view)
	}
	if !strings.Contains(view, "anyway") {
		t.Errorf("the confirm button should read 'Delete anyway' for dirty rows:\n%s", view)
	}

	// Enter on the default choice cancels and returns to the list.
	cancelled, quit := press(t, m, "enter")
	if quit || cancelled.outcome.Action != "" {
		t.Error("pressing enter on the default choice deleted something")
	}
	if cancelled.confirmRow != nil {
		t.Error("cancelling left the dialog open")
	}

	// Only moving to the other button and confirming goes through.
	confirmed, quit := press(t, m, "right", "enter")
	if !quit {
		t.Fatal("confirming did not finish the picker")
	}
	if confirmed.outcome.Action != "delete" || confirmed.outcome.Row.ID != "b" {
		t.Errorf("outcome = %+v, want delete of row b", confirmed.outcome)
	}
}

func TestConfirmationEscapes(t *testing.T) {
	m, _ := press(t, demo(), "d", "esc")
	if m.confirmRow != nil {
		t.Error("esc did not dismiss the dialog")
	}
	if m.outcome.Action != "" {
		t.Error("esc produced an outcome")
	}
}

func TestSingleKeystrokeCannotDelete(t *testing.T) {
	// Every key that is not an explicit two-step confirmation must leave the
	// dialog open and produce nothing.
	for _, k := range []string{"d", "y", "Y", "delete", "x", "enter"} {
		m, quit := press(t, demo(), "d", k)
		if quit && m.outcome.Action == "delete" {
			t.Errorf("pressing %q alone deleted a worktree", k)
		}
	}
}

func TestFilterNarrowsRows(t *testing.T) {
	m, _ := press(t, demo(), "/", "g", "a", "m")
	if len(m.rows) != 1 || m.rows[0].ID != "c" {
		t.Fatalf("filter kept %d rows, want just gamma", len(m.rows))
	}
	m, quit := press(t, m, "enter", "enter")
	if !quit || m.outcome.Row.ID != "c" {
		t.Errorf("outcome = %+v, want the filtered row", m.outcome)
	}

	// Backspacing restores the other rows.
	m, _ = press(t, demo(), "/", "g", "a", "m", "backspace", "backspace", "backspace")
	if len(m.rows) != 3 {
		t.Errorf("clearing the filter left %d rows, want 3", len(m.rows))
	}
}

func TestFilterEscapeClears(t *testing.T) {
	m, _ := press(t, demo(), "/", "z", "z", "esc")
	if m.filter != "" || len(m.rows) != 3 {
		t.Errorf("esc did not clear the filter: filter=%q rows=%d", m.filter, len(m.rows))
	}
}

func TestQuitProducesNoOutcome(t *testing.T) {
	m, quit := press(t, demo(), "q")
	if !quit {
		t.Fatal("q did not quit")
	}
	if m.outcome.Action != "" {
		t.Errorf("quitting produced outcome %+v", m.outcome)
	}
}

func TestEmptyListIgnoresActions(t *testing.T) {
	m := newPicker(Config{
		Title:   "worktrees",
		Rows:    nil,
		Actions: []Action{{Key: "enter", Name: "select", Help: "enter"}},
		Empty:   "no worktrees yet",
	})
	after, quit := press(t, m, "enter", "d")
	if quit || after.outcome.Action != "" {
		t.Error("an empty picker produced an outcome")
	}
	if !strings.Contains(m.View(), "no worktrees yet") {
		t.Error("the empty message is not shown")
	}
}

func TestGroupHeadingsRenderOnce(t *testing.T) {
	m := newPicker(Config{
		Title: "tasks",
		Rows: []Row{
			{ID: "1", Group: "Needs input", Title: "one"},
			{ID: "2", Group: "Needs input", Title: "two"},
			{ID: "3", Group: "Working", Title: "three"},
		},
		Actions: []Action{{Key: "enter", Name: "select", Help: "enter"}},
	})
	m.width, m.height = 100, 40
	view := m.View()
	if n := strings.Count(view, "Needs input"); n != 1 {
		t.Errorf("the group heading appears %d times, want 1:\n%s", n, view)
	}
	if !strings.Contains(view, "Working") {
		t.Errorf("the second group heading is missing:\n%s", view)
	}
}

func TestTruncateKeepsTheTailAndRespectsRunes(t *testing.T) {
	if got := truncate("/home/u/projects/worktrees/nm-id-x-123456", 20); !strings.HasSuffix(got, "nm-id-x-123456") {
		t.Errorf("truncate dropped the informative end: %q", got)
	}
	if got := truncate("short", 20); got != "short" {
		t.Errorf("truncate shortened a string that fits: %q", got)
	}
	// Multi-byte characters must not be sliced in half.
	got := truncate("ααααβββββ", 5)
	if len([]rune(got)) != 5 {
		t.Errorf("truncate returned %d runes, want 5: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "ββββ") {
		t.Errorf("truncate mangled multi-byte text: %q", got)
	}
}

func TestLongListScrollsToKeepTheCursorVisible(t *testing.T) {
	rows := make([]Row, 50)
	for i := range rows {
		rows[i] = Row{ID: fmt.Sprint(i), Title: fmt.Sprintf("row-%02d", i)}
	}
	m := newPicker(Config{Title: "many", Rows: rows, Actions: []Action{{Key: "enter", Name: "select"}}})
	m.width, m.height = 80, 20

	if !strings.Contains(m.View(), "row-00") {
		t.Error("the first row is not visible at the top of the list")
	}

	m, _ = press(t, m, "end")
	view := m.View()
	if !strings.Contains(view, "row-49") {
		t.Errorf("the cursor row is not visible after jumping to the end:\n%s", view)
	}
	if strings.Contains(view, "row-00") {
		t.Error("the list did not scroll: the first row is still shown at the end")
	}
	if !strings.Contains(view, "more") {
		t.Error("the list does not say how many rows are scrolled off")
	}
}
