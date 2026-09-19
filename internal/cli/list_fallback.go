package cli

import (
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/worktree"
	"github.com/spf13/cobra"
)

// runWorktreeList prints the worktrees. The interactive picker replaces this
// when stdout is a terminal.
func runWorktreeList(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	list, err := worktree.List(cfg)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no worktrees in %s\n", cfg.Worktrees())
		return nil
	}
	for _, w := range list {
		hazards := ""
		if h := w.Status.Hazards(); len(h) > 0 {
			hazards = "  (" + strings.Join(h, ", ") + ")"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\n", w.Repo, w.Dir, hazards)
	}
	return nil
}
