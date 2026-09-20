package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/spf13/cobra"
)

// completionEnv points nm at a throwaway configuration and projects root, so
// completion tests never read the developer's own ~/.nm.json or ~/projects.
func completionEnv(t *testing.T, repos ...string) config.Config {
	t.Helper()
	isolateGit(t)
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	for _, repo := range repos {
		if err := os.MkdirAll(filepath.Join(projects, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Defaults()
	cfg.ProjectsRoot = projects
	cfg.WorktreesRoot = filepath.Join(projects, "worktrees")
	cfg.TasksRoot = filepath.Join(projects, "tasks")

	blob, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "nm.json")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvPath, path)
	return cfg
}

// isolateGit clears the plumbing variables a git hook exports. Without this,
// `git` run inside a temporary directory follows $GIT_DIR back to the
// repository being committed to, and a completion test quietly starts
// answering with this repository's own branches — but only when run from the
// pre-commit hook, which is the worst way to find out.
func isolateGit(t *testing.T) {
	t.Helper()
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

func TestCompleteOneRepoOffersTheProjectsRoot(t *testing.T) {
	completionEnv(t, "nm", "site")

	got, directive := completeOneRepo(nil, nil, "")
	if directive != noFiles {
		t.Errorf("directive = %v, want no file completion: a repo is a name, not a path", directive)
	}
	if strings.Join(got, ",") != "nm,site" {
		t.Errorf("completeOneRepo = %v, want nm and site", got)
	}

	// Once the one repository argument is filled, there is nothing more to say.
	if got, _ := completeOneRepo(nil, []string{"nm"}, ""); len(got) != 0 {
		t.Errorf("completeOneRepo after the argument = %v, want nothing", got)
	}
}

func TestCompleteRepoListDropsWhatIsAlreadyTyped(t *testing.T) {
	completionEnv(t, "nm", "site", "notes")

	got, _ := completeRepoList(nil, []string{"nm"}, "")
	for _, name := range got {
		if name == "nm" {
			t.Fatalf("completeRepoList offered nm twice: %v", got)
		}
	}
	if strings.Join(got, ",") != "notes,site" {
		t.Errorf("completeRepoList = %v, want notes and site", got)
	}

	if got, _ := completeRepoList(nil, []string{"nm"}, "s"); strings.Join(got, ",") != "site" {
		t.Errorf("completeRepoList with a prefix = %v, want site", got)
	}
}

func TestCompleteRebaseArgsWalksThePositions(t *testing.T) {
	completionEnv(t, "nm")

	if got, _ := completeRebaseArgs(nil, nil, ""); strings.Join(got, ",") != "nm" {
		t.Errorf("the first argument = %v, want the repositories", got)
	}
	// The second argument wants the branches that repository has on origin.
	cfg := completionEnvConfig(t)
	seedRemoteBranches(t, cfg.RepoPath("nm"), "main", "feature/auth")

	got, directive := completeRebaseArgs(nil, []string{"nm"}, "")
	if directive != noFiles {
		t.Errorf("directive = %v, want no file completion", directive)
	}
	if strings.Join(got, ",") != "feature/auth,main" {
		t.Errorf("branches = %v, want feature/auth and main", got)
	}
	if got, _ := completeRebaseArgs(nil, []string{"nm"}, "feat"); strings.Join(got, ",") != "feature/auth" {
		t.Errorf("branches with a prefix = %v, want feature/auth", got)
	}
	if got, _ := completeRebaseArgs(nil, []string{"nm", "feature/auth"}, ""); len(got) != 0 {
		t.Errorf("a third argument was offered completions: %v", got)
	}
}

// completionEnvConfig re-reads the configuration completionEnv wrote, so a
// test can reach the same paths nm will.
func completionEnvConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seedRemoteBranches turns a stub directory into a real repository holding
// the named remote-tracking branches, which is what branch completion reads.
func seedRemoteBranches(t *testing.T, dir string, branches ...string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := gitx.Run(dir, args...)
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return out
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "nm test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "nm test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")

	run("init", "--quiet", "-b", "main", ".")
	run("commit", "--quiet", "--allow-empty", "-m", "seed")
	head := run("rev-parse", "HEAD")
	for _, branch := range branches {
		run("update-ref", "refs/remotes/origin/"+branch, head)
	}
}

func TestCompleteConfigKeysShowTheirValues(t *testing.T) {
	completionEnv(t)

	got, directive := completeConfigKeys(nil, nil, "")
	if directive != noFiles {
		t.Errorf("directive = %v, want no file completion", directive)
	}
	if len(got) == 0 {
		t.Fatal("no configuration keys were offered")
	}

	// Sorted, and each carries the value it currently holds.
	if !sortedStrings(got) {
		t.Errorf("the keys came back unsorted: %v", got)
	}
	var found bool
	for _, entry := range got {
		if strings.HasPrefix(entry, "editor_command\t") {
			found = true
			if !strings.HasSuffix(entry, "\tcode") {
				t.Errorf("editor_command was offered as %q, without its value", entry)
			}
		}
	}
	if !found {
		t.Errorf("editor_command was not offered: %v", got)
	}

	// A prefix narrows it, and every key still accepted by `nm config get`.
	narrowed, _ := completeConfigKeys(nil, nil, "tasks_")
	if len(narrowed) != 1 || !strings.HasPrefix(narrowed[0], "tasks_root\t") {
		t.Errorf("completeConfigKeys(\"tasks_\") = %v, want tasks_root", narrowed)
	}
	for _, entry := range got {
		key, _, _ := strings.Cut(entry, "\t")
		if _, err := settingValue(config.Defaults(), key); err != nil {
			t.Errorf("completion offers %q but `nm config get` rejects it: %v", key, err)
		}
	}
}

func TestCompleteShellsMatchesTheInitCommand(t *testing.T) {
	got, directive := completeShells(nil, nil, "")
	if directive != noFiles {
		t.Errorf("directive = %v, want no file completion", directive)
	}
	if strings.Join(got, ",") != "bash,nu,zsh" {
		t.Errorf("completeShells = %v, want every shell nm can generate for", got)
	}
	if got, _ := completeShells(nil, nil, "z"); strings.Join(got, ",") != "zsh" {
		t.Errorf("completeShells(\"z\") = %v, want zsh", got)
	}
}

// TestEveryArgumentHasACompletion is the guard that keeps this from rotting:
// a command that takes an argument and says nothing about how to complete it
// falls back to listing files, which is never right for an nm argument.
func TestEveryArgumentHasACompletion(t *testing.T) {
	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		for _, sub := range cmd.Commands() {
			walk(sub, strings.TrimSpace(path+" "+sub.Name()))
		}
		// cobra generates these two and completes them itself.
		if cmd.Name() == "completion" || cmd.Name() == "help" || cmd.HasSubCommands() {
			return
		}
		if cmd.ValidArgsFunction == nil && len(cmd.ValidArgs) == 0 {
			t.Errorf("`nm %s` has no ValidArgsFunction, so tab falls back to listing files", path)
		}
	}
	walk(newRootCmd(), "")
}

func sortedStrings(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] > values[i] {
			return false
		}
	}
	return true
}
