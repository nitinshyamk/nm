package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/spf13/cobra"
)

func newSelfCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "self",
		GroupID:           groupConfig,
		Short:             "Manage this nm installation",
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	cmd.AddCommand(newSelfInstallCmd())
	return cmd
}

func newSelfInstallCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Copy the built binary into install_dir from ~/.nm.json",
		Long: "Copies the binary `mise run build` produced into the install_dir from\n" +
			"~/.nm.json, creating that directory if it does not exist.\n\n" +
			"This is Go's job rather than the build task's because the task used to do\n" +
			"it with mkdir -p and install -m, neither of which exists on Windows.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			dest, err := installBinary(from, cfg.Install())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "installed %s\n", dest)
			fmt.Fprintf(out, "\nFor 'nm worktree' / 'nm task' to change your shell's directory, add the\n"+
				"shell integration to your rc file (or run: mise run setup-shell):\n\n"+
				"  eval \"$(nm shell init bash)\"\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "",
		"binary to install (default: the one `mise run build` writes to bin/)")
	return cmd
}

// builtBinary is where `go build -o bin/` leaves the binary. Go appends .exe on
// Windows, where a file without it is not executable.
func builtBinary() string {
	name := "nm"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join("bin", name)
}

// installBinary copies src into dir and returns the path it wrote. An empty src
// means the binary the build task produces. The copy lands on a temporary name
// first and is renamed into place, so a failure partway through cannot leave a
// truncated binary where a working one used to be.
func installBinary(src, dir string) (string, error) {
	if src == "" {
		src = builtBinary()
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("no binary to install at %s (run `mise run build` first): %w", src, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	dest := filepath.Join(dir, filepath.Base(src))

	staged := dest + ".new"
	if err := copyFile(src, staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, dest); err != nil {
		// Leaving the staged copy behind would be worse than the failure itself.
		_ = os.Remove(staged)
		return "", fmt.Errorf("moving %s into place: %w", dest, err)
	}
	return dest, nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	// Nothing was written to the source, so its close cannot lose data.
	defer func() { _ = in.Close() }()

	// 0o755 rather than copying the source mode: this is the executable bit
	// `install -m 0755` used to set, and it is what makes the result runnable.
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}
	return nil
}
