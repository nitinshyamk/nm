package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func configAt(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".nm.json")
	t.Setenv(EnvPath, path)
	return path
}

func TestLoadCreatesDefaults(t *testing.T) {
	path := configAt(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != Defaults() {
		t.Errorf("Load returned %+v, want the defaults", cfg)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Load did not create the config file: %v", err)
	}

	// A second Load reads what the first wrote.
	again, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if again != cfg {
		t.Errorf("second Load returned %+v, want %+v", again, cfg)
	}
}

func TestLoadFillsMissingKeysAndKeepsUnknownOnes(t *testing.T) {
	path := configAt(t)
	partial := `{"hash_length": 9, "experimental_thing": {"a": 1}}`
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HashLength != 9 {
		t.Errorf("HashLength = %d, want the value from the file (9)", cfg.HashLength)
	}
	if cfg.ProjectsRoot != Defaults().ProjectsRoot {
		t.Errorf("ProjectsRoot = %q, want the default", cfg.ProjectsRoot)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("rewritten config is not valid JSON: %v", err)
	}
	if _, ok := back["experimental_thing"]; !ok {
		t.Error("rewriting the config dropped an unrecognized key")
	}
	if _, ok := back["projects_root"]; !ok {
		t.Error("rewriting the config did not add the missing keys")
	}
}

// A config file written before workplans existed must grow the key without
// disturbing the values already in it. This is the upgrade path every existing
// ~/.nm.json takes, so it is asserted rather than assumed.
func TestLoadAddsWorkplansRootToAnOlderConfig(t *testing.T) {
	path := configAt(t)
	older := `{
  "projects_root": "C:/repos",
  "tasks_root": "C:/repos/tasks",
  "hash_length": 6
}`
	if err := os.WriteFile(path, []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WorkplansRoot != Defaults().WorkplansRoot {
		t.Errorf("WorkplansRoot = %q, want the default %q", cfg.WorkplansRoot, Defaults().WorkplansRoot)
	}
	if cfg.ProjectsRoot != "C:/repos" {
		t.Errorf("ProjectsRoot = %q, want the value already in the file", cfg.ProjectsRoot)
	}
	if cfg.TasksRoot != "C:/repos/tasks" {
		t.Errorf("TasksRoot = %q, want the value already in the file", cfg.TasksRoot)
	}
	if cfg.HashLength != 6 {
		t.Errorf("HashLength = %d, want 6", cfg.HashLength)
	}

	// The key is written back, so the next run reads it rather than defaulting
	// again.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("rewritten config is not valid JSON: %v", err)
	}
	for _, key := range []string{"workplans_root", "escalations_dir"} {
		if _, ok := back[key]; !ok {
			t.Errorf("Load did not persist %s", key)
		}
	}
}

func TestWorkplansExpandsLikeTheOtherRoots(t *testing.T) {
	cfg := Defaults()
	cfg.WorkplansRoot = "/src/workplans"
	if got := cfg.Workplans(); got != "/src/workplans" {
		t.Errorf("Workplans() = %q, want /src/workplans", got)
	}
}

func TestLoadRejectsBrokenJSON(t *testing.T) {
	path := configAt(t)
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted invalid JSON; it should report it rather than silently reset")
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := Expand("~/projects"), filepath.Join(home, "projects"); got != want {
		t.Errorf("Expand(~/projects) = %q, want %q", got, want)
	}
	if got := Expand("/absolute/path"); got != "/absolute/path" {
		t.Errorf("Expand mangled an absolute path: %q", got)
	}
	if got := Expand("~notauser/x"); got != "~notauser/x" {
		t.Errorf("Expand should only expand a leading ~/, got %q", got)
	}
}

func TestRepoPath(t *testing.T) {
	cfg := Defaults()
	cfg.ProjectsRoot = "/src"
	// RepoPath joins with filepath, so the separator is the platform's: the
	// expectation has to be built the same way rather than hardcoded as POSIX.
	want := filepath.Join("/src", "nm")
	if got := cfg.RepoPath("nm"); got != want {
		t.Errorf("RepoPath = %q, want %q", got, want)
	}
}
