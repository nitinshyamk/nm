package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pressChooser(t *testing.T, m chooser, keys ...string) (chooser, bool) {
	t.Helper()
	quit := false
	for _, k := range keys {
		next, cmd := m.Update(pressKey(k))
		updated, ok := next.(chooser)
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

func dialog() chooser {
	return newChooser(ChooseConfig{
		Title:    "Delete auth-9c31a0?",
		Subtitle: "/tasks/auth-9c31a0",
		Hazards:  []string{"artifacts/ holds 3 files"},
		Choices:  []string{"Cancel", "Delete anyway"},
		Default:  0,
	})
}

func TestChooseStartsOnTheDefault(t *testing.T) {
	m, quit := pressChooser(t, dialog(), "enter")
	if !quit {
		t.Fatal("enter did not close the dialog")
	}
	if m.result != 0 {
		t.Errorf("result = %d, want the default choice", m.result)
	}
}

func TestChooseMovesAndSelects(t *testing.T) {
	m, quit := pressChooser(t, dialog(), "right", "enter")
	if !quit || m.result != 1 {
		t.Errorf("result = %d, want the second choice", m.result)
	}

	// Movement stops at the ends rather than wrapping onto the risky button.
	m, _ = pressChooser(t, dialog(), "left", "left", "left")
	if m.cursor != 0 {
		t.Errorf("cursor = %d after running off the left, want 0", m.cursor)
	}
	m, _ = pressChooser(t, dialog(), "right", "right", "right")
	if m.cursor != 1 {
		t.Errorf("cursor = %d after running off the right, want 1", m.cursor)
	}
}

func TestChooseEscapes(t *testing.T) {
	m, quit := pressChooser(t, dialog(), "esc")
	if !quit {
		t.Fatal("esc did not close the dialog")
	}
	if m.result != -1 {
		t.Errorf("result = %d, want -1 for an escape", m.result)
	}
}

func TestChooseShowsWhatIsAtStake(t *testing.T) {
	view := dialog().View()
	for _, want := range []string{"Delete auth-9c31a0?", "artifacts/ holds 3 files", "Cancel", "Delete anyway"} {
		if !strings.Contains(view, want) {
			t.Errorf("the dialog does not show %q:\n%s", want, view)
		}
	}
	if got := dialog().View(); got == "" {
		t.Error("the dialog rendered nothing")
	}
	m, _ := pressChooser(t, dialog(), "enter")
	if got := m.View(); got != "" {
		t.Errorf("the dialog still renders after answering:\n%s", got)
	}
}

func TestChooseWithThreeOptions(t *testing.T) {
	m := newChooser(ChooseConfig{
		Title:   "nm has uncommitted changes",
		Choices: []string{"Cancel", "Skip this repo", "Commit"},
		Default: 2,
	})
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want the configured default", m.cursor)
	}
	m, quit := pressChooser(t, m, "left", "enter")
	if !quit || m.result != 1 {
		t.Errorf("result = %d, want the middle choice", m.result)
	}
}
