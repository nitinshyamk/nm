// Package config loads and creates the nm user configuration at ~/.nm.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvPath overrides the configuration file location. Used by tests.
const EnvPath = "NM_CONFIG"

// Config mirrors ~/.nm.json. Paths may contain a leading ~, which the
// accessor methods expand.
type Config struct {
	ProjectsRoot      string `json:"projects_root"`
	WorktreesRoot     string `json:"worktrees_root"`
	TasksRoot         string `json:"tasks_root"`
	ArtifactsDir      string `json:"artifacts_dir"`
	HashLength        int    `json:"hash_length"`
	ListRows          int    `json:"list_rows"`
	PromptRows        int    `json:"prompt_rows"`
	EditorCommand     string `json:"editor_command"`
	ClaudeCommand     string `json:"claude_command"`
	GHCommand         string `json:"gh_command"`
	InstallDir        string `json:"install_dir"`
	DefaultBaseBranch string `json:"default_base_branch"`
	// BranchPrefix goes in front of the branches nm creates, so a shared remote
	// shows whose they are: "nitin/" yields nitin/<name>-<hash>. It is used
	// literally, trailing slash included, because a prefix ending in - or _ is
	// just as valid a convention. Empty means unprefixed.
	//
	// It applies only to branches nm creates. `nm task rebase` starts from a
	// branch that already exists on the remote and is never renamed.
	BranchPrefix string `json:"branch_prefix"`
}

// Defaults returns the configuration nm writes on first run.
func Defaults() Config {
	return Config{
		ProjectsRoot:      "~/projects",
		WorktreesRoot:     "~/projects/worktrees",
		TasksRoot:         "~/projects/tasks",
		ArtifactsDir:      "artifacts",
		HashLength:        6,
		ListRows:          8,
		PromptRows:        10,
		EditorCommand:     "code",
		ClaudeCommand:     "claude",
		GHCommand:         "gh",
		InstallDir:        "~/.local/bin",
		DefaultBaseBranch: "",
		BranchPrefix:      "",
	}
}

// Path returns the configuration file location.
func Path() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".nm.json"), nil
}

// Load reads the configuration, creating it with defaults when absent and
// filling in any missing keys. Keys nm does not recognize are preserved.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := Defaults()
		if err := write(path, cfg, nil); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := Defaults()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	var unknown map[string]json.RawMessage
	if err := json.Unmarshal(raw, &unknown); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	for k := range known() {
		delete(unknown, k)
	}

	// Rewrite only when the file is missing keys, so nm is self-healing after
	// an upgrade adds a setting without rewriting a file that is already fine.
	if complete, err := hasAllKeys(raw); err == nil && !complete {
		if err := write(path, cfg, unknown); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func known() map[string]struct{} {
	blob, err := json.Marshal(Defaults())
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil
	}
	keys := make(map[string]struct{}, len(m))
	for k := range m {
		keys[k] = struct{}{}
	}
	return keys
}

func hasAllKeys(raw []byte) (bool, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return false, err
	}
	for k := range known() {
		if _, ok := present[k]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func write(path string, cfg Config, extra map[string]json.RawMessage) error {
	blob, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(blob, &merged); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	for k, v := range extra {
		merged[k] = v
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Expand resolves a leading ~ against the user's home directory.
func Expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return path
}

// Projects returns the expanded directory holding the source repositories.
func (c Config) Projects() string { return Expand(c.ProjectsRoot) }

// Worktrees returns the expanded directory holding standalone worktrees.
func (c Config) Worktrees() string { return Expand(c.WorktreesRoot) }

// Tasks returns the expanded directory holding task directories.
func (c Config) Tasks() string { return Expand(c.TasksRoot) }

// Install returns the expanded directory the binary installs into.
func (c Config) Install() string { return Expand(c.InstallDir) }

// RepoPath returns the expected source checkout for a repository name.
func (c Config) RepoPath(repo string) string { return filepath.Join(c.Projects(), repo) }
