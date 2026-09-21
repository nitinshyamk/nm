package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
)

// pushBranch adds a commit on a new branch in the source checkout, pushes it,
// and puts the checkout back on main — which is what a branch someone else
// opened looks like from here.
func pushBranch(t *testing.T, cfg config.Config, repo, branch, file, content string) {
	t.Helper()
	dir := cfg.RepoPath(repo)
	git(t, dir, "checkout", "--quiet", "-b", branch)
	writeAt(t, dir, file, content)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "work on "+branch)
	git(t, dir, "push", "--quiet", "-u", "origin", branch)
	git(t, dir, "checkout", "--quiet", "main")
	// Leave no local branch behind: the branch nm rebases normally exists
	// only on the remote.
	git(t, dir, "branch", "-D", branch)
}

// advanceBase puts a new commit on main and pushes it, so a branch cut before
// it has something to be replayed onto.
func advanceBase(t *testing.T, cfg config.Config, repo, file, content string) {
	t.Helper()
	dir := cfg.RepoPath(repo)
	writeAt(t, dir, file, content)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "move main")
	git(t, dir, "push", "--quiet", "origin", "main")
}

func writeAt(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartRebaseChecksOutTheRemoteBranch(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatalf("StartRebase: %v", err)
	}

	// The branch keeps its own name — it is what gets pushed back — while the
	// task directory is the thing that carries the hash.
	if rb.Branch() != "feature" {
		t.Errorf("branch = %q, want the remote branch name unchanged", rb.Branch())
	}
	if base := filepath.Base(rb.Task.Dir); base != "feature-"+rb.Task.Hash {
		t.Errorf("task directory %q does not carry the hash", base)
	}
	if rb.Base.Branch != "main" {
		t.Errorf("base = %q, want main", rb.Base.Branch)
	}

	branch, err := gitx.CurrentBranch(rb.Worktree())
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature" {
		t.Errorf("the worktree is on %q, want feature", branch)
	}
	if _, err := os.Stat(filepath.Join(rb.Worktree(), "feature.txt")); err != nil {
		t.Errorf("the worktree does not hold the branch's work: %v", err)
	}
	for _, dir := range []string{rb.Task.Artifacts(cfg), rb.Task.Input(cfg), rb.Task.Scratch(cfg)} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("no %s directory: %v", filepath.Base(dir), err)
		}
	}
	if _, err := Load(rb.Task.Dir); err != nil {
		t.Errorf("the task record was not written: %v", err)
	}
}

func TestStartRebaseRefusesWhatItCannotReplay(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")

	cases := []struct {
		name string
		opts RebaseOptions
		want string
	}{
		{
			name: "a branch that is not on the remote",
			opts: RebaseOptions{Repo: "nm", Branch: "typo"},
			want: "has no branch",
		},
		{
			name: "a repository that is not there",
			opts: RebaseOptions{Repo: "nope", Branch: "feature"},
			want: "no repository",
		},
		{
			name: "the base branch itself",
			opts: RebaseOptions{Repo: "nm", Branch: "main"},
			want: "nothing to rebase it onto",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := StartRebase(cfg, tc.opts)
			if err == nil {
				t.Fatal("StartRebase accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestStartRebaseRefusesABranchAlreadyCheckedOut(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")
	// Put the source checkout itself on the branch: git will not hand the
	// same branch to a second worktree.
	git(t, cfg.RepoPath("nm"), "checkout", "--quiet", "-b", "feature", "origin/feature")

	_, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err == nil {
		t.Fatal("StartRebase accepted a branch that is checked out elsewhere")
	}
	if !strings.Contains(err.Error(), "already checked out") {
		t.Errorf("error %q does not say where the branch is", err)
	}
}

func TestStartRebaseIsTransactionalAndRefusesASecondRun(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err == nil {
		t.Fatal("a second rebase of the same branch was accepted")
	}
	if !strings.Contains(err.Error(), "already in flight") {
		t.Errorf("error %q does not explain that the first one is still there", err)
	}

	// The refusal must not have disturbed the rebase already in progress.
	if _, err := os.Stat(rb.Worktree()); err != nil {
		t.Errorf("the first rebase's worktree was removed: %v", err)
	}
}

func TestReplayCleanRewritesAndPushes(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")
	advanceBase(t, cfg, "nm", "other.txt", "main moved\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := rb.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if report.Outcome != RebaseReplayed {
		t.Fatalf("outcome = %q, want %q (conflicts: %v, err: %v)", report.Outcome, RebaseReplayed, report.Conflicts, report.Err)
	}
	if !report.Changed() {
		t.Error("a replay onto a moved base left the branch where it was")
	}

	// Both sides survive the replay.
	for _, name := range []string{"feature.txt", "other.txt"} {
		if _, err := os.Stat(filepath.Join(rb.Worktree(), name)); err != nil {
			t.Errorf("%s is missing after the rebase: %v", name, err)
		}
	}

	if err := rb.Push(); err != nil {
		t.Fatalf("Push: %v", err)
	}
	remote := git(t, rb.Worktree(), "rev-parse", "origin/feature")
	if remote != report.After {
		t.Errorf("origin/feature is at %s, want the replayed %s", remote, report.After)
	}
}

func TestReplayReportsAnAlreadyCurrentBranch(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := rb.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if report.Outcome != RebaseUpToDate {
		t.Errorf("outcome = %q, want %q", report.Outcome, RebaseUpToDate)
	}
	if report.Changed() {
		t.Error("a branch already on top of main was rewritten anyway")
	}
}

func TestReplayLeavesAConflictInPlaceForResolving(t *testing.T) {
	cfg := env(t, "nm")
	// Both sides edit the same line, which is the only thing git cannot
	// decide on its own.
	pushBranch(t, cfg, "nm", "feature", "shared.txt", "the branch's line\n")
	advanceBase(t, cfg, "nm", "shared.txt", "main's line\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := rb.Replay()
	if err != nil {
		t.Fatalf("a conflict is an outcome, not an error: %v", err)
	}
	if report.Outcome != RebaseConflicted {
		t.Fatalf("outcome = %q, want %q", report.Outcome, RebaseConflicted)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0] != "shared.txt" {
		t.Errorf("conflicts = %v, want [shared.txt]", report.Conflicts)
	}
	// The rebase has to be left running: that mid-rebase state is what
	// whoever resolves it works from.
	if !gitx.RebaseInProgress(rb.Worktree()) {
		t.Error("the rebase was not left in progress")
	}

	prompt := RebasePrompt(cfg, rb, report)
	for _, want := range []string{
		"shared.txt",
		rb.Worktree(),
		"--force-with-lease origin feature",
		EscalationFile,
		"git rebase --continue",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the agent prompt never mentions %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"rebase --abort", "reset --hard"} {
		// They appear only in the sentence forbidding them.
		if strings.Count(prompt, unwanted) != 1 {
			t.Errorf("%q should appear once, as a prohibition", unwanted)
		}
	}
}

func TestRebaseNameFlattensBranchPaths(t *testing.T) {
	cases := map[string]string{
		"feature":            "feature",
		"feature/auth":       "feature-auth",
		"user/nitin/fix-tui": "user-nitin-fix-tui",
		"/leading":           "leading",
	}
	for branch, want := range cases {
		if got := RebaseName(branch); got != want {
			t.Errorf("RebaseName(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestStartRebaseWithASlashedBranchStaysOneDirectoryDeep(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature/auth", "auth.txt", "one\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature/auth"})
	if err != nil {
		t.Fatalf("StartRebase: %v", err)
	}
	if rb.Branch() != "feature/auth" {
		t.Errorf("branch = %q, want the slash kept", rb.Branch())
	}
	if parent := filepath.Dir(rb.Task.Dir); parent != cfg.Tasks() {
		t.Errorf("task directory %q is nested; it should sit directly under %s", rb.Task.Dir, cfg.Tasks())
	}
}

func TestRebasePromptForACleanReplayAsksForTheChecks(t *testing.T) {
	cfg := env(t, "nm")
	pushBranch(t, cfg, "nm", "feature", "feature.txt", "one\n")
	advanceBase(t, cfg, "nm", "other.txt", "main moved\n")

	rb, err := StartRebase(cfg, RebaseOptions{Repo: "nm", Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := rb.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != RebaseReplayed {
		t.Fatalf("outcome = %q, want a clean replay", report.Outcome)
	}

	prompt := RebasePrompt(cfg, rb, report)
	for _, want := range []string{
		"already finished",
		"mise run pre-commit",
		"--force-with-lease origin feature",
		EscalationFile,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the verify prompt never mentions %q:\n%s", want, prompt)
		}
	}
	// There are no conflicts to resolve, so it must not tell the agent to.
	for _, unwanted := range []string{"git rebase --continue", "Resolve each conflict"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("the verify prompt talks about resolving conflicts (%q):\n%s", unwanted, prompt)
		}
	}
	// The numbered steps must still run 1, 2, 3 after the resolve step is
	// dropped.
	for _, want := range []string{"\n1. ", "\n2. ", "\n3. "} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the verify prompt is missing step %q:\n%s", strings.TrimSpace(want), prompt)
		}
	}
	if strings.Contains(prompt, "\n4. ") {
		t.Errorf("the verify prompt has a fourth step it should not:\n%s", prompt)
	}
}
