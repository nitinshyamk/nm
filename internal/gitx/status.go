package gitx

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status summarizes what would be lost if a worktree were deleted right now.
type Status struct {
	Branch      string
	Staged      int
	Unstaged    int
	Untracked   int
	Unpushed    int
	HasUpstream bool
	LastCommit  time.Time
}

// Dirty reports whether the worktree holds work that exists nowhere else.
func (s Status) Dirty() bool {
	return s.Staged+s.Unstaged+s.Untracked+s.Unpushed > 0
}

// Hazards describes, in human terms, the work that deleting would destroy.
func (s Status) Hazards() []string {
	var out []string
	if n := s.Staged; n > 0 {
		out = append(out, fmt.Sprintf("%s staged", plural(n, "change", "changes")))
	}
	if n := s.Unstaged; n > 0 {
		out = append(out, fmt.Sprintf("%s modified", plural(n, "file", "files")))
	}
	if n := s.Untracked; n > 0 {
		out = append(out, fmt.Sprintf("%s untracked", plural(n, "file", "files")))
	}
	if n := s.Unpushed; n > 0 {
		out = append(out, fmt.Sprintf("%s not on any remote", plural(n, "commit", "commits")))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// GetStatus collects the state of a single worktree, commit age included.
func GetStatus(dir string) (Status, error) {
	return getStatus(dir, true)
}

// GetStatusNoAge is GetStatus without LastCommit, for callers that never render
// a commit age. It is one fewer git invocation per worktree, which the task list
// multiplies by every repository of every task.
func GetStatusNoAge(dir string) (Status, error) {
	return getStatus(dir, false)
}

// getStatus reads a worktree with two or three git invocations, run
// concurrently, because on Windows the spawn itself is the cost: `git --version`
// in an empty directory measures ~400ms here, the same as a full `git status`.
// What a call does matters far less than how many calls there are and whether
// they wait on each other. `--branch` folds the branch name and the upstream
// check into the status call that was already being made, and what remains needs
// nothing from anything else.
func getStatus(dir string, withAge bool) (Status, error) {
	var (
		s         Status
		statusErr error
		wg        sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		out, err := Run(dir, "status", "--porcelain=v1", "--branch", "-z", "--untracked-files=normal")
		if err != nil {
			statusErr = err
			return
		}
		s.Branch, s.HasUpstream, s.Staged, s.Unstaged, s.Untracked = ParsePorcelainBranchZ(out)
	}()
	// Commits that exist on no remote-tracking branch are the ones at risk,
	// whether or not an upstream is configured.
	go func() {
		defer wg.Done()
		if out, err := Run(dir, "rev-list", "--count", "HEAD", "--not", "--remotes"); err == nil {
			s.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
		}
	}()
	if withAge {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := Run(dir, "log", "-1", "--format=%ct"); err == nil {
				if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
					s.LastCommit = time.Unix(secs, 0)
				}
			}
		}()
	}
	wg.Wait()

	if statusErr != nil {
		return Status{}, statusErr
	}
	return s, nil
}

// ParsePorcelainBranchZ reads `git status --porcelain=v1 --branch -z`, which
// leads with a `## ` header naming the branch and its upstream before the file
// entries ParsePorcelainZ counts.
//
// The header has to be peeled rather than passed through: `## main` looks like
// an entry with both an index and a worktree status to the counting loop, so it
// would report every clean repository as having a staged and an unstaged change.
func ParsePorcelainBranchZ(out string) (branch string, hasUpstream bool, staged, unstaged, untracked int) {
	header, rest, found := strings.Cut(out, "\x00")
	if !found || !strings.HasPrefix(header, "## ") {
		// No header means no branch to read, but the entries are still entries.
		return "", false, 0, 0, 0
	}
	staged, unstaged, untracked = ParsePorcelainZ(rest)

	desc := strings.TrimPrefix(header, "## ")
	switch {
	case desc == "HEAD (no branch)":
		// Detached, which CurrentBranch also reports as no branch at all.
		return "", false, staged, unstaged, untracked
	case strings.HasPrefix(desc, "No commits yet on "):
		// A branch that exists only as an unborn HEAD has no upstream yet.
		return strings.TrimPrefix(desc, "No commits yet on "), false, staged, unstaged, untracked
	}

	// `main...origin/main [ahead 1]` — the tracking half is present only when an
	// upstream is configured, which is exactly what HasUpstream asks.
	name, tracking, hasTracking := strings.Cut(desc, "...")
	return name, hasTracking && tracking != "", staged, unstaged, untracked
}

// ParsePorcelainZ counts staged, unstaged, and untracked entries in
// `git status --porcelain=v1 -z` output.
func ParsePorcelainZ(out string) (staged, unstaged, untracked int) {
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 3 {
			continue
		}
		x, y := entry[0], entry[1]
		switch {
		case x == '?' && y == '?':
			untracked++
		default:
			if x != ' ' {
				staged++
			}
			if y != ' ' {
				unstaged++
			}
			// Renames and copies carry their source path as the next record.
			if x == 'R' || x == 'C' {
				i++
			}
		}
	}
	return staged, unstaged, untracked
}
