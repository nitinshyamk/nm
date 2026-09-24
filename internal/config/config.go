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
	WorkplansRoot     string `json:"workplans_root"`
	ArtifactsDir      string `json:"artifacts_dir"`
	InputDir          string `json:"input_dir"`
	ScratchDir        string `json:"scratch_dir"`
	EscalationsDir    string `json:"escalations_dir"`
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
		WorkplansRoot:     "~/projects/workplans",
		ArtifactsDir:      "artifacts",
		InputDir:          "input",
		ScratchDir:        "scratch",
		EscalationsDir:    "escalations",
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

	if err := cfg.deriveMissingRoots(raw); err != nil {
		return Config{}, err
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

// deriveMissingRoots fills in a root that this version of nm added, siting it
// beside the roots the user already configured rather than at its default.
//
// The defaults all sit under ~/projects, but a user who moved projects_root to
// C:/repos has moved every root with it. Handing such a config the literal
// default for a new key would put workplans under ~/projects/workplans while
// tasks and worktrees are under C:/repos — a split nobody asked for and the kind
// of thing that is noticed only after work has been written to the wrong place.
//
// Only a key that is genuinely absent is derived. A key the user set, including
// one set to the default, is theirs.
func (c *Config) deriveMissingRoots(raw []byte) error {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return fmt.Errorf("parsing the configuration: %w", err)
	}
	if _, ok := present["workplans_root"]; ok {
		return nil
	}
	// Tasks is the nearest relative: a workplan is a directory of tasks, so it
	// belongs wherever tasks live.
	if base := siblingOf(c.TasksRoot); base != "" {
		c.WorkplansRoot = base + "/workplans"
	}
	return nil
}

// siblingOf returns the parent of a configured root, in the same spelling the
// user wrote it, so a derived root keeps their separators and any leading ~.
// It answers "" when there is no parent to hang a sibling from.
func siblingOf(root string) string {
	trimmed := strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx <= 0 {
		return ""
	}
	return trimmed[:idx]
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

// Workplans returns the expanded directory holding workplan directories.
//
// Unlike a task, a workplan directory is named for the workplan alone, with no
// hash: a workplan name is chosen deliberately and typed often.
func (c Config) Workplans() string { return Expand(c.WorkplansRoot) }

// Install returns the expanded directory the binary installs into.
func (c Config) Install() string { return Expand(c.InstallDir) }

// RepoPath returns the expected source checkout for a repository name.
func (c Config) RepoPath(repo string) string { return filepath.Join(c.Projects(), repo) }
