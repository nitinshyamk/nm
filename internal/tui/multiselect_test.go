package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func msType(m multiSelect, s string) multiSelect {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(multiSelect)
	}
	return m
}

// msControl builds the control-key messages the component matches on by
// String(), which pressKey does not cover.
func msControl(key string) tea.KeyMsg {
	switch key {
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+w":
		return tea.KeyMsg{Type: tea.KeyCtrlW}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}
	default:
		return pressKey(key)
	}
}

func msKey(m multiSelect, key string) multiSelect {
	next, _ := m.Update(msControl(key))
	return next.(multiSelect)
}

func sampleMultiSelect() multiSelect {
	m := newMultiSelect(MultiSelectConfig{
		Title:      "repos",
		Candidates: []string{"api", "web", "worker", "shared"},
		MinChoices: 1,
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(multiSelect)
}

func TestMultiSelectFiltersAndAdds(t *testing.T) {
	m := sampleMultiSelect()

	// Typing narrows the suggestions to the ones that match.
	m = msType(m, "wo")
	if len(m.matches) != 1 || m.matches[0] != "worker" {
		t.Fatalf("filter 'wo' gave %v, want [worker]", m.matches)
	}

	// Enter adds the highlighted match and clears the box for the next.
	m = msKey(m, "enter")
	if len(m.chosen) != 1 || m.chosen[0] != "worker" {
		t.Fatalf("enter did not add worker: chosen=%v", m.chosen)
	}
	if m.input.Value() != "" {
		t.Errorf("the filter was not cleared after a pick: %q", m.input.Value())
	}

	// An already-chosen repo is no longer offered.
	m = msType(m, "worker")
	for _, match := range m.matches {
		if match == "worker" {
			t.Errorf("a chosen repo was offered again: %v", m.matches)
		}
	}
}

func TestMultiSelectSubmitRequiresMinimum(t *testing.T) {
	m := sampleMultiSelect()

	// Submitting with nothing chosen is refused and says so.
	m = msKey(m, "ctrl+s")
	if m.done {
		t.Fatal("submitted with no repos despite MinChoices=1")
	}
	if !strings.Contains(m.status, "at least 1") {
		t.Errorf("no hint about the minimum: %q", m.status)
	}

	// Choosing one and submitting returns it.
	m = msType(m, "api")
	m = msKey(m, "enter")
	m = msKey(m, "ctrl+s")
	if !m.done || !m.result.Submitted {
		t.Fatalf("submit after a choice did not finish: done=%v result=%+v", m.done, m.result)
	}
	if len(m.result.Chosen) != 1 || m.result.Chosen[0] != "api" {
		t.Errorf("submit returned %v, want [api]", m.result.Chosen)
	}
}

func TestMultiSelectBackspaceRemovesLastChosen(t *testing.T) {
	m := sampleMultiSelect()
	m = msType(m, "api")
	m = msKey(m, "enter")
	m = msType(m, "web")
	m = msKey(m, "enter")
	if len(m.chosen) != 2 {
		t.Fatalf("expected two chosen, got %v", m.chosen)
	}

	// Backspace on an empty box drops the most recent pick.
	m = msKey(m, "backspace")
	if len(m.chosen) != 1 || m.chosen[0] != "api" {
		t.Errorf("backspace did not remove the last chosen: %v", m.chosen)
	}
	// api is offered again now that web is the only pick.
	if m.picked["web"] {
		t.Error("web is still marked chosen after removal")
	}
}

func TestMultiSelectCancelsEmpty(t *testing.T) {
	m := sampleMultiSelect()
	m = msKey(m, "esc")
	if !m.done {
		t.Fatal("esc on an empty picker did not quit")
	}
	if m.result.Submitted {
		t.Error("an escaped picker reported a submission")
	}
}
