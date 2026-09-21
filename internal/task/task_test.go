package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCreateBuildsTheWholeTask(t *testing.T) {
	cfg := env(t, "nm", "site")

	task, err := Create(cfg, Options{Name: "auth", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if filepath.Base(task.Dir) != "auth-"+task.Hash {
		t.Errorf("task directory is %q, want auth-%s", filepath.Base(task.Dir), task.Hash)
	}
	if len(task.Repos) != 2 {
		t.Fatalf("task has %d repos, want 2", len(task.Repos))
	}
	for _, r := range task.Repos {
		if filepath.Base(r.Dir) != r.Name+"-"+task.Hash {
			t.Errorf("worktree directory is %q, want %s-%s", filepath.Base(r.Dir), r.Name, task.Hash)
		}
		if !gitx.IsRepo(r.Dir) {
			t.Errorf("%s is not a git worktree", r.Dir)
		}
		if r.Branch != "auth-"+task.Hash {
			t.Errorf("branch = %q, want auth-%s", r.Branch, task.Hash)
		}
		if r.BaseBranch != "main" || r.BaseCommit == "" {
			t.Errorf("base was not recorded: %+v", r)
		}
	}

	artifacts := task.Artifacts(cfg)
	for _, dir := range []string{artifacts, task.Input(cfg), task.Scratch(cfg)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s is missing: %v", filepath.Base(dir), err)
		}
	}
	if n, err := CountArtifacts(artifacts); err != nil || n != 0 {
		t.Errorf("a new task has %d artifacts, want 0 (%v)", n, err)
	}

	// The record round-trips, including the base each worktree came from.
	loaded, err := Load(task.Dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != "auth" || len(loaded.Repos) != 2 || loaded.Repos[0].BaseCommit == "" {
		t.Errorf("loaded task does not match what was created: %+v", loaded)
	}
}

func TestHashIsOrderIndependent(t *testing.T) {
	a := Hash(6, "auth", []string{"nm", "site"})
	b := Hash(6, "auth", []string{"site", "nm"})
	if a != b {
		t.Errorf("repo order changed the hash: %q vs %q", a, b)
	}
	if Hash(6, "auth", []string{"nm"}) == a {
		t.Error("a different set of repos produced the same hash")
	}
	if Hash(6, "other", []string{"nm", "site"}) == a {
		t.Error("a different name produced the same hash")
	}
}

func TestCreateIsTransactional(t *testing.T) {
	cfg := env(t, "nm")

	// The second repository does not exist, so nothing should be left behind.
	_, err := Create(cfg, Options{Name: "broken", Repos: []string{"nm", "ghost"}})
	if err == nil {
		t.Fatal("Create accepted a repository that does not exist")
	}
	entries, readErr := os.ReadDir(cfg.Tasks())
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed Create left %d directories behind: %v", len(entries), entries)
	}
	if gitx.BranchExists(cfg.RepoPath("nm"), "broken-"+Hash(cfg.HashLength, "broken", []string{"nm", "ghost"})) {
		t.Error("a failed Create left a branch behind")
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	cfg := env(t, "nm")

	if _, err := Create(cfg, Options{Name: "x", Repos: nil}); err == nil {
		t.Error("Create accepted a task with no repositories")
	}
	if _, err := Create(cfg, Options{Name: "bad name", Repos: []string{"nm"}}); err == nil {
		t.Error("Create accepted an invalid task name")
	}
	if _, err := Create(cfg, Options{Name: "dup", Repos: []string{"nm", "nm"}}); err == nil {
		t.Error("Create accepted the same repository twice")
	}
	if _, err := Create(cfg, Options{Name: "once", Repos: []string{"nm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(cfg, Options{Name: "once", Repos: []string{"nm"}}); err == nil {
		t.Error("Create built a second task with the same name and repos")
	}
}

func TestSavePromptWritesTheInputPrompt(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "auth", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(task.Input(cfg), PromptFile)

	// A task with no prompt — no agent was started — leaves input/ empty.
	if err := task.SavePrompt(cfg); err != nil {
		t.Fatalf("SavePrompt with no prompt: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("SavePrompt wrote %s for a task with no prompt (%v)", PromptFile, err)
	}

	task.Prompt = "  Refactor auth across both repos.  "
	if err := task.SavePrompt(cfg); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if got, want := string(blob), "Refactor auth across both repos.\n"; got != want {
		t.Errorf("prompt file holds %q, want %q", got, want)
	}

	// A task created before these directories existed grows them rather than
	// failing to write the prompt.
	for _, dir := range []string{task.Input(cfg), task.Scratch(cfg)} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := task.SavePrompt(cfg); err != nil {
		t.Fatalf("SavePrompt after the directories were removed: %v", err)
	}
	for _, dir := range []string{task.Input(cfg), task.Scratch(cfg)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("SavePrompt did not recreate %s: %v", filepath.Base(dir), err)
		}
	}
}

func TestSurveyReportsWorkAcrossRepos(t *testing.T) {
	cfg := env(t, "nm", "site")
	task, err := Create(cfg, Options{Name: "auth", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatal(err)
	}

	// Uncommitted work in one repo, a local commit in the other, and a file
	// in artifacts: three different reasons not to delete this task.
	if err := os.WriteFile(filepath.Join(task.Repos[0].Dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := task.Repos[1].Dir
	if err := os.WriteFile(filepath.Join(second, "work.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, second, "add", "-A")
	git(t, second, "commit", "-m", "local work")
	if err := os.WriteFile(filepath.Join(task.Artifacts(cfg), "plan.md"), []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	views, err := Survey(cfg)
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("Survey returned %d tasks, want 1", len(views))
	}
	v := views[0]
	if !v.Dirty() {
		t.Error("Survey reports a task with uncommitted work as clean")
	}
	if v.Artifacts != 1 {
		t.Errorf("Artifacts = %d, want 1", v.Artifacts)
	}
	if total := v.Totals(); total.Untracked != 1 || total.Unpushed != 1 {
		t.Errorf("Totals = %+v, want one untracked file and one unpushed commit", total)
	}

	hazards := strings.Join(v.Hazards(), " | ")
	for _, want := range []string{"nm:", "site:", "artifacts/ holds 1 file"} {
		if !strings.Contains(hazards, want) {
			t.Errorf("hazards %q do not mention %q", hazards, want)
		}
	}
}

func TestDeleteRemovesEverything(t *testing.T) {
	cfg := env(t, "nm", "site")
	task, err := Create(cfg, Options{Name: "auth", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.Repos[0].Dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Delete(task, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(task.Dir); !os.IsNotExist(err) {
		t.Error("the task directory survived deletion")
	}
	for _, r := range task.Repos {
		if gitx.BranchExists(r.Source, r.Branch) {
			t.Errorf("branch %s in %s survived deletion", r.Branch, r.Source)
		}
		list, err := gitx.ListWorktrees(r.Source)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range list {
			if w.Path == r.Dir {
				t.Errorf("%s is still registered as a worktree", r.Dir)
			}
		}
	}
}

func TestDeleteKeepsBranchesHoldingUniqueCommits(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "keep", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := task.Repos[0].Dir
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "unmerged")

	notes, err := Delete(task, true)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(notes) == 0 {
		t.Error("Delete did not report keeping the branch")
	}
	if !gitx.BranchExists(task.Repos[0].Source, task.Repos[0].Branch) {
		t.Error("Delete discarded commits that exist nowhere else")
	}
}
