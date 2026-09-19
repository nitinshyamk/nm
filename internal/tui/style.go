// Package tui holds the interactive views: a keyboard-driven picker with
// confirmation dialogs, and a prompt editor.
package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// BadgeKind selects the color a badge is drawn in.
type BadgeKind int

// Badge kinds, in ascending order of "look at me".
const (
	BadgeNeutral BadgeKind = iota
	BadgeOK
	BadgeInfo
	BadgeWarn
	BadgeDanger
)

// Badge is a small labelled chip drawn beside a row title.
type Badge struct {
	Text string
	Kind BadgeKind
}

// Adaptive colors keep the views readable on light and dark terminals.
var (
	colorMuted  = lipgloss.AdaptiveColor{Light: "#6a6a6a", Dark: "#9a9a9a"}
	colorText   = lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#e6e6e6"}
	colorAccent = lipgloss.AdaptiveColor{Light: "#0b5cad", Dark: "#7cc5ff"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#116329", Dark: "#63d68a"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#8a5200", Dark: "#f0b84a"}
	colorDanger = lipgloss.AdaptiveColor{Light: "#a01b2b", Dark: "#ff8189"}

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	styleGroup    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent).MarginTop(1)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleHelp     = lipgloss.NewStyle().Foreground(colorMuted).MarginTop(1)
	styleDanger   = lipgloss.NewStyle().Foreground(colorDanger)
	styleWarnText = lipgloss.NewStyle().Foreground(colorWarn)

	styleDialog = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDanger).
			Padding(1, 2).
			MarginTop(1)

	styleButton = lipgloss.NewStyle().Padding(0, 2)
	styleFocus  = lipgloss.NewStyle().Padding(0, 2).Bold(true).Reverse(true)
)

func badgeStyle(kind BadgeKind) lipgloss.Style {
	switch kind {
	case BadgeOK:
		return lipgloss.NewStyle().Foreground(colorOK)
	case BadgeInfo:
		return lipgloss.NewStyle().Foreground(colorAccent)
	case BadgeWarn:
		return lipgloss.NewStyle().Foreground(colorWarn)
	case BadgeDanger:
		return lipgloss.NewStyle().Foreground(colorDanger)
	default:
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
}

// Interactive reports whether nm is attached to a terminal it can draw on.
// When it is not, callers print plain text instead of starting a view.
func Interactive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
