package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/worktree"
)

// EscalationFile is where a rebase agent writes a conflict it will not decide
// on its own. It lands in the task's artifacts directory, so the task list
// shows it as something waiting for a human.
const EscalationFile = "rebase-escalation.md"

// RebaseOptions describes an existing branch to replay onto its base.
type RebaseOptions struct {
	Repo   string
	Branch string // must already exist on the remote
	Base   string // "" asks the remote for its current default branch
	Remote string // "" means origin
	Now    func() time.Time
}

// Rebase is a task holding one worktree checked out on an existing remote
// branch, ready to be replayed.
//
// It is a task like any other — it lists, enters, and deletes the same way —
// but its worktree sits on a branch other people can already see, which is
// what makes the force push at the end something to be careful about.
type Rebase struct {
	Task   Task
	Base   gitx.Base
	Remote string
}

// RepoName is the repository being rebased.
func (r Rebase) RepoName() string { return r.Task.Repos[0].Name }

// Worktree is the directory the rebase happens in.
func (r Rebase) Worktree() string { return r.Task.Repos[0].Dir }

// Branch is the remote branch being replayed.
func (r Rebase) Branch() string { return r.Task.Repos[0].Branch }

// RebaseName turns a branch name into a task name. A branch may contain
// slashes and a task name becomes a directory, so they are flattened rather
// than burying the task three levels down.
func RebaseName(branch string) string {
	return strings.ReplaceAll(strings.Trim(branch, "/"), "/", "-")
}

// StartRebase creates the task directory and checks the existing branch out
// into it. Nothing is rewritten yet: Replay does that.
//
// Creation is transactional the same way Create is — a failure removes the
// worktree and the directory rather than leaving half a task behind.
func StartRebase(cfg config.Config, opts RebaseOptions) (rb Rebase, err error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}

	if err := worktree.ValidateName(opts.Branch); err != nil {
		return Rebase{}, fmt.Errorf("branch %w", err)
	}

	repoDir := cfg.RepoPath(opts.Repo)
	if info, statErr := os.Stat(repoDir); statErr != nil || !info.IsDir() {
		return Rebase{}, fmt.Errorf("no repository %q in %s", opts.Repo, cfg.Projects())
	}
	if !gitx.IsRepo(repoDir) {
		return Rebase{}, fmt.Errorf("%s is not a git repository", repoDir)
	}
	if !gitx.HasRemote(repoDir, remote) {
		return Rebase{}, fmt.Errorf("%s has no %s remote, so there is no branch to rebase", repoDir, remote)
	}

	// The branch has to be on the remote already: this command replays work
	// that exists, and creating it here would hide a typo as a new branch.
	exists, err := gitx.RemoteBranchExists(repoDir, remote, opts.Branch)
	if err != nil {
		return Rebase{}, err
	}
	if !exists {
		return Rebase{}, fmt.Errorf("%s has no branch %q — rebase replays a branch that is already pushed; "+
			"to start a new one use: nm task new %s -n <name>", remote, opts.Branch, opts.Repo)
	}
	if err := gitx.FetchBranch(repoDir, remote, opts.Branch); err != nil {
		return Rebase{}, err
	}
	override := opts.Base
	if override == "" {
		override = cfg.DefaultBaseBranch
	}
	base, err := gitx.ResolveBase(repoDir, override, false)
	if err != nil {
		return Rebase{}, err
	}
	if base.Branch == opts.Branch {
		return Rebase{}, fmt.Errorf("%s is the base branch, so there is nothing to rebase it onto", opts.Branch)
	}

	name := RebaseName(opts.Branch)
	hash := Hash(cfg.HashLength, name, []string{opts.Repo})
	dir := filepath.Join(cfg.Tasks(), DirName(name, hash))
	if _, statErr := os.Stat(dir); statErr == nil {
		return Rebase{}, fmt.Errorf("a rebase of %s is already in flight at %s; finish it, or remove it with: nm task remove %s",
			opts.Branch, dir, DirName(name, hash))
	}

	// Checked last, so the more specific refusals above get to speak first:
	// a rebase of this branch already in flight is the usual reason git would
	// refuse to hand the branch over.
	if held, taken := gitx.WorktreeForBranch(repoDir, opts.Branch); taken {
		return Rebase{}, fmt.Errorf("branch %s is already checked out at %s; git cannot check it out twice", opts.Branch, held)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Rebase{}, fmt.Errorf("creating %s: %w", dir, err)
	}
	worktreeDir := filepath.Join(dir, opts.Repo+"-"+hash)
	defer func() {
		if err == nil {
			return
		}
		// The branch is shared, so unwinding must not delete it: pass the
		// empty string where Remove would otherwise drop it.
		_, _ = worktree.Remove(repoDir, worktreeDir, "", true)
		_ = os.RemoveAll(dir)
	}()

	if err := gitx.AddWorktreeTracking(repoDir, worktreeDir, remote, opts.Branch); err != nil {
		return Rebase{}, err
	}

	t := Task{
		Name:      name,
		Hash:      hash,
		CreatedAt: now(),
		Dir:       dir,
		Repos: []Repo{{
			Name:       opts.Repo,
			Dir:        worktreeDir,
			Branch:     opts.Branch,
			BaseBranch: base.Branch,
			BaseCommit: base.Commit,
			Source:     repoDir,
		}},
	}
	if err := os.MkdirAll(t.Artifacts(cfg), 0o755); err != nil {
		return Rebase{}, fmt.Errorf("creating the artifacts directory: %w", err)
	}
	if err := t.Save(); err != nil {
		return Rebase{}, err
	}
	return Rebase{Task: t, Base: base, Remote: remote}, nil
}

// RebaseOutcome is what the replay did to the branch.
type RebaseOutcome string

// The three ways a replay ends.
const (
	RebaseUpToDate   RebaseOutcome = "up to date" // already on top of the base
	RebaseReplayed   RebaseOutcome = "replayed"   // rewritten, and ready to push
	RebaseConflicted RebaseOutcome = "conflicted" // stopped partway, waiting on a human or an agent
)

// RebaseReport describes one replay.
type RebaseReport struct {
	Outcome   RebaseOutcome
	Before    string   // the commit the branch was on
	After     string   // the commit it ended on
	Conflicts []string // paths git left with conflict markers
	Err       error    // the git failure, when conflicted
}

// Changed reports whether the replay moved the branch. That is what makes the
// force push necessary — and, when nothing moved, unnecessary.
func (r RebaseReport) Changed() bool { return r.Before != r.After }

// Replay runs `git pull --rebase <remote> <base>` in the worktree.
//
// A conflict is an outcome, not an error: the rebase is left in progress so
// the conflicted state is there for whoever resolves it. Only a failure that
// is not a conflict comes back as an error.
func (r Rebase) Replay() (RebaseReport, error) {
	dir := r.Worktree()

	before, err := gitx.Head(dir)
	if err != nil {
		return RebaseReport{}, err
	}
	report := RebaseReport{Before: before, After: before}

	if err := gitx.PullRebase(dir, r.Remote, r.Base.Branch); err != nil {
		if !errors.Is(err, gitx.ErrRebaseConflict) {
			return report, err
		}
		report.Outcome, report.Err = RebaseConflicted, err
		report.Conflicts, _ = gitx.ConflictedFiles(dir)
		return report, nil
	}

	after, err := gitx.Head(dir)
	if err != nil {
		return report, err
	}
	report.After = after
	report.Outcome = RebaseReplayed
	if !report.Changed() {
		report.Outcome = RebaseUpToDate
	}
	return report, nil
}

// Push overwrites the branch on the remote with the replayed history.
//
// A rebase rewrites commits, so a plain push cannot work; --force-with-lease
// is what keeps that from discarding whatever someone else pushed while the
// rebase was running.
func (r Rebase) Push() error {
	return gitx.PushForceWithLease(r.Worktree(), r.Remote, r.Branch())
}

// RebasePrompt is what the background agent is told to do: finish the rebase
// git stopped on, or — when git finished it on its own and the caller asked
// for verification — check the result before it is pushed.
//
// It is deliberately explicit about the two things that make this different
// from ordinary agent work: the branch is shared, so the only allowed push is
// a leased force push; and a conflict that is really a decision belongs to a
// human, not to the agent.
func RebasePrompt(cfg config.Config, r Rebase, report RebaseReport) string {
	var b strings.Builder

	if report.Outcome == RebaseConflicted {
		b.WriteString("Finish a git rebase that stopped with conflicts.\n\n")
	} else {
		b.WriteString("Check a git rebase that has already finished, then push it.\n\n")
	}
	fmt.Fprintf(&b, "  repository  %s\n", r.RepoName())
	fmt.Fprintf(&b, "  worktree    %s\n", r.Worktree())
	fmt.Fprintf(&b, "  branch      %s (already on %s — other people can see it)\n", r.Branch(), r.Remote)
	fmt.Fprintf(&b, "  rebased on  %s, from %s\n", r.Base.Branch, r.Base.Source)
	fmt.Fprintf(&b, "  artifacts   %s\n\n", r.Task.Artifacts(cfg))

	fmt.Fprintf(&b, "`git pull --rebase %s %s` ", r.Remote, r.Base.Branch)
	step := 1
	switch {
	case report.Outcome != RebaseConflicted:
		fmt.Fprintf(&b, "replayed the branch cleanly, onto\n%s. Nothing has been pushed: that is your job, once you have\nchecked it.\n\n", short(report.After))
	case len(report.Conflicts) == 0:
		b.WriteString("stopped. The rebase is still in progress; run\n`git status` to see where.\n\n")
	default:
		b.WriteString("stopped with conflicts in:\n\n")
		for _, path := range report.Conflicts {
			fmt.Fprintf(&b, "  - %s\n", path)
		}
		b.WriteString("\n")
	}

	b.WriteString("Work only inside that worktree. In order:\n\n")
	if report.Outcome == RebaseConflicted {
		fmt.Fprintf(&b, "%d. Resolve each conflict so both sides survive: what the branch was doing,\n", step)
		fmt.Fprintf(&b, "   and what %s changed underneath it. Read the code around the conflict and\n", r.Base.Branch)
		b.WriteString("   both versions of the commit (`git log --oneline -5`, `git show REBASE_HEAD`)\n")
		b.WriteString("   before you decide anything.\n")
		step++
		fmt.Fprintf(&b, "%d. `git add` the files you resolved, then `git rebase --continue`. Later\n", step)
		b.WriteString("   commits may conflict too — repeat until git says the rebase is done.\n")
		step++
	} else {
		fmt.Fprintf(&b, "%d. Read what the replay did (`git log --oneline %s..HEAD`, `git diff %s`).\n",
			step, r.Base.Branch, r.Base.Branch)
		b.WriteString("   git merged the text without complaining, which is not the same as the\n")
		b.WriteString("   result being right: look for calls to things the base renamed or removed.\n")
		step++
	}
	fmt.Fprintf(&b, "%d. Run the repository's own verification gate before pushing. Look for it in\n", step)
	b.WriteString("   AGENTS.md, CLAUDE.md, mise.toml, Makefile, or package.json (for example\n")
	b.WriteString("   `mise run pre-commit`, `make test`, `npm test`). Fix whatever the rebase\n")
	b.WriteString("   broke. Never weaken a test or a lint rule to get a green result.\n")
	step++
	if report.Outcome == RebaseConflicted {
		fmt.Fprintf(&b, "%d. Only once the rebase has finished and the gate passes:\n\n", step)
	} else {
		fmt.Fprintf(&b, "%d. Only once the gate passes:\n\n", step)
	}
	fmt.Fprintf(&b, "       git push --force-with-lease %s %s\n\n", r.Remote, r.Branch())
	b.WriteString("   --force-with-lease is required, not optional: it refuses the push if\n")
	b.WriteString("   someone else moved the branch while you were working. If it is refused,\n")
	b.WriteString("   stop and escalate — do not retry with --force.\n\n")

	b.WriteString("Stop and escalate instead of guessing when:\n\n")
	b.WriteString("  - a conflict is a decision rather than a merge: the two sides do genuinely\n")
	b.WriteString("    different things and picking one changes behaviour;\n")
	b.WriteString("  - the replay is clean as text but wrong in meaning, and putting it right\n")
	b.WriteString("    means deciding what the branch should now do;\n")
	fmt.Fprintf(&b, "  - %s removed or reworked something this branch is built on;\n", r.Base.Branch)
	b.WriteString("  - the gate fails for a reason you cannot fix without changing what the\n")
	b.WriteString("    branch set out to do.\n\n")

	b.WriteString("To escalate: leave the worktree exactly as it is, write what you found and\n")
	b.WriteString("what the choice is to\n\n")
	fmt.Fprintf(&b, "       %s\n\n", filepath.Join(r.Task.Artifacts(cfg), EscalationFile))
	b.WriteString("and stop with a short summary saying a human needs to decide. Do not push\na guess.\n\n")

	b.WriteString("Never run `git rebase --abort`, `git reset --hard`, or `git push` without\n")
	b.WriteString("--force-with-lease, and never touch a repository other than this worktree.\n")
	return b.String()
}

// short abbreviates a commit id for prose.
func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
