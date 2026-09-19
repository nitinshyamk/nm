package gitx

import (
	"fmt"
	"strconv"
	"strings"
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

// GetStatus collects the state of a single worktree.
func GetStatus(dir string) (Status, error) {
	var s Status

	branch, err := CurrentBranch(dir)
	if err != nil {
		return s, err
	}
	s.Branch = branch

	porcelain, err := Run(dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return s, err
	}
	s.Staged, s.Unstaged, s.Untracked = ParsePorcelainZ(porcelain)

	if _, err := Run(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		s.HasUpstream = true
	}

	// Commits that exist on no remote-tracking branch are the ones at risk,
	// whether or not an upstream is configured.
	if out, err := Run(dir, "rev-list", "--count", "HEAD", "--not", "--remotes"); err == nil {
		s.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	if out, err := Run(dir, "log", "-1", "--format=%ct"); err == nil {
		if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			s.LastCommit = time.Unix(secs, 0)
		}
	}
	return s, nil
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
