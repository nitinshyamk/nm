package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "nm test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "nm test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	for _, key := range []string{
		"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_PREFIX", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// env builds a projects root holding one repository with a remote, plus an
// empty worktrees root, and returns a config pointing at both.
func env(t *testing.T, repos ...string) config.Config {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, repo := range repos {
		bare := filepath.Join(root, repo+".git")
		git(t, root, "init", "--bare", "-b", "main", bare)
		dir := filepath.Join(projects, repo)
		git(t, projects, "init", "-b", "main", dir)
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(repo+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-m", "init")
		git(t, dir, "remote", "add", "origin", bare)
		git(t, dir, "push", "--quiet", "-u", "origin", "main")
	}

	cfg := config.Defaults()
	cfg.ProjectsRoot = projects
	cfg.WorktreesRoot = filepath.Join(root, "worktrees")
	cfg.TasksRoot = filepath.Join(root, "tasks")
	return cfg
}

func TestCreateNamed(t *testing.T) {
	cfg := env(t, "nm")

	w, err := Create(cfg, Options{Repo: "nm", Name: "fix-tui"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantDir := filepath.Join(cfg.Worktrees(), "nm-id-fix-tui-"+w.Hash)
	if w.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", w.Dir, wantDir)
	}
	if w.Branch != "fix-tui-"+w.Hash {
		t.Errorf("Branch = %q, want fix-tui-%s", w.Branch, w.Hash)
	}
	if len(w.Hash) != cfg.HashLength {
		t.Errorf("hash %q is %d characters, want %d", w.Hash, len(w.Hash), cfg.HashLength)
	}
	if !gitx.IsRepo(w.Dir) {
		t.Error("the created directory is not a git worktree")
	}
	if branch, _ := gitx.CurrentBranch(w.Dir); branch != w.Branch {
		t.Errorf("worktree is on branch %q, want %q", branch, w.Branch)
	}
}

func TestCreateNamedIsDeterministicAndRefusesDuplicates(t *testing.T) {
	cfg := env(t, "nm")

	first, err := Create(cfg, Options{Repo: "nm", Name: "fix-tui"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = Create(cfg, Options{Repo: "nm", Name: "fix-tui"})
	if err == nil {
		t.Fatal("creating the same named worktree twice should report the existing one")
	}
	if !strings.Contains(err.Error(), first.Hash) {
		t.Errorf("error %q should name the existing worktree", err)
	}

	// The same inputs in a fresh tree produce the same hash.
	other := env(t, "nm")
	again, err := Create(other, Options{Repo: "nm", Name: "fix-tui"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if again.Hash != first.Hash {
		t.Errorf("hash %q differs from %q for identical inputs", again.Hash, first.Hash)
	}
}

func TestCreateUnnamedUsesTheDateAndStaysUnique(t *testing.T) {
	cfg := env(t, "nm")
	day := time.Date(2026, 9, 19, 14, 32, 0, 0, time.UTC)

	calls := 0
	clock := func() time.Time {
		calls++
		return day.Add(time.Duration(calls) * time.Millisecond)
	}

	first, err := Create(cfg, Options{Repo: "nm", Now: clock})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := Create(cfg, Options{Repo: "nm", Now: clock})
	if err != nil {
		t.Fatalf("second Create on the same day: %v", err)
	}

	if first.Name != "2026-09-19" {
		t.Errorf("Name = %q, want the date", first.Name)
	}
	if !strings.HasPrefix(filepath.Base(first.Dir), "nm-id-2026-09-19-") {
		t.Errorf("directory %q does not follow <repo>-id-<date>-<hash>", filepath.Base(first.Dir))
	}
	if first.Hash == second.Hash {
		t.Error("two unnamed worktrees on the same day collided")
	}
	if first.Branch == second.Branch {
		t.Error("two unnamed worktrees on the same day share a branch")
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	cfg := env(t, "nm")

	if _, err := Create(cfg, Options{Repo: "nope", Name: "x"}); err == nil {
		t.Error("Create accepted a repository that does not exist")
	}
	if _, err := Create(cfg, Options{Repo: "nm", Name: "bad name"}); err == nil {
		t.Error("Create accepted a name with a space")
	}
	if _, err := Create(cfg, Options{Repo: "nm", Name: "a-id-b"}); err == nil {
		t.Error("Create accepted a name containing the -id- separator")
	}
}

func TestCreateBranchesFromTheRemoteTip(t *testing.T) {
	cfg := env(t, "nm")
	repoDir := cfg.RepoPath("nm")

	// Push a commit through a second clone so the source repository's
	// origin/main is stale.
	other := filepath.Join(t.TempDir(), "other")
	remote := git(t, repoDir, "remote", "get-url", "origin")
	git(t, t.TempDir(), "clone", "--quiet", remote, other)
	if err := os.WriteFile(filepath.Join(other, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "elsewhere")
	git(t, other, "push", "--quiet", "origin", "main")
	want := git(t, other, "rev-parse", "HEAD")

	w, err := Create(cfg, Options{Repo: "nm", Name: "fresh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := git(t, w.Dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("new worktree is at %s, want the freshly pushed %s", got, want)
	}
}

func TestListAndDelete(t *testing.T) {
	cfg := env(t, "nm", "site")

	a, err := Create(cfg, Options{Repo: "nm", Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(cfg, Options{Repo: "site", Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := List(cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List returned %d worktrees, want 2", len(list))
	}

	byBranch := map[string]Worktree{}
	for _, w := range list {
		if w.Err != nil {
			t.Errorf("status error for %s: %v", w.Dir, w.Err)
		}
		byBranch[w.Branch] = w
	}
	alpha, ok := byBranch[a.Branch]
	if !ok {
		t.Fatalf("List did not return %s: %+v", a.Branch, byBranch)
	}
	if alpha.Repo != "nm" {
		t.Errorf("Repo = %q, want nm", alpha.Repo)
	}
	if !alpha.Status.Dirty() || alpha.Status.Untracked != 1 {
		t.Errorf("alpha status = %+v, want one untracked file", alpha.Status)
	}

	// A dirty worktree needs force; a clean one does not.
	if _, err := Delete(alpha, false); err == nil {
		t.Error("Delete removed a dirty worktree without force")
	}
	if _, err := Delete(alpha, true); err != nil {
		t.Fatalf("forced Delete: %v", err)
	}

	after, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("after deleting one worktree List returned %d, want 1", len(after))
	}
	if gitx.BranchExists(cfg.RepoPath("nm"), a.Branch) {
		t.Error("deleting a clean-history worktree should also remove its branch")
	}
}

func TestDeleteKeepsBranchWithUnmergedCommits(t *testing.T) {
	cfg := env(t, "nm")
	w, err := Create(cfg, Options{Repo: "nm", Name: "keeper"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, w.Dir, "add", "-A")
	git(t, w.Dir, "commit", "-m", "unmerged work")

	note, err := Delete(w, true)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if note == "" {
		t.Error("Delete should report that it kept the branch")
	}
	if !gitx.BranchExists(cfg.RepoPath("nm"), w.Branch) {
		t.Error("Delete discarded a branch holding commits that exist nowhere else")
	}
}

func TestDirNameRoundTrip(t *testing.T) {
	dir := DirName("nm", "fix-tui", "7b21e4")
	if dir != "nm-id-fix-tui-7b21e4" {
		t.Fatalf("DirName = %q", dir)
	}
	repo, label, ok := ParseDirName(dir)
	if !ok || repo != "nm" || label != "fix-tui-7b21e4" {
		t.Errorf("ParseDirName(%q) = %q, %q, %v", dir, repo, label, ok)
	}
	// A repository whose own name contains a dash still splits correctly.
	repo, label, ok = ParseDirName(DirName("home-management-system", "2026-09-19", "a3f9c2"))
	if !ok || repo != "home-management-system" || label != "2026-09-19-a3f9c2" {
		t.Errorf("ParseDirName with a dashed repo = %q, %q, %v", repo, label, ok)
	}
	if _, _, ok := ParseDirName("some-random-directory"); ok {
		t.Error("ParseDirName accepted a directory nm did not create")
	}
}
