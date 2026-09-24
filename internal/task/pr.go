package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/gitx"
)

// PRState is what happened to one repository during a pull request run.
type PRState string

// The outcomes of putting one repository up for review.
const (
	PROpened  PRState = "opened"  // a new pull request
	PRExisted PRState = "existed" // already open, pushed again
	PRSkipped PRState = "skipped" // nothing to review, or the user skipped it
	PRFailed  PRState = "failed"
)

// PRResult is the per-repository outcome of Publish.
type PRResult struct {
	Repo   string
	State  PRState
	Reason string // why it was skipped or how it failed
	URL    string
	Number int
}

// OK reports whether the repository ended up with an open pull request.
func (r PRResult) OK() bool { return r.State == PROpened || r.State == PRExisted }

// CommitDecision is what the caller wants done about uncommitted work.
type CommitDecision int

// What to do with a repository that has uncommitted changes.
const (
	CommitSkipRepo CommitDecision = iota // leave this repository alone
	CommitChanges                        // commit everything, then push
	CommitAbort                          // stop the whole run
)

// Asker decides what to do about uncommitted work in one repository. It
// returns the decision and, when committing, the message to use. The CLI
// implements this with a dialog and the emacs editor; tests implement it with
// a function.
type Asker func(repo Repo, status gitx.Status) (CommitDecision, string, error)

// Forge is the slice of the GitHub CLI that publishing needs. Taking an
// interface lets the whole pipeline — commit, push, create, cross-link — be
// tested against local repositories with no network and no gh login.
type Forge interface {
	CheckAuth() error
	Find(dir, branch string) (*forge.PR, error)
	Create(opts forge.CreateOptions) (*forge.PR, error)
	EditBody(dir, branch, bodyFile string) error
}

// Reviewer is the slice of the GitHub CLI that deciding a task's state needs:
// whether its pull request has been approved, what feedback arrived after it was
// published, and whether it can be merged.
//
// It is separate from Forge rather than added to it because publishing and
// reviewing are used by different callers — Publish needs none of this, and the
// orchestrator needs none of Create. A fake for one should not have to implement
// the other.
type Reviewer interface {
	CheckAuth() error
	StatusForBranch(dir, branch string) (*forge.Status, error)
	Merge(dir string, number int, method string) error
}

// PublishOptions controls a pull request run.
type PublishOptions struct {
	Ask   Asker
	Draft bool
	GH    Forge
}

// Publish pushes every repository in a task and opens a pull request for each.
//
// The task record is updated as it goes, so a run that fails partway still
// remembers the pull requests it did open and a re-run picks up from there.
func Publish(t *Task, opts PublishOptions) ([]PRResult, error) {
	if err := opts.GH.CheckAuth(); err != nil {
		return nil, err
	}

	results := make([]PRResult, 0, len(t.Repos))
	for i := range t.Repos {
		result, abort := publishRepo(t, &t.Repos[i], opts)
		results = append(results, result)
		if abort {
			break
		}
	}

	if err := crossLink(t, results, opts.GH); err != nil {
		return results, err
	}
	if err := t.Save(); err != nil {
		return results, err
	}
	return results, nil
}

// publishRepo handles one repository. The bool reports whether the whole run
// should stop.
func publishRepo(t *Task, repo *Repo, opts PublishOptions) (PRResult, bool) {
	result := PRResult{Repo: repo.Name}

	status, err := gitx.GetStatus(repo.Dir)
	if err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}

	if status.Staged+status.Unstaged+status.Untracked > 0 {
		if opts.Ask == nil {
			result.State = PRFailed
			result.Reason = "uncommitted changes: " + strings.Join(status.Hazards(), ", ")
			return result, false
		}
		decision, message, askErr := opts.Ask(*repo, status)
		switch {
		case askErr != nil:
			result.State, result.Reason = PRFailed, askErr.Error()
			return result, false
		case decision == CommitAbort:
			result.State, result.Reason = PRSkipped, "cancelled"
			return result, true
		case decision == CommitSkipRepo:
			result.State, result.Reason = PRSkipped, "left uncommitted"
			return result, false
		}
		if err := gitx.CommitAll(repo.Dir, message); err != nil {
			result.State, result.Reason = PRFailed, err.Error()
			return result, false
		}
	}

	base := repo.mergeBase()
	ahead, err := gitx.CommitsAhead(repo.Dir, base)
	if err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}
	if ahead == 0 {
		result.State, result.Reason = PRSkipped, "no commits to review"
		return result, false
	}

	if err := gitx.Push(repo.Dir, repo.Branch); err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}

	existing, err := opts.GH.Find(repo.Dir, repo.Branch)
	if err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}
	if existing != nil {
		repo.PRURL, repo.PRNumber = existing.URL, existing.Number
		result.State, result.URL, result.Number = PRExisted, existing.URL, existing.Number
		return result, false
	}

	bodyFile, cleanup, err := writeBody(t, *repo, base, nil)
	if err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}
	defer cleanup()

	pr, err := opts.GH.Create(forge.CreateOptions{
		Dir:      repo.Dir,
		Base:     repo.BaseBranch,
		Head:     repo.Branch,
		Title:    t.Name,
		BodyFile: bodyFile,
		Draft:    opts.Draft,
	})
	if err != nil {
		result.State, result.Reason = PRFailed, err.Error()
		return result, false
	}
	if pr != nil {
		repo.PRURL, repo.PRNumber = pr.URL, pr.Number
		result.URL, result.Number = pr.URL, pr.Number
	}
	result.State = PROpened
	return result, false
}

// mergeBase is the commit a branch diverged from. The recorded base commit is
// exact, but a rebase can leave it off the branch, in which case the base
// branch's remote tip is the honest comparison.
func (r Repo) mergeBase() string {
	if r.BaseCommit != "" && gitx.IsAncestor(r.Dir, r.BaseCommit) {
		return r.BaseCommit
	}
	return "origin/" + r.BaseBranch
}

// crossLink appends the sibling pull requests to each body, which can only
// happen once they all exist.
func crossLink(t *Task, results []PRResult, gh Forge) error {
	var opened []PRResult
	for _, r := range results {
		if r.OK() {
			opened = append(opened, r)
		}
	}
	if len(opened) < 2 {
		return nil // nothing to link to
	}

	for i := range t.Repos {
		repo := &t.Repos[i]
		if repo.PRURL == "" {
			continue
		}
		bodyFile, cleanup, err := writeBody(t, *repo, repo.mergeBase(), opened)
		if err != nil {
			return err
		}
		err = gh.EditBody(repo.Dir, repo.Branch, bodyFile)
		cleanup()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeBody renders a pull request body to a temporary file, which keeps a
// long body out of the argument list.
func writeBody(t *Task, repo Repo, base string, siblings []PRResult) (path string, cleanup func(), err error) {
	subjects, err := gitx.CommitSubjects(repo.Dir, base)
	if err != nil {
		return "", func() {}, err
	}
	body := PRBody(*t, repo, subjects, siblings)

	file, err := os.CreateTemp("", "nm-pr-body-*.md")
	if err != nil {
		return "", func() {}, fmt.Errorf("writing the pull request body: %w", err)
	}
	cleanup = func() { _ = os.Remove(file.Name()) }
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("writing the pull request body: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("writing the pull request body: %w", err)
	}
	return file.Name(), cleanup, nil
}

// PRBody builds the description: what the task set out to do, what was
// actually committed, and where the sibling repositories' reviews are.
func PRBody(t Task, repo Repo, subjects []string, siblings []PRResult) string {
	var b strings.Builder

	if prompt := strings.TrimSpace(t.Prompt); prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}

	if len(subjects) > 0 {
		b.WriteString("## Commits\n\n")
		for _, subject := range subjects {
			fmt.Fprintf(&b, "- %s\n", subject)
		}
		b.WriteString("\n")
	}

	var others []PRResult
	for _, s := range siblings {
		if s.Repo != repo.Name {
			others = append(others, s)
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(&b, "---\n\nPart of task `%s`:\n\n", t.Label())
		for _, s := range others {
			fmt.Fprintf(&b, "- %s: %s\n", s.Repo, s.URL)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "<sub>opened by nm from `%s`</sub>\n", filepath.Base(t.Dir))
	return b.String()
}
