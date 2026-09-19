package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate keeps tests away from the developer's global git configuration and
// identity, so results do not depend on the machine running them.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "nm test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "nm test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")

	// git hooks export plumbing variables that would point every command in
	// these tests back at the repository being committed to. Clear them so the
	// suite behaves the same standalone and under `git commit`.
	for _, key := range []string{
		"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_PREFIX", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	} {
		t.Setenv(key, "") // registers the restore
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Run(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return out
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRemote builds a bare repository with one commit on defaultBranch and
// returns its path.
func newRemote(t *testing.T, defaultBranch string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")

	git(t, root, "init", "--bare", "-b", defaultBranch, bare)
	git(t, root, "init", "-b", defaultBranch, seed)
	writeFile(t, seed, "README.md", "seed\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-m", "seed")
	git(t, seed, "remote", "add", "origin", bare)
	git(t, seed, "push", "-u", "origin", defaultBranch)
	return bare
}

func clone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	git(t, t.TempDir(), "clone", "--quiet", remote, dir)
	return dir
}

func TestResolveBaseUsesTheRemotesCurrentTip(t *testing.T) {
	isolate(t)
	for _, branch := range []string{"main", "master"} {
		t.Run(branch, func(t *testing.T) {
			remote := newRemote(t, branch)
			local := clone(t, remote)

			// Someone else pushes. The local clone knows nothing about it.
			other := clone(t, remote)
			writeFile(t, other, "new.txt", "from elsewhere\n")
			git(t, other, "add", "-A")
			git(t, other, "commit", "-m", "elsewhere")
			git(t, other, "push", "--quiet", "origin", branch)
			want := git(t, other, "rev-parse", "HEAD")

			stale := git(t, local, "rev-parse", "origin/"+branch)
			if stale == want {
				t.Fatal("test setup failed: the local clone is not actually stale")
			}

			base, err := ResolveBase(local, "", false)
			if err != nil {
				t.Fatalf("ResolveBase: %v", err)
			}
			if base.Branch != branch {
				t.Errorf("Branch = %q, want %q", base.Branch, branch)
			}
			if base.Commit != want {
				t.Errorf("Commit = %s, want the freshly pushed %s", base.Commit, want)
			}
		})
	}
}

func TestResolveBaseOverrideAndOffline(t *testing.T) {
	isolate(t)
	remote := newRemote(t, "main")
	local := clone(t, remote)

	git(t, local, "checkout", "--quiet", "-b", "side")
	writeFile(t, local, "side.txt", "side\n")
	git(t, local, "add", "-A")
	git(t, local, "commit", "-m", "side")
	git(t, local, "push", "--quiet", "-u", "origin", "side")

	base, err := ResolveBase(local, "side", false)
	if err != nil {
		t.Fatalf("ResolveBase with override: %v", err)
	}
	if base.Branch != "side" {
		t.Errorf("Branch = %q, want side", base.Branch)
	}

	// With the remote gone, the default is an error naming --offline...
	git(t, local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if _, err := ResolveBase(local, "", false); !errors.Is(err, ErrRemoteUnreachable) {
		t.Errorf("unreachable origin gave %v, want ErrRemoteUnreachable", err)
	}
	// ...and --offline falls back to local knowledge rather than failing.
	off, err := ResolveBase(local, "", true)
	if err != nil {
		t.Fatalf("offline ResolveBase: %v", err)
	}
	if off.Branch != "main" {
		t.Errorf("offline Branch = %q, want main from the local origin/HEAD", off.Branch)
	}
	if !strings.Contains(off.Source, "offline") {
		t.Errorf("offline Source = %q, want it to say where the base came from", off.Source)
	}
}

func TestResolveBaseWithoutRemote(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	git(t, dir, "init", "-b", "trunk", ".")
	writeFile(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "first")

	base, err := ResolveBase(dir, "", false)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if base.Branch != "trunk" {
		t.Errorf("Branch = %q, want trunk", base.Branch)
	}
	if !strings.Contains(base.Source, "no origin remote") {
		t.Errorf("Source = %q, want it to explain there is no remote", base.Source)
	}
}

func TestWorktreeLifecycleAndStatus(t *testing.T) {
	isolate(t)
	remote := newRemote(t, "main")
	local := clone(t, remote)
	wt := filepath.Join(t.TempDir(), "wt")

	base, err := ResolveBase(local, "", false)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if err := AddWorktree(local, wt, "feature-abc123", base.Commit); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if !IsRepo(wt) {
		t.Fatal("the new worktree is not a git working tree")
	}
	if main, err := MainWorktree(wt); err != nil || main != local {
		t.Errorf("MainWorktree = %q, %v; want %q", main, err, local)
	}

	clean, err := GetStatus(wt)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if clean.Dirty() {
		t.Errorf("a fresh worktree reports dirty: %+v", clean)
	}
	if clean.Branch != "feature-abc123" {
		t.Errorf("Branch = %q, want feature-abc123", clean.Branch)
	}

	writeFile(t, wt, "committed.txt", "committed\n")
	git(t, wt, "add", "committed.txt")
	git(t, wt, "commit", "-m", "local only")

	// Staged after the commit, so it stays in the index rather than being
	// swept into it.
	writeFile(t, wt, "README.md", "modified\n")
	writeFile(t, wt, "untracked.txt", "new\n")
	writeFile(t, wt, "staged.txt", "staged\n")
	git(t, wt, "add", "staged.txt")

	dirty, err := GetStatus(wt)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if dirty.Staged != 1 || dirty.Unstaged != 1 || dirty.Untracked != 1 || dirty.Unpushed != 1 {
		t.Errorf("status = %+v; want 1 each of staged/unstaged/untracked/unpushed", dirty)
	}
	if dirty.HasUpstream {
		t.Error("a branch that was never pushed should not report an upstream")
	}
	if len(dirty.Hazards()) != 4 {
		t.Errorf("Hazards() = %v, want one entry per category", dirty.Hazards())
	}

	// git refuses to drop a worktree holding uncommitted work unless forced.
	if err := RemoveWorktree(local, wt, false); err == nil {
		t.Error("RemoveWorktree removed a dirty worktree without force")
	}
	if err := RemoveWorktree(local, wt, true); err != nil {
		t.Fatalf("forced RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("the worktree directory is still present after removal")
	}
	if !BranchExists(local, "feature-abc123") {
		t.Fatal("removing a worktree should not delete its branch")
	}
	if err := DeleteBranch(local, "feature-abc123", true); err != nil {
		t.Errorf("DeleteBranch: %v", err)
	}
}

func TestParseSymrefHead(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{
			name: "main",
			in:   "ref: refs/heads/main\tHEAD\n5f2a…\tHEAD",
			want: "main",
		},
		{
			name: "master",
			in:   "ref: refs/heads/master\tHEAD\n5f2a…\tHEAD",
			want: "master",
		},
		{
			name: "slashes in branch name",
			in:   "ref: refs/heads/release/2.0\tHEAD",
			want: "release/2.0",
		},
		{name: "no symref", in: "5f2a…\tHEAD", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSymrefHead(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParsePorcelainZ(t *testing.T) {
	cases := []struct {
		name                        string
		in                          string
		staged, unstaged, untracked int
	}{
		{name: "empty"},
		{name: "untracked", in: "?? new.txt\x00", untracked: 1},
		{name: "modified", in: " M a.txt\x00", unstaged: 1},
		{name: "staged", in: "A  a.txt\x00", staged: 1},
		{name: "staged and modified", in: "MM a.txt\x00", staged: 1, unstaged: 1},
		{
			name: "rename consumes its source path",
			in:   "R  new.txt\x00old.txt\x00?? other.txt\x00",
			// old.txt is the rename's source record, not a second entry.
			staged: 1, untracked: 1,
		},
		{
			name:   "mixed",
			in:     "A  a.txt\x00 M b.txt\x00?? c.txt\x00?? d.txt\x00",
			staged: 1, unstaged: 1, untracked: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			staged, unstaged, untracked := ParsePorcelainZ(tc.in)
			if staged != tc.staged || unstaged != tc.unstaged || untracked != tc.untracked {
				t.Errorf("got staged=%d unstaged=%d untracked=%d; want %d/%d/%d",
					staged, unstaged, untracked, tc.staged, tc.unstaged, tc.untracked)
			}
		})
	}
}

func TestParseWorktreeList(t *testing.T) {
	in := "worktree /home/u/projects/nm\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /home/u/projects/worktrees/nm-id-x-123456\nHEAD def\nbranch refs/heads/x-123456\n\n" +
		"worktree /home/u/detached\nHEAD 999\ndetached\n"
	got := ParseWorktreeList(in)
	if len(got) != 3 {
		t.Fatalf("parsed %d worktrees, want 3: %+v", len(got), got)
	}
	if got[1].Path != "/home/u/projects/worktrees/nm-id-x-123456" || got[1].Branch != "x-123456" {
		t.Errorf("second entry = %+v", got[1])
	}
	if got[2].Branch != "" {
		t.Errorf("detached entry should have no branch, got %q", got[2].Branch)
	}
}

func TestHazardsWording(t *testing.T) {
	s := Status{Staged: 1, Unstaged: 2, Untracked: 1, Unpushed: 3}
	got := strings.Join(s.Hazards(), ", ")
	want := "1 change staged, 2 files modified, 1 file untracked, 3 commits not on any remote"
	if got != want {
		t.Errorf("Hazards() = %q, want %q", got, want)
	}
}

func TestResolveBaseWithEmptyRemote(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	local := filepath.Join(root, "work")
	git(t, root, "init", "--bare", "-b", "main", bare)
	git(t, root, "init", "-b", "main", local)
	writeFile(t, local, "a.txt", "a\n")
	git(t, local, "add", "-A")
	git(t, local, "commit", "-m", "first")
	git(t, local, "remote", "add", "origin", bare)

	// A remote that exists but has never been pushed to has no HEAD to report.
	// That is not an error: there is nothing stale to protect against.
	base, err := ResolveBase(local, "", false)
	if err != nil {
		t.Fatalf("ResolveBase against an empty remote: %v", err)
	}
	if base.Branch != "main" {
		t.Errorf("Branch = %q, want main", base.Branch)
	}
	if !strings.Contains(base.Source, "no branches yet") {
		t.Errorf("Source = %q, want it to explain the remote is empty", base.Source)
	}
}
