package gitx

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRemoteBranchExistsAsksTheRemote(t *testing.T) {
	isolate(t)
	bare := newRemote(t, "main")
	work := clone(t, bare)

	git(t, work, "checkout", "--quiet", "-b", "feature")
	writeFile(t, work, "feature.txt", "one\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "feature")

	// Local only so far: the remote has not heard of it.
	if found, err := RemoteBranchExists(work, "origin", "feature"); err != nil || found {
		t.Errorf("RemoteBranchExists before the push = %v, %v; want false", found, err)
	}

	git(t, work, "push", "--quiet", "-u", "origin", "feature")
	if found, err := RemoteBranchExists(work, "origin", "feature"); err != nil || !found {
		t.Errorf("RemoteBranchExists after the push = %v, %v; want true", found, err)
	}
	if found, err := RemoteBranchExists(work, "origin", "never"); err != nil || found {
		t.Errorf("RemoteBranchExists for a branch nobody made = %v, %v; want false", found, err)
	}
}

func TestRebaseLifecycle(t *testing.T) {
	isolate(t)
	bare := newRemote(t, "main")
	work := clone(t, bare)

	// A branch someone else pushed, and a main that has moved since.
	git(t, work, "checkout", "--quiet", "-b", "feature")
	writeFile(t, work, "shared.txt", "the branch's line\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "branch work")
	git(t, work, "push", "--quiet", "-u", "origin", "feature")

	git(t, work, "checkout", "--quiet", "main")
	writeFile(t, work, "shared.txt", "main's line\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "main work")
	git(t, work, "push", "--quiet", "origin", "main")
	git(t, work, "branch", "--quiet", "-D", "feature")

	if got := RemoteBranches(work, "origin"); len(got) != 2 {
		t.Errorf("RemoteBranches = %v, want main and feature", got)
	}

	// Checking the remote branch out into a worktree creates a local branch
	// tracking it.
	wt := filepath.Join(t.TempDir(), "feature-wt")
	if err := AddWorktreeTracking(work, wt, "origin", "feature"); err != nil {
		t.Fatalf("AddWorktreeTracking: %v", err)
	}
	if branch, _ := CurrentBranch(wt); branch != "feature" {
		t.Fatalf("the worktree is on %q, want feature", branch)
	}
	if held, ok := WorktreeForBranch(work, "feature"); !ok || !samePath(t, held, wt) {
		t.Errorf("WorktreeForBranch = %q, %v; want %q", held, ok, wt)
	}

	// Both sides touched the same line, so the replay stops.
	err := PullRebase(wt, "origin", "main")
	if !errors.Is(err, ErrRebaseConflict) {
		t.Fatalf("PullRebase = %v, want a conflict", err)
	}
	if !RebaseInProgress(wt) {
		t.Error("the rebase was not left in progress for resolving")
	}
	conflicts, err := ConflictedFiles(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Fatalf("ConflictedFiles = %v, want [shared.txt]", conflicts)
	}

	// Resolving and continuing is what an agent would do next.
	writeFile(t, wt, "shared.txt", "both lines\n")
	git(t, wt, "add", "shared.txt")
	if _, err := Run(wt, "-c", "core.editor=true", "rebase", "--continue"); err != nil {
		t.Fatalf("rebase --continue: %v", err)
	}
	if RebaseInProgress(wt) {
		t.Error("the rebase is still in progress after --continue")
	}

	// A rebase rewrote history, so only a forced push can land it.
	if err := Push(wt, "feature"); err == nil {
		t.Error("a plain push of rewritten history was accepted")
	}
	if err := PushForceWithLease(wt, "origin", "feature"); err != nil {
		t.Fatalf("PushForceWithLease: %v", err)
	}
	head, err := Head(wt)
	if err != nil {
		t.Fatal(err)
	}
	if remote := git(t, wt, "rev-parse", "origin/feature"); remote != head {
		t.Errorf("origin/feature = %s, want the replayed %s", remote, head)
	}
}

func TestPushForceWithLeaseRefusesWhenTheBranchMoved(t *testing.T) {
	isolate(t)
	bare := newRemote(t, "main")
	work := clone(t, bare)

	git(t, work, "checkout", "--quiet", "-b", "feature")
	writeFile(t, work, "a.txt", "one\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "one")
	git(t, work, "push", "--quiet", "-u", "origin", "feature")

	// Someone else pushes to the branch while we are not looking.
	other := clone(t, bare)
	git(t, other, "checkout", "--quiet", "feature")
	writeFile(t, other, "b.txt", "theirs\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "theirs")
	git(t, other, "push", "--quiet", "origin", "feature")

	// We rewrite our copy and try to land it. The lease is the only thing
	// standing between this and silently deleting their commit.
	git(t, work, "commit", "--quiet", "--amend", "-m", "one, reworded")
	if err := PushForceWithLease(work, "origin", "feature"); err == nil {
		t.Fatal("the leased push overwrote a branch that had moved")
	}
}

func TestRebaseInProgressIsFalseOnAQuietTree(t *testing.T) {
	isolate(t)
	work := clone(t, newRemote(t, "main"))
	if RebaseInProgress(work) {
		t.Error("RebaseInProgress reported a rebase in a clean checkout")
	}
	if files, err := ConflictedFiles(work); err != nil || len(files) != 0 {
		t.Errorf("ConflictedFiles = %v, %v; want none", files, err)
	}
}
