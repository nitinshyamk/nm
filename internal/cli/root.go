// Package cli wires up the nm command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time with -ldflags "-X ...cli.version=...".
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "nm",
		Short:         "Manage git worktrees and multi-repo tasks",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return cmd
}

// Execute runs the nm command tree and exits non-zero on failure.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "nm:", err)
		os.Exit(1)
	}
}
