// Package cli wires up the nm command tree.
package cli

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version can be overridden at build time with -ldflags "-X ...cli.version=...".
// Nothing does today: buildVersion reads what Go already stamps into every
// binary, which keeps the build task free of a shell (the ldflags value used to
// come from a $(git describe) that only a POSIX shell could expand). The hook
// stays so a release build can pass a tag name, which Go does not stamp.
var version = ""

// buildVersion reports the version nm shows. Go records the revision for a
// build made inside a checkout and the module version for one installed with
// `go install pkg@version`, so both paths report something real rather than
// falling back to "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision != "" {
		if len(revision) > 7 {
			revision = revision[:7]
		}
		if modified == "true" {
			return revision + "-dirty"
		}
		return revision
	}

	// "(devel)" is what a module reports when it was not built from a released
	// version, which says less than "dev" already does.
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

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
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddGroup(
		&cobra.Group{ID: groupCommon, Title: "Common commands:"},
		&cobra.Group{ID: groupConfig, Title: "Configuration:"},
	)
	cmd.AddCommand(newWorktreeCmd(), newTaskCmd(), newShellCmd(), newConfigCmd(), newSelfCmd())

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
