package gitx

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AddWorktree creates a worktree at path on a new branch cut from commit.
func AddWorktree(repoDir, path, branch, commit string) error {
	_, err := Run(repoDir, "worktree", "add", "-b", branch, path, commit)
	return explainLongPaths(err)
}

// explainLongPaths names the git setting behind a "Filename too long" checkout
// failure, which otherwise reads as a problem with the repository.
//
// A default Windows git install refuses paths over 260 characters, and a task
// directory is already several segments deep before the repository's own tree
// starts — so a repository with long nested paths fails to check out at all, in a
// way indistinguishable from a real error. The remedy is one setting, and it is
// worth naming rather than leaving to be rediscovered.
//
// It is checked on the error rather than at startup deliberately: `core.longpaths`
// only matters for repositories that actually have such paths, so a startup warning
// would fire for everyone and mean nothing to almost all of them.
func explainLongPaths(err error) error {
	if err == nil || !strings.Contains(err.Error(), "Filename too long") {
		return err
	}
	return fmt.Errorf("%w\n\nThis is the 260-character path limit a default Windows git install "+
		"enforces, not a problem with the repository. Enable long paths and try again:\n"+
		"    git config --global core.longpaths true", err)
}

// RemoveWorktree removes a worktree; force discards uncommitted changes.
func RemoveWorktree(repoDir, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := Run(repoDir, args...)
	return err
}

// PruneWorktrees drops administrative records for worktrees whose directories
// are already gone.
func PruneWorktrees(repoDir string) error {
	_, err := Run(repoDir, "worktree", "prune")
	return err
}

// DeleteBranch removes a local branch. Without force, git refuses when the
// branch holds commits that are not merged anywhere.
func DeleteBranch(repoDir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Run(repoDir, "branch", flag, branch)
	return err
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path   string
	Head   string
	Branch string
	Bare   bool
}

// ListWorktrees returns every worktree registered with the repository.
func ListWorktrees(repoDir string) ([]Worktree, error) {
	out, err := Run(repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out), nil
}

// ParseWorktreeList parses `git worktree list --porcelain` output, whose
// records are blank-line separated "key value" lines.
func ParseWorktreeList(out string) []Worktree {
	var (
		list []Worktree
		cur  Worktree
		open bool
	)
	flush := func() {
		if open {
			list = append(list, cur)
		}
		cur, open = Worktree{}, false
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
			open = true
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			cur.Bare = true
		}
	}
	flush()
	return list
}

// IsWorktreeOf reports whether path is a worktree belonging to repoDir.
func IsWorktreeOf(repoDir, path string) (bool, error) {
	list, err := ListWorktrees(repoDir)
	if err != nil {
		return false, err
	}
	want, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", path, err)
	}
	for _, w := range list {
		if w.Path == want {
			return true, nil
		}
	}
	return false, nil
}
