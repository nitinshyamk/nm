package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/gitx"
)

// writeDef writes a task definition to a temporary file and returns its path.
func writeDef(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	body := `{"id":"01-a","predecessors":[],"repositories":["nm"],` +
		`"description":"Do the thing.","acceptance-criteria":["It is done."]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The existing behavior of `nm task new` must be untouched when none of the new
// options are given: no escalations directory, no task file, no workplan link.
func TestCreateWithoutTheNewOptionsIsUnchanged(t *testing.T) {
	cfg := env(t, "nm")

	task, err := Create(cfg, Options{Name: "plain", Repos: []string{"nm"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := os.Stat(task.Escalations(cfg)); !os.IsNotExist(err) {
		t.Errorf("a task with no definition grew an escalations directory: %v", err)
	}
	if task.TaskFile != "" || task.Workplan != "" || task.TaskID != "" {
		t.Errorf("a plain task recorded workplan fields: %+v", task)
	}
	if task.HasEscalations {
		t.Error("HasEscalations is set on a task with no definition")
	}
	// The three original directories are still there.
	for _, dir := range []string{task.Artifacts(cfg), task.Input(cfg), task.Scratch(cfg)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s is missing: %v", dir, err)
		}
	}
}

// A repository list is still required without a definition: an empty one there
// is the signature of a command line that lost its arguments.
func TestCreateStillNeedsARepositoryWithoutADefinition(t *testing.T) {
	cfg := env(t)

	if _, err := Create(cfg, Options{Name: "empty"}); err == nil {
		t.Fatal("Create accepted a task with no repositories and no definition")
	}
}

// With a definition, zero repositories is a real case: work that entails no code
// change, such as a clarification to record.
func TestCreateWithNoRepositories(t *testing.T) {
	cfg := env(t)
	def := writeDef(t, "01-a.json")

	task, err := Create(cfg, Options{Name: "01-a", TaskFile: def})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(task.Repos) != 0 {
		t.Errorf("Repos = %v, want none", task.Repos)
	}
	if len(task.Dirs()) != 0 {
		t.Errorf("Dirs = %v, want none", task.Dirs())
	}
	// It is a task like any other, so it must be discoverable by List.
	tasks, err := List(cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Label() != task.Label() {
		t.Errorf("List returned %d tasks, want the one just created", len(tasks))
	}
	// And Load must read it back with its fields intact.
	back, err := Load(task.Dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.TaskFile != "01-a.json" || !back.HasEscalations {
		t.Errorf("reloaded task lost its definition fields: %+v", back)
	}
}

func TestCreateCopiesTheTaskFileAndArtifacts(t *testing.T) {
	cfg := env(t, "nm")
	def := writeDef(t, "01-a.json")

	artifacts := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifacts, "design.md"), []byte("the design"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(artifacts, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "sub", "more.md"), []byte("more"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := Create(cfg, Options{
		Name:      "01-a",
		Repos:     []string{"nm"},
		TaskFile:  def,
		Artifacts: artifacts,
		Workplan:  "build-the-thing",
		TaskID:    "01-a",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The definition lands in input/, beside the prompt.
	if got, err := os.ReadFile(filepath.Join(task.Input(cfg), "01-a.json")); err != nil {
		t.Errorf("the definition was not copied: %v", err)
	} else if !strings.Contains(string(got), "01-a") {
		t.Errorf("the copied definition is wrong: %s", got)
	}
	// Artifacts land as contents, not under an extra level.
	for _, rel := range []string{"design.md", filepath.Join("sub", "more.md")} {
		if _, err := os.Stat(filepath.Join(task.Artifacts(cfg), rel)); err != nil {
			t.Errorf("artifact %s was not copied: %v", rel, err)
		}
	}
	// A definition means somewhere to escalate to.
	if info, err := os.Stat(task.Escalations(cfg)); err != nil || !info.IsDir() {
		t.Errorf("escalations/ is missing: %v", err)
	}
	if task.Workplan != "build-the-thing" || task.TaskID != "01-a" {
		t.Errorf("the workplan link was not recorded: %+v", task)
	}
}

// A failure to copy must leave nothing behind: an agent started against half its
// context is worse than a task that was never created.
func TestCreateUnwindsWhenACopyFails(t *testing.T) {
	cfg := env(t, "nm")
	def := writeDef(t, "01-a.json")

	_, err := Create(cfg, Options{
		Name:      "01-a",
		Repos:     []string{"nm"},
		TaskFile:  def,
		Artifacts: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil {
		t.Fatal("Create succeeded with unreadable artifacts")
	}

	// No task directory, and — the part the existing unwind already handled but
	// which the new copy step must not break — no leftover branch.
	entries, readErr := os.ReadDir(cfg.Tasks())
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed Create left %d directories behind", len(entries))
	}
	if gitx.BranchExists(cfg.RepoPath("nm"), "01-a-"+Hash(cfg.HashLength, "01-a", []string{"nm"})) {
		t.Error("a failed Create left its branch behind")
	}
}

// A failure in the second repository must unwind the first one's worktree and
// branch.
//
// This was broken before the copy steps above existed and was reachable without
// them: every failure returns `Task{}, err`, which zeroes the named return before
// the deferred unwind reads it, so the unwind found no worktrees to remove. The
// leftover branch then made retrying the same task name fail with "branch already
// exists" — a task that could never be created again.
func TestCreateUnwindsTheFirstRepoWhenASecondFails(t *testing.T) {
	cfg := env(t, "aa", "zz")
	branch := "late-" + Hash(cfg.HashLength, "late", []string{"aa", "zz"})
	// Pre-create the branch in zz only, so aa's worktree is built and zz's is
	// refused.
	git(t, cfg.RepoPath("zz"), "branch", branch)

	if _, err := Create(cfg, Options{Name: "late", Repos: []string{"aa", "zz"}}); err == nil {
		t.Fatal("Create succeeded despite a pre-existing branch in the second repository")
	}

	if gitx.BranchExists(cfg.RepoPath("aa"), branch) {
		t.Error("the first repository's branch survived a failure in the second")
	}
	// A prunable worktree record is what holds the branch, so it is checked too.
	if out, err := gitx.Run(cfg.RepoPath("aa"), "worktree", "list"); err != nil {
		t.Fatal(err)
	} else if strings.Contains(out, "prunable") {
		t.Errorf("a prunable worktree record was left behind:\n%s", out)
	}

	// And the whole point: the same task can be created again once the
	// obstruction is gone.
	git(t, cfg.RepoPath("zz"), "branch", "-D", branch)
	if _, err := Create(cfg, Options{Name: "late", Repos: []string{"aa", "zz"}}); err != nil {
		t.Errorf("the task could not be created after a failed attempt: %v", err)
	}
}

// The base override is what makes stacked work possible: a successor has to
// branch from its predecessor's branch, not from the remote's default.
func TestCreateBranchesFromAnExplicitBase(t *testing.T) {
	cfg := env(t, "nm")
	source := cfg.RepoPath("nm")

	// A commit that is not on main, standing in for a predecessor's branch.
	git(t, source, "checkout", "--quiet", "-b", "predecessor")
	if err := os.WriteFile(filepath.Join(source, "pred.txt"), []byte("from the predecessor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", "-A")
	git(t, source, "commit", "-m", "predecessor work")
	head := git(t, source, "rev-parse", "HEAD")
	git(t, source, "checkout", "--quiet", "main")

	task, err := Create(cfg, Options{
		Name:  "02-b",
		Repos: []string{"nm"},
		Base:  &gitx.Base{Branch: "predecessor", Commit: head, Source: "predecessor"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	repo := task.Repos[0]
	if repo.BaseCommit != head {
		t.Errorf("BaseCommit = %q, want the predecessor's head %q", repo.BaseCommit, head)
	}
	if repo.BaseBranch != "predecessor" {
		t.Errorf("BaseBranch = %q, want predecessor", repo.BaseBranch)
	}
	// The worktree must actually contain the predecessor's work, which is the
	// whole point: branching from main would silently lose it.
	if _, err := os.Stat(filepath.Join(repo.Dir, "pred.txt")); err != nil {
		t.Errorf("the worktree does not contain the predecessor's commit: %v", err)
	}
	if !gitx.IsAncestor(repo.Dir, head) {
		t.Errorf("%s is not a descendant of the base commit", repo.Dir)
	}
}

// An explicit base must not consult the remote at all — that is what lets a
// successor start before its predecessor is merged.
func TestCreateWithAnExplicitBaseDoesNotNeedTheRemote(t *testing.T) {
	cfg := env(t, "nm")
	source := cfg.RepoPath("nm")
	head := git(t, source, "rev-parse", "HEAD")
	// Point origin at nothing, so any attempt to reach it fails.
	git(t, source, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	_, err := Create(cfg, Options{
		Name:  "02-b",
		Repos: []string{"nm"},
		Base:  &gitx.Base{Branch: "main", Commit: head, Source: "test"},
	})
	if err != nil {
		t.Fatalf("Create with an explicit base reached for the remote: %v", err)
	}
}

func TestTaskFilePrompt(t *testing.T) {
	cfg := env(t)
	task := Task{Name: "01-a", Hash: "abc123", TaskFile: "01-a.json"}

	got := TaskFilePrompt(cfg, task)
	want := "/nm-task-execute the task in input/01-a.json"
	if got != want {
		t.Errorf("TaskFilePrompt = %q, want %q", got, want)
	}
	// Forward slashes even on Windows: the prompt is read by an agent, and a
	// backslash in it would be an escape rather than a separator.
	if strings.Contains(got, `\`) {
		t.Errorf("TaskFilePrompt used a backslash: %q", got)
	}
}

func TestReadyAndRefiningPaths(t *testing.T) {
	cfg := env(t)
	task := Task{Dir: filepath.Join("tasks", "01-a-abc123")}

	if got, want := task.Ready(cfg), filepath.Join(task.Dir, "escalations", ReadyFile); got != want {
		t.Errorf("Ready = %q, want %q", got, want)
	}
	if got, want := task.Refining(cfg), filepath.Join(task.Dir, "escalations", RefiningFile); got != want {
		t.Errorf("Refining = %q, want %q", got, want)
	}
}

// makeDirs runs again on every save, so a task whose escalations directory was
// deleted grows it back rather than staying half-shaped.
func TestSavePromptRestoresTheEscalationsDirectory(t *testing.T) {
	cfg := env(t)
	def := writeDef(t, "01-a.json")

	task, err := Create(cfg, Options{Name: "01-a", TaskFile: def})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.RemoveAll(task.Escalations(cfg)); err != nil {
		t.Fatal(err)
	}

	task.Prompt = "something"
	if err := task.SavePrompt(cfg); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if info, err := os.Stat(task.Escalations(cfg)); err != nil || !info.IsDir() {
		t.Errorf("SavePrompt did not restore escalations/: %v", err)
	}
}
