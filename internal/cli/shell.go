package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nitinshyamk/nm/internal/shellint"
	"github.com/spf13/cobra"
)

const (
	beginMarker = "# >>> nm shell integration >>>"
	endMarker   = "# <<< nm shell integration <<<"
)

func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "shell",
		GroupID: groupConfig,
		Short:   "Shell integration for changing directories",
		Long: "nm runs in its own process and cannot change your shell's directory.\n" +
			"The integration defines an nm function that performs the cd for it.",
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	cmd.AddCommand(newShellInitCmd(), newShellSetupCmd())
	return cmd
}

func newShellInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "init <" + strings.Join(shellint.Shells(), "|") + ">",
		Short:             "Print the shell integration script",
		Args:              cobra.ExactArgs(1),
		ValidArgs:         shellint.Shells(),
		ValidArgsFunction: completeShells,
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := shellint.InitScript(args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), script)
			return nil
		},
	}
}

func newShellSetupCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Add the shell integration to your shell's rc file",
		Long: "Appends a managed block to your rc file. Running it again refreshes\n" +
			"that block instead of adding a second one.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shell == "" {
				detected, err := shellint.Detect()
				if err != nil {
					return err
				}
				shell = detected
			}
			// Accept what the user typed in whatever form: --shell pwsh.exe and
			// --shell /bin/zsh both name a shell nm can write for.
			if name := shellint.NormalizeShell(shell); name != "" {
				shell = name
			}
			if _, err := shellint.InitScript(shell); err != nil {
				return err
			}
			rc, err := rcPath(shell)
			if err != nil {
				return err
			}
			replaced, err := writeManagedBlock(rc, shell)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if replaced {
				fmt.Fprintf(out, "refreshed the nm block in %s\n", rc)
			} else {
				fmt.Fprintf(out, "added the nm block to %s\n", rc)
			}
			fmt.Fprintf(out, "open a new shell, or run: source %s\n", rc)
			return nil
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "shell to configure (default: the shell nm was run from)")
	_ = cmd.RegisterFlagCompletionFunc("shell", completeShells)
	return cmd
}

func rcPath(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	switch shell {
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "zsh":
		if dir := os.Getenv("ZDOTDIR"); dir != "" {
			return filepath.Join(dir, ".zshrc"), nil
		}
		return filepath.Join(home, ".zshrc"), nil
	case "nu":
		return nuConfigPath(home)
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

// nuConfigPath is where nushell reads config.nu, which is not XDG on Windows:
// there it is %APPDATA%\nushell, and writing to ~/.config/nushell produced a
// file nushell never reads, so nu setup silently did nothing on Windows.
// $nu.default-config-dir is the authority; this mirrors it without shelling out
// to nu, which may not even be on PATH when nm runs.
func nuConfigPath(home string) (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "nushell", "config.nu"), nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "nushell", "config.nu"), nil
}

// sourceLine is what the managed block contains: calls back into nm, so the
// wrapper and the completions always match the installed binary.
func sourceLine(shell string) string {
	if shell == "nu" {
		// The nu script carries its own completer, because cobra generates no
		// nushell completion -- so unlike bash and zsh there is no second
		// `nm completion nu` to source.
		return "nm shell init nu | save --force ($nu.default-config-dir | path join nm.nu)\n" +
			"source ($nu.default-config-dir | path join nm.nu)"
	}
	return fmt.Sprintf("eval \"$(nm shell init %s)\"\nsource <(nm completion %s)", shell, shell)
}

// writeManagedBlock adds or refreshes the nm block in an rc file, reporting
// whether an existing block was replaced.
func writeManagedBlock(rc, shell string) (bool, error) {
	existing, err := os.ReadFile(rc)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", rc, err)
	}

	kept, replaced := stripManagedBlock(string(existing))
	if kept != "" && !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	updated := kept + "\n" + beginMarker + "\n" + sourceLine(shell) + "\n" + endMarker + "\n"

	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(rc), err)
	}
	if err := os.WriteFile(rc, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", rc, err)
	}
	return replaced, nil
}

// stripManagedBlock removes a previously written block, along with the blank
// line that precedes it, so repeated runs do not grow the file.
func stripManagedBlock(content string) (string, bool) {
	if !strings.Contains(content, beginMarker) {
		return content, false
	}
	var (
		out      []string
		skipping bool
		found    bool
	)
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.TrimSpace(line) == beginMarker:
			skipping, found = true, true
			// Drop a single blank separator line written with the block.
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
		case strings.TrimSpace(line) == endMarker:
			skipping = false
		case !skipping:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n"), found
}
