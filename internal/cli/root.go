// Package cli wires up the nm command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time with -ldflags "-X ...cli.version=...".
var version = "dev"

// Command groups, so the everyday commands are not listed alongside the
// ones you touch once when setting nm up.
const (
	groupCommon = "common"
	groupConfig = "configuration"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "nm",
		Short:         "Manage git worktrees and multi-repo tasks",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddGroup(
		&cobra.Group{ID: groupCommon, Title: "Common commands:"},
		&cobra.Group{ID: groupConfig, Title: "Configuration:"},
	)
	cmd.AddCommand(newWorktreeCmd(), newTaskCmd(), newShellCmd(), newConfigCmd())

	// cobra generates these two itself; file them under Configuration rather
	// than leaving them in an unlabelled group of their own.
	cmd.SetHelpCommandGroupID(groupConfig)
	cmd.SetCompletionCommandGroupID(groupConfig)
	return cmd
}

// Execute runs the nm command tree and exits non-zero on failure.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "nm:", err)
		os.Exit(1)
	}
}
