package tui

import "github.com/charmbracelet/bubbles/key"

// The dashboard is driven by emacs bindings, with the vi-style keys the picker
// has always had kept as aliases. Nobody should have to relearn `j` to use the
// new view, and nobody should have to use it.
//
// Two bindings are worth explaining because they look like collisions and are
// not. `ctrl+v` pages down here while the prompt editor spends it on paste
// (prompt.go:95) — different models, never on screen together. `ctrl+p`/`ctrl+n`
// already mean up and down in the picker (picker.go:140,143), so the emacs half
// of this was load-bearing before it was a decision.
//
// Only the emacs key is given to key.WithHelp, so the legend reads as one scheme
// rather than a list of synonyms. The aliases still work, they are just not
// advertised.
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Unfold key.Binding
	Fold   key.Binding

	PageDown key.Binding
	PageUp   key.Binding
	Top      key.Binding
	Bottom   key.Binding

	ScrollDown key.Binding
	ScrollUp   key.Binding

	OtherPane key.Binding
	Find      key.Binding
	Refresh   key.Binding
	Tail      key.Binding
	Help      key.Binding

	Enter  key.Binding
	Open   key.Binding
	Agent  key.Binding
	Delete key.Binding

	Abort key.Binding
	Quit  key.Binding

	// Prefix keys. These are not actions: they open a two-key sequence, and what
	// follows decides. Held separately so the dispatcher can test for them before
	// trying to match anything else.
	PrefixCtrlX key.Binding
	PrefixCtrlC key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Up:     key.NewBinding(key.WithKeys("ctrl+p", "up", "k"), key.WithHelp("C-p", "up")),
		Down:   key.NewBinding(key.WithKeys("ctrl+n", "down", "j"), key.WithHelp("C-n", "down")),
		Unfold: key.NewBinding(key.WithKeys("ctrl+f", "right", "l"), key.WithHelp("C-f", "unfold")),
		Fold:   key.NewBinding(key.WithKeys("ctrl+b", "left", "h"), key.WithHelp("C-b", "fold")),

		PageDown: key.NewBinding(key.WithKeys("ctrl+v", "pgdown"), key.WithHelp("C-v", "page down")),
		PageUp:   key.NewBinding(key.WithKeys("alt+v", "pgup"), key.WithHelp("M-v", "page up")),
		Top:      key.NewBinding(key.WithKeys("alt+<", "home", "g"), key.WithHelp("M-<", "top")),
		Bottom:   key.NewBinding(key.WithKeys("alt+>", "end", "G"), key.WithHelp("M->", "bottom")),

		ScrollDown: key.NewBinding(key.WithKeys("alt+n"), key.WithHelp("M-n", "scroll preview")),
		ScrollUp:   key.NewBinding(key.WithKeys("alt+p"), key.WithHelp("M-p", "scroll back")),

		OtherPane: key.NewBinding(key.WithKeys("tab"), key.WithHelp("C-x o", "other pane")),
		Find:      key.NewBinding(key.WithKeys("ctrl+s", "/"), key.WithHelp("C-s", "find")),
		Refresh:   key.NewBinding(key.WithKeys("ctrl+l", "r"), key.WithHelp("C-l", "refresh")),
		Tail:      key.NewBinding(key.WithKeys("f"), key.WithHelp("C-c C-l", "agent log")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),

		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("RET", "cd here")),
		Open:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "editor + agent")),
		Agent:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agent")),
		Delete: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),

		Abort: key.NewBinding(key.WithKeys("ctrl+g", "esc"), key.WithHelp("C-g", "cancel")),
		Quit:  key.NewBinding(key.WithKeys("q"), key.WithHelp("C-x C-c", "quit")),

		PrefixCtrlX: key.NewBinding(key.WithKeys("ctrl+x")),
		PrefixCtrlC: key.NewBinding(key.WithKeys("ctrl+c")),
	}
}

// ShortHelp is the one-line legend: moving, folding, and the actions that change
// something. Implements help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.Down, k.Unfold, k.Find, k.Enter, k.Open, k.Agent, k.Help,
	}
}

// FullHelp is the `?` view, grouped by what the keys are for.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageDown, k.PageUp, k.Top, k.Bottom},
		{k.Unfold, k.Fold, k.OtherPane, k.ScrollDown, k.ScrollUp},
		{k.Enter, k.Open, k.Agent, k.Delete, k.Tail, k.Refresh},
		{k.Find, k.Abort, k.Quit, k.Help},
	}
}

// prefix is which two-key sequence is part-way entered.
type prefix int

// The pending-prefix states.
const (
	prefixNone prefix = iota
	prefixCtrlX
	prefixCtrlC
)

// label is what the echo area shows while a prefix is pending, the way emacs
// shows `C-x-` and waits.
func (p prefix) label() string {
	switch p {
	case prefixCtrlX:
		return "C-x-"
	case prefixCtrlC:
		return "C-c-"
	default:
		return ""
	}
}

// prefixAction is what a completed two-key sequence means.
type prefixAction int

// The sequences the dashboard understands. Anything else is dropped, and the
// prefix is cleared either way — a mistyped sequence should not leave the next
// keystroke being interpreted as part of it.
const (
	prefixNothing   prefixAction = iota
	prefixQuit                   // C-x C-c
	prefixOtherPane              // C-x o
	prefixTail                   // C-c C-l
)

// resolvePrefix interprets the second key of a sequence.
func resolvePrefix(p prefix, key string) prefixAction {
	switch p {
	case prefixCtrlX:
		switch key {
		case "ctrl+c":
			return prefixQuit
		case "o":
			return prefixOtherPane
		}
	case prefixCtrlC:
		if key == "ctrl+l" {
			return prefixTail
		}
	}
	return prefixNothing
}
