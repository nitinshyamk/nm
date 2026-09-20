package gitx

import (
	"context"
	"strconv"
	"strings"
)

// Push sends a branch to origin and sets it as the upstream, so later status
// checks can compare against it.
func Push(dir, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()
	_, err := RunContext(ctx, dir, "push", "--quiet", "--set-upstream", "origin", branch)
	return err
}

// CommitAll stages everything in the worktree, including untracked files, and
// commits it.
func CommitAll(dir, message string) error {
	if _, err := Run(dir, "add", "--all"); err != nil {
		return err
	}
	_, err := Run(dir, "commit", "--message", message)
	return err
}

// CommitSubjects lists the subject lines of the commits a branch has added on
// top of base, oldest first.
func CommitSubjects(dir, base string) ([]string, error) {
	out, err := Run(dir, "log", "--reverse", "--format=%s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// CommitsAhead counts the commits a branch has added on top of base.
func CommitsAhead(dir, base string) (int, error) {
	out, err := Run(dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// IsAncestor reports whether commit is reachable from HEAD, which tells us
// whether a recorded base commit still describes where a branch diverged.
func IsAncestor(dir, commit string) bool {
	_, err := Run(dir, "merge-base", "--is-ancestor", commit, "HEAD")
	return err == nil
}
