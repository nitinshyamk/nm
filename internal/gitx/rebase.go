package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrRebaseConflict means a rebase stopped partway and left conflicts in the
// working tree. The rebase is still in progress: continuing it is the caller's
// decision, not gitx's.
var ErrRebaseConflict = errors.New("the rebase stopped with conflicts")

// RemoteBranchExists asks the remote itself whether it has a branch. The
// remote is the authority here for the same reason it is for the default
// branch: a local refs/remotes/origin/<branch> can be stale or absent.
func RemoteBranchExists(dir, remote, branch string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()

	out, err := RunContext(ctx, dir, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrRemoteUnreachable, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// FetchBranch brings one branch down from a remote, updating its
// remote-tracking ref.
func FetchBranch(dir, remote, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()

	if _, err := RunContext(ctx, dir, "fetch", "--quiet", remote, branch); err != nil {
		return fmt.Errorf("%w: %w", ErrRemoteUnreachable, err)
	}
	return nil
}

// AddWorktreeTracking checks an existing branch out into a new worktree.
//
// Unlike AddWorktree, which cuts a fresh branch, this is for a branch that
// already exists somewhere: locally, or only on the remote, in which case a
// local branch tracking it is created.
func AddWorktreeTracking(repoDir, path, remote, branch string) error {
	if BranchExists(repoDir, branch) {
		_, err := Run(repoDir, "worktree", "add", path, branch)
		return err
	}
	_, err := Run(repoDir, "worktree", "add", "--track", "-b", branch, path, remote+"/"+branch)
	return err
}

// PullRebase replays the current branch on top of a branch fetched from a
// remote. A conflict comes back as ErrRebaseConflict with the underlying git
// failure wrapped, so callers can tell "needs a human" from "git broke".
func PullRebase(dir, remote, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()

	if _, err := RunContext(ctx, dir, "pull", "--rebase", remote, branch); err != nil {
		if RebaseInProgress(dir) {
			return fmt.Errorf("%w: %w", ErrRebaseConflict, err)
		}
		return err
	}
	return nil
}

// RebaseInProgress reports whether a rebase stopped partway and is waiting to
// be continued or aborted.
func RebaseInProgress(dir string) bool {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		path, err := Run(dir, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// ConflictedFiles lists the paths git left with conflict markers.
func ConflictedFiles(dir string) ([]string, error) {
	out, err := Run(dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if path := strings.TrimSpace(line); path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// Head returns the commit the working tree is on.
func Head(dir string) (string, error) {
	return Run(dir, "rev-parse", "HEAD")
}

// PushForceWithLease overwrites a branch on a remote, but only if the remote
// still points where nm last saw it. A rebase rewrites history, so a plain
// push cannot work and a plain --force would silently discard whatever
// someone else pushed in the meantime.
func PushForceWithLease(dir, remote, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()

	_, err := RunContext(ctx, dir, "push", "--force-with-lease", "--quiet", remote, branch)
	return err
}

// WorktreeForBranch reports where a branch is already checked out, if it is.
// git refuses to check the same branch out twice, so nm looks first and says
// which directory holds it rather than passing git's message on.
func WorktreeForBranch(repoDir, branch string) (string, bool) {
	list, err := ListWorktrees(repoDir)
	if err != nil {
		return "", false
	}
	for _, w := range list {
		if w.Branch == branch {
			return w.Path, true
		}
	}
	return "", false
}
