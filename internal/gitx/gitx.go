// Package gitx is the only place in nm that shells out to git.
package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// NetworkTimeout bounds operations that talk to a remote, so a stalled
// connection surfaces as an error instead of a hung terminal.
const NetworkTimeout = 45 * time.Second

// CmdError carries the stderr of a failed git invocation.
type CmdError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *CmdError) Unwrap() error { return e.Err }

// Run executes git in dir and returns its trimmed stdout.
func Run(dir string, args ...string) (string, error) {
	return RunContext(context.Background(), dir, args...)
}

// RunContext is Run with cancellation, used for anything touching a remote.
func RunContext(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		// Never block on an interactive credential or host-key prompt: a
		// clean failure is recoverable, a hung TUI is not.
		"GIT_TERMINAL_PROMPT=0",
	)
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &CmdError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	out, err := Run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Root returns the working tree root containing dir.
func Root(dir string) (string, error) {
	return Run(dir, "rev-parse", "--show-toplevel")
}

// MainWorktree returns the root of the repository a worktree belongs to.
func MainWorktree(dir string) (string, error) {
	common, err := Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	// The common dir is <repo>/.git for a normal checkout, or a bare repo path.
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return common, nil
}

// CurrentBranch returns the checked-out branch, or "" when detached.
func CurrentBranch(dir string) (string, error) {
	out, err := Run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", nil
	}
	return out, nil
}

// HasRemote reports whether the named remote is configured.
func HasRemote(dir, remote string) bool {
	_, err := Run(dir, "remote", "get-url", remote)
	return err == nil
}

// BranchExists reports whether a local branch of that name exists.
func BranchExists(dir, branch string) bool {
	_, err := Run(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
