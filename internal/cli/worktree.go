package cli

import (
	"errors"
	"fmt"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/worktree"
	"github.com/spf13/cobra"
)

func newWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"wt"},
		GroupID: groupCommon,
		Short:   "Create, enter, and delete git worktrees",
		Long: "With no arguments, opens a list of every worktree to select, enter,\n" +
			"or delete.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeList(cmd)
		},
	}
	cmd.AddCommand(newWorktreeNewCmd())
	return cmd
}

func newWorktreeNewCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "new <repo> [name]",
		Short: "Create a worktree for a repository",
		Long: "Creates <worktrees_root>/<repo>-id-<name>-<hash> on a new branch cut\n" +
			"from the remote's current default branch. Without a name, today's\n" +
			"date is used.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 1 {
				name = args[1]
			}

			w, err := worktree.Create(cfg, worktree.Options{
				Repo:    args[0],
				Name:    name,
				Offline: offline,
			})
			if err != nil {
				if errors.Is(err, gitx.ErrRemoteUnreachable) {
					return fmt.Errorf("%w\n\nnm branches from the remote's current default branch so you never\n"+
						"start from a stale commit. To branch from local refs instead, re-run\n"+
						"with --offline", err)
				}
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "created %s on branch %s\n", w.Dir, w.Branch)
			return enterDir(cmd.OutOrStdout(), w.Dir)
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "branch from local refs instead of fetching from origin")
	return cmd
}
