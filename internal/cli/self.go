package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
			dest, note, err := installBinary(from, cfg.Install())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "installed %s\n", dest)
			if note != "" {
				fmt.Fprintf(out, "note: %s\n", note)
			}
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

// installBinary copies src into dir and returns the path it wrote, plus a note
// worth showing the user when one applies. An empty src means the binary the
// build task produces. The copy lands on a temporary name first and is moved into
// place, so a failure partway through cannot leave a truncated binary where a
// working one used to be.
//
// The old binary is rotated aside rather than overwritten. Windows refuses to
// replace a running executable -- `mise run install` while any nm is still
// running failed with "Access is denied" -- but it does allow renaming one,
// because the open handle follows the file rather than the name. So the swap is
// rename-then-rename, and the rotated file is deleted afterwards if the process
// holding it has exited.
func installBinary(src, dir string) (dest, note string, err error) {
	if src == "" {
		src = builtBinary()
	}
	if _, err := os.Stat(src); err != nil {
		return "", "", fmt.Errorf("no binary to install at %s (run `mise run build` first): %w", src, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", dir, err)
	}
	dest = filepath.Join(dir, filepath.Base(src))

	staged := dest + ".new"
	if err := copyFile(src, staged); err != nil {
		return "", "", err
	}

	rotated := ""
	if _, err := os.Stat(dest); err == nil {
		rotated, err = rotateAside(dest)
		if err != nil {
			_ = os.Remove(staged)
			return "", "", err
		}
	}

	if err := os.Rename(staged, dest); err != nil {
		// Put the old binary back rather than leaving nothing installed.
		if rotated != "" {
			_ = os.Rename(rotated, dest)
		}
		_ = os.Remove(staged)
		return "", "", fmt.Errorf("moving %s into place: %w", dest, err)
	}

	if rotated != "" {
		_ = os.Remove(rotated)
	}
	// Report whatever is actually still on disk, not just this run's rotation: a
	// copy held by a long-running nm survives several installs, and an
	// unexplained nm.exe.old sitting next to the binary invites a guess.
	if leftovers := remainingRotations(dest); len(leftovers) > 0 {
		note = fmt.Sprintf("a previous binary is still in use and left at %s; "+
			"nm deletes it on a later install once that process has exited",
			strings.Join(leftovers, ", "))
	}
	return dest, note, nil
}

// maxRotations bounds how many held copies of a binary nm will tolerate at once.
// More than this means something is wrong that another rename will not fix.
const maxRotations = 20

// rotateAside moves dest out of the way and reports the name it now has.
//
// The name has to be unique rather than a fixed ".old": installing twice while
// the same nm is still running leaves the first rotation held by that process,
// and Windows will neither delete it nor let a second rename replace it, so a
// fixed name makes every install after the first one fail.
func rotateAside(dest string) (string, error) {
	sweepRotations(dest)

	for _, candidate := range rotationNames(dest) {
		// Anything still here survived the sweep, so it is held and cannot be
		// replaced. Try the next name rather than failing.
		if _, err := os.Stat(candidate); err == nil {
			continue
		}
		if err := os.Rename(dest, candidate); err != nil {
			return "", fmt.Errorf("moving the previous %s aside: %w", dest, err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%s has %d previous copies still in use; exit any running nm and try again",
		dest, maxRotations)
}

// sweepRotations deletes rotations left by earlier installs. Each is best effort:
// one still held by a running process stays until that process exits, and a later
// install clears it.
func sweepRotations(dest string) {
	for _, path := range rotationNames(dest) {
		_ = os.Remove(path)
	}
}

// remainingRotations lists the rotations still on disk, by base name.
func remainingRotations(dest string) []string {
	var out []string
	for _, path := range rotationNames(dest) {
		if _, err := os.Stat(path); err == nil {
			out = append(out, filepath.Base(path))
		}
	}
	return out
}

// rotationNames is every name a rotated binary can occupy, in the order
// rotateAside tries them.
func rotationNames(dest string) []string {
	out := make([]string, 0, maxRotations)
	for i := range maxRotations {
		if i == 0 {
			out = append(out, dest+".old")
			continue
		}
		out = append(out, fmt.Sprintf("%s.old.%d", dest, i))
	}
	return out
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
