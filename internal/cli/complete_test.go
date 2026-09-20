package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/spf13/cobra"
)

// completionEnv points nm at a throwaway configuration and projects root, so
// completion tests never read the developer's own ~/.nm.json or ~/projects.
func completionEnv(t *testing.T, repos ...string) config.Config {
	t.Helper()
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
