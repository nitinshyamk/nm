package gitx

import (
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
