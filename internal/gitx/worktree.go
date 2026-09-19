package gitx

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AddWorktree creates a worktree at path on a new branch cut from commit.
func AddWorktree(repoDir, path, branch, commit string) error {
	_, err := Run(repoDir, "worktree", "add", "-b", branch, path, commit)
	return err
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
