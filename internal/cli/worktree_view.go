package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/tui"
	"github.com/nitinshyamk/nm/internal/worktree"
	"github.com/spf13/cobra"
)

// runWorktreeList opens the worktree picker, or prints a plain listing when
// there is no terminal to draw on.
func runWorktreeList(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if !tui.Interactive() {
		return printWorktrees(out, cfg)
	}

	for {
		list, err := worktree.List(cfg)
		if err != nil {
			return err
		}

		rows := make([]tui.Row, 0, len(list))
		for _, w := range list {
			rows = append(rows, worktreeRow(w))
		}

		outcome, err := tui.Run(tui.Config{
			Title:   fmt.Sprintf("worktrees in %s", cfg.Worktrees()),
			Rows:    rows,
			MaxRows: cfg.ListRows,
			Empty:   "no worktrees yet — create one with: nm worktree new <repo> [name]",
			Actions: []tui.Action{
				{Key: "enter", Name: "select", Help: "cd here"},
				{Key: "d", Name: "delete", Help: "delete", Confirm: tui.ConfirmAlways, Verb: "Delete"},
			},
		})
		if err != nil {
			return err
		}

		switch outcome.Action {
		case "":
			return nil
		case "select":
			w, ok := outcome.Row.Data.(worktree.Worktree)
			if !ok {
				return fmt.Errorf("unexpected row payload %T", outcome.Row.Data)
			}
			return enterDir(out, w.Dir)
		case "delete":
			w, ok := outcome.Row.Data.(worktree.Worktree)
			if !ok {
				return fmt.Errorf("unexpected row payload %T", outcome.Row.Data)
			}
			note, err := worktree.Delete(w, w.Status.Dirty())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "removed %s\n", w.Dir)
			if note != "" {
				fmt.Fprintf(out, "%s\n", note)
			}
		}
	}
}

func worktreeRow(w worktree.Worktree) tui.Row {
	row := tui.Row{
		ID:       w.Dir,
		Title:    w.Label(),
		Subtitle: w.Dir,
		Hazards:  w.Status.Hazards(),
		Data:     w,
	}
	if w.Repo != "" {
		row.Badges = append(row.Badges, tui.Badge{Text: w.Repo, Kind: tui.BadgeInfo})
	}
	row.Badges = append(row.Badges, statusBadges(w.Status)...)
	if w.Err != nil {
		row.Badges = append(row.Badges, tui.Badge{Text: "status unavailable", Kind: tui.BadgeDanger})
	}
	if !w.Status.LastCommit.IsZero() {
		row.Badges = append(row.Badges, tui.Badge{Text: humanAge(time.Since(w.Status.LastCommit))})
	}
	return row
}

// statusBadges turns a git status into compact chips: what is uncommitted and
// what has never left this machine.
func statusBadges(s gitx.Status) []tui.Badge {
	var badges []tui.Badge
	if n := s.Staged; n > 0 {
		badges = append(badges, tui.Badge{Text: fmt.Sprintf("●%d", n), Kind: tui.BadgeWarn})
	}
	if n := s.Unstaged; n > 0 {
		badges = append(badges, tui.Badge{Text: fmt.Sprintf("✎%d", n), Kind: tui.BadgeWarn})
	}
	if n := s.Untracked; n > 0 {
		badges = append(badges, tui.Badge{Text: fmt.Sprintf("+%d", n), Kind: tui.BadgeWarn})
	}
	if n := s.Unpushed; n > 0 {
		badges = append(badges, tui.Badge{Text: fmt.Sprintf("↑%d", n), Kind: tui.BadgeDanger})
	}
	if len(badges) == 0 {
		badges = append(badges, tui.Badge{Text: "clean", Kind: tui.BadgeOK})
	}
	return badges
}

// humanAge renders a duration the way a person would say it.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func printWorktrees(out io.Writer, cfg config.Config) error {
	list, err := worktree.List(cfg)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(out, "no worktrees in %s\n", cfg.Worktrees())
		return nil
	}
	for _, w := range list {
		state := "clean"
		if h := w.Status.Hazards(); len(h) > 0 {
			state = strings.Join(h, ", ")
		}
		fmt.Fprintf(out, "%s\t%s\t%s\n", w.Label(), w.Dir, state)
	}
	return nil
}
