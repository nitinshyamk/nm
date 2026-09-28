package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefNames(t *testing.T) {
	out := strings.Join([]string{"HEAD", "main", "feature/auth", "", "  release  "}, "\n")
	got := ParseRefNames(out)
	want := []string{"main", "feature/auth", "release"}
	if len(got) != len(want) {
		t.Fatalf("ParseRefNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseRefNames = %v, want %v", got, want)
		}
	}
}

func TestRemoteBranchesListsWhatHasBeenFetched(t *testing.T) {
	isolate(t)
	work := clone(t, newRemote(t, "main"))

	git(t, work, "checkout", "--quiet", "-b", "feature/auth")
	writeFile(t, work, "a.txt", "one\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "one")

	// Only what the remote has, and never the origin/HEAD pointer.
	if got := RemoteBranches(work, "origin"); len(got) != 1 || got[0] != "main" {
		t.Errorf("RemoteBranches before the push = %v, want [main]", got)
	}
	git(t, work, "push", "--quiet", "-u", "origin", "feature/auth")
	got := RemoteBranches(work, "origin")
	if strings.Join(got, ",") != "feature/auth,main" {
		t.Errorf("RemoteBranches = %v, want feature/auth and main", got)
	}
}

// A commit from one repository must not read as present in another. This is the check
// that turns "fatal: invalid reference: <sha>" into an error naming both.
func TestCommitExistsIsPerRepository(t *testing.T) {
	isolate(t)
	one := clone(t, newRemote(t, "main"))
	two := clone(t, newRemote(t, "main"))

	writeFile(t, one, "only-here.txt", "one\n")
	git(t, one, "add", "-A")
	git(t, one, "commit", "--quiet", "-m", "only in one")
	commit := git(t, one, "rev-parse", "HEAD")

	if !CommitExists(one, commit) {
		t.Error("CommitExists = false for a commit in its own repository")
	}
	if CommitExists(two, commit) {
		t.Error("CommitExists = true for a commit from another repository")
	}
	if CommitExists(one, "0000000000000000000000000000000000000000") {
		t.Error("CommitExists = true for a commit that is nowhere")
	}
}

// RefsContaining is how nm tells a leftover branch that holds work from one that holds
// none, because `git branch -d` answers a narrower question and refuses both.
func TestRefsContainingSeesWhatElseHoldsACommit(t *testing.T) {
	isolate(t)
	work := clone(t, newRemote(t, "main"))

	// A branch cut from main and never committed to: debris. main still holds its tip.
	git(t, work, "branch", "fresh")
	tip := git(t, work, "rev-parse", "refs/heads/fresh")
	others := RefsContaining(work, tip, "fresh")
	if len(others) == 0 {
		t.Errorf("RefsContaining = %v, want main among them: a branch cut from main holds no work", others)
	}
	for _, ref := range others {
		if ref == "refs/heads/fresh" {
			t.Error("RefsContaining included the branch being asked about, which always contains its own tip")
		}
	}

	// A branch with a commit of its own is contained by nothing else.
	git(t, work, "checkout", "--quiet", "-b", "has-work")
	writeFile(t, work, "work.txt", "unique\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "work nobody else has")
	unique := git(t, work, "rev-parse", "refs/heads/has-work")
	if got := RefsContaining(work, unique, "has-work"); len(got) != 0 {
		t.Errorf("RefsContaining = %v, want nothing: this commit exists only on has-work", got)
	}
}

// The 260-character path limit on a default Windows git install is a setting, not a
// broken repository, and the error has to say so — the raw failure is indistinguishable
// from a real one, which is what masked the cross-repo base bug for a while.
func TestAddWorktreeExplainsTheWindowsPathLimit(t *testing.T) {
	err := explainLongPaths(&CmdError{
		Args:   []string{"worktree", "add"},
		Stderr: "error: unable to create file some/deeply/nested/path: Filename too long",
	})
	if err == nil {
		t.Fatal("explainLongPaths dropped the error")
	}
	for _, want := range []string{"core.longpaths", "Filename too long"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	// Anything else passes through unchanged, so a real failure is not dressed up as
	// a path-length problem.
	other := &CmdError{Args: []string{"worktree", "add"}, Stderr: "fatal: invalid reference: abc123"}
	if got := explainLongPaths(other); !errors.Is(got, other) || got.Error() != other.Error() {
		t.Errorf("explainLongPaths rewrote an unrelated error: %v", got)
	}
	if explainLongPaths(nil) != nil {
		t.Error("explainLongPaths invented an error out of nil")
	}
}

func TestRemoteBranchesIsSilentOnANonRepository(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RemoteBranches(dir, "origin"); got != nil {
		t.Errorf("RemoteBranches = %v, want nothing: completion must not report errors", got)
	}
}
