package gitx

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRemoteUnreachable means origin could not be consulted, so nm cannot tell
// what the up-to-date default branch is.
var ErrRemoteUnreachable = errors.New("cannot reach origin")

// Base is the commit a new worktree branches from.
type Base struct {
	Branch string // e.g. "main"
	Commit string // the resolved object id
	Source string // human-readable provenance, shown to the user
}

// ResolveBase determines the branch point for a new worktree.
//
// The remote is the authority: a local refs/remotes/origin/HEAD goes stale when
// the remote's default branch changes, and origin/<branch> goes stale as soon as
// anyone else pushes. So unless offline is set, nm asks origin what its default
// branch is and fetches it, branching from exactly what came back.
func ResolveBase(repoDir, override string, offline bool) (Base, error) {
	if !HasRemote(repoDir, "origin") {
		return localBase(repoDir, override, "no origin remote")
	}
	if offline {
		return localBase(repoDir, override, "--offline")
	}

	ctx, cancel := context.WithTimeout(context.Background(), NetworkTimeout)
	defer cancel()

	branch := override
	if branch == "" {
		out, err := RunContext(ctx, repoDir, "ls-remote", "--symref", "origin", "HEAD")
		if err != nil {
			return Base{}, fmt.Errorf("%w: %w", ErrRemoteUnreachable, err)
		}
		branch, err = ParseSymrefHead(out)
		if err != nil {
			return Base{}, err
		}
	}

	if _, err := RunContext(ctx, repoDir, "fetch", "--quiet", "origin", branch); err != nil {
		return Base{}, fmt.Errorf("%w: %w", ErrRemoteUnreachable, err)
	}
	commit, err := Run(repoDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return Base{}, err
	}
	return Base{Branch: branch, Commit: commit, Source: "origin/" + branch + " (just fetched)"}, nil
}

// localBase resolves a branch point without touching the network.
func localBase(repoDir, override, why string) (Base, error) {
	if override != "" {
		commit, err := Run(repoDir, "rev-parse", override)
		if err != nil {
			return Base{}, fmt.Errorf("resolving base branch %q: %w", override, err)
		}
		return Base{Branch: override, Commit: commit, Source: fmt.Sprintf("local %s (%s)", override, why)}, nil
	}

	// Best local guess, in descending order of trustworthiness.
	if out, err := Run(repoDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch := strings.TrimPrefix(out, "origin/")
		if commit, err := Run(repoDir, "rev-parse", out); err == nil {
			return Base{Branch: branch, Commit: commit, Source: fmt.Sprintf("local %s (%s)", out, why)}, nil
		}
	}
	branch, err := CurrentBranch(repoDir)
	if err != nil {
		return Base{}, err
	}
	if branch == "" {
		return Base{}, fmt.Errorf("%s is on a detached HEAD and has no usable default branch", repoDir)
	}
	commit, err := Run(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return Base{}, err
	}
	return Base{Branch: branch, Commit: commit, Source: fmt.Sprintf("local %s (%s)", branch, why)}, nil
}

// ParseSymrefHead extracts the default branch from `git ls-remote --symref
// origin HEAD` output, whose first line reads "ref: refs/heads/main\tHEAD".
func ParseSymrefHead(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "ref:"))
		if len(fields) == 0 {
			continue
		}
		if branch := strings.TrimPrefix(fields[0], "refs/heads/"); branch != "" && branch != fields[0] {
			return branch, nil
		}
	}
	return "", fmt.Errorf("origin did not report a default branch (no symref in ls-remote output)")
}
