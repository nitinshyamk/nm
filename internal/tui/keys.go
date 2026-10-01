package tui

import "github.com/charmbracelet/bubbles/key"

// The dashboard moves with emacs navigation — the vi-style keys the picker has
// always had are kept as aliases, so nobody has to relearn `j` to use the new
// view and nobody has to use it. The chrome keys, though, are plain single
// strokes rather than emacs chords: quit is C-c, refresh is C-r, and the panes
// are reached with Tab. Nothing here is a two-key sequence.
//
// One binding looks like a collision and is not: `ctrl+v` pages down here while
// the prompt editor spends it on paste (prompt.go:95) — different models, never
// on screen together. `ctrl+p`/`ctrl+n` already mean up and down in the picker
// (picker.go:140,143), so the emacs half of this was load-bearing before it was
// a decision.
//
// Only one key per action is given to key.WithHelp, so the legend reads as one
// scheme rather than a list of synonyms. The aliases still work, they are just
// not advertised.
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
	Help      key.Binding

	Enter  key.Binding
	Open   key.Binding
	Agent  key.Binding
	Attach key.Binding
	New    key.Binding
	Delete key.Binding

	Abort key.Binding
	Quit  key.Binding
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

		// Two panes, so Tab and Shift-Tab both just toggle; both are bound so the
		// key someone reaches for works either way.
		OtherPane: key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("TAB", "other pane")),
		Find:      key.NewBinding(key.WithKeys("ctrl+s", "/"), key.WithHelp("C-s", "find")),
		Refresh:   key.NewBinding(key.WithKeys("ctrl+r", "r"), key.WithHelp("C-r", "refresh")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),

		Enter:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("RET", "cd here")),
		Open:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "editor + agent")),
		Agent:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agent")),
		Attach: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "interact")),
		New:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new task")),
		Delete: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),

		Abort: key.NewBinding(key.WithKeys("ctrl+g", "esc"), key.WithHelp("C-g", "cancel")),
		Quit:  key.NewBinding(key.WithKeys("ctrl+c", "q"), key.WithHelp("C-c", "quit")),
	}
}

// ShortHelp is the one-line legend: moving, folding, and the actions that change
// something. Implements help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	// Kept short enough to fit a 60-column pane without the help model having to
	// truncate: find is reachable with `/` and lives in the full legend, so the
	// one-liner spends its room on the two actions this view is for — making a
	// task and talking to its agent — plus the key to everything else.
	return []key.Binding{
		k.Down, k.Unfold, k.New, k.Attach, k.Help,
	}
}

// FullHelp is the `?` view, grouped by what the keys are for.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageDown, k.PageUp, k.Top, k.Bottom},
		{k.Unfold, k.Fold, k.OtherPane, k.ScrollDown, k.ScrollUp},
		{k.New, k.Enter, k.Open, k.Agent, k.Attach, k.Delete, k.Refresh},
		{k.Find, k.Abort, k.Quit, k.Help},
	}
}
