// Package task manages multi-repo task directories: a worktree per repository,
// the input, artifacts, and scratch directories, and the record of the agent
// working on it.
package task

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/nmhash"
	"github.com/nitinshyamk/nm/internal/worktree"
)

// MetaFile records what a task directory contains.
const MetaFile = ".nm-task.json"

// PromptFile is the prompt a task started from, written inside the input
// directory. The record in MetaFile is the source of truth; this is the copy a
// human or an agent can read without parsing JSON.
const PromptFile = "prompt.md"

// Repo is one repository checked out inside a task.
type Repo struct {
	Name       string `json:"repo"`
	Dir        string `json:"dir"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
	BaseCommit string `json:"base_commit"`
	Source     string `json:"source"`
	PRURL      string `json:"pr_url,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
}

// Agent records the background claude session started for a task.
type Agent struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id,omitempty"`
	LaunchedAt time.Time `json:"launched_at"`
	Raw        string    `json:"launch_output,omitempty"`
}

// Task is the contents of one directory under the tasks root.
type Task struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	Repos     []Repo    `json:"repos"`
	Prompt    string    `json:"prompt,omitempty"`
	Agent     *Agent    `json:"agent,omitempty"`

	Dir string `json:"-"` // absolute path, implied by the file's location
}

// Label is how a task is identified in the UI.
func (t Task) Label() string { return t.Name + "-" + t.Hash }

// Artifacts returns the directory holding output that is never committed.
func (t Task) Artifacts(cfg config.Config) string {
	return filepath.Join(t.Dir, cfg.ArtifactsDir)
}

// Input returns the directory holding what the task was given: the prompt, and
// whatever assets the user drops in beside it.
func (t Task) Input(cfg config.Config) string {
	return filepath.Join(t.Dir, cfg.InputDir)
}

// Scratch returns the directory for throwaway working notes — debugging
// output, half-finished thinking, anything an agent needs somewhere to put.
// Nothing in nm reads it back, so it never has to be tidy.
func (t Task) Scratch(cfg config.Config) string {
	return filepath.Join(t.Dir, cfg.ScratchDir)
}

// makeDirs creates the fixed directories every task directory has. It runs on
// creation and again whenever a prompt is saved, so a task made before one of
// these directories existed grows it rather than staying half-shaped.
func (t Task) makeDirs(cfg config.Config) error {
	for _, dir := range []string{t.Artifacts(cfg), t.Input(cfg), t.Scratch(cfg)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// Dirs lists the worktree directories, for passing to an agent as --add-dir.
func (t Task) Dirs() []string {
	dirs := make([]string, 0, len(t.Repos))
	for _, r := range t.Repos {
		dirs = append(dirs, r.Dir)
	}
	return dirs
}

// DirName builds the on-disk name for a task.
func DirName(name, hash string) string { return name + "-" + hash }

// Hash derives a task's hash from its name and repositories. Repository order
// is normalized, so the same set of repos in any order yields the same hash.
func Hash(length int, name string, repos []string) string {
	sorted := append([]string(nil), repos...)
	sort.Strings(sorted)
	return nmhash.Short(length, append([]string{name}, sorted...)...)
}

// Options describes a task to create.
type Options struct {
	Name    string
	Repos   []string
	Offline bool
	Now     func() time.Time
}

// Create builds a task directory with a worktree per repository.
//
// Creation is transactional: if any repository fails, the worktrees already
// added are removed and the directory is deleted, so a failed attempt never
// leaves half a task behind.
func Create(cfg config.Config, opts Options) (t Task, err error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if err := worktree.ValidateName(opts.Name); err != nil {
		return Task{}, fmt.Errorf("task %w", err)
	}
	if len(opts.Repos) == 0 {
		return Task{}, fmt.Errorf("a task needs at least one repository")
	}
	if dup := firstDuplicate(opts.Repos); dup != "" {
		return Task{}, fmt.Errorf("repository %q is listed twice", dup)
	}
	for _, repo := range opts.Repos {
		dir := cfg.RepoPath(repo)
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			return Task{}, fmt.Errorf("no repository %q in %s", repo, cfg.Projects())
		}
		if !gitx.IsRepo(dir) {
			return Task{}, fmt.Errorf("%s is not a git repository", dir)
		}
	}

	hash := Hash(cfg.HashLength, opts.Name, opts.Repos)
	dir := filepath.Join(cfg.Tasks(), DirName(opts.Name, hash))
	if _, statErr := os.Stat(dir); statErr == nil {
		return Task{}, fmt.Errorf("task %s already exists at %s", DirName(opts.Name, hash), dir)
	}

	t = Task{Name: opts.Name, Hash: hash, CreatedAt: now(), Dir: dir}
	branch := opts.Name + "-" + hash

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Task{}, fmt.Errorf("creating %s: %w", dir, err)
	}
	// Unwind everything on any failure, so a half-built task never survives.
	defer func() {
		if err == nil {
			return
		}
		for _, r := range t.Repos {
			_, _ = worktree.Remove(r.Source, r.Dir, r.Branch, true)
		}
		_ = os.RemoveAll(dir)
	}()

	for _, repo := range opts.Repos {
		source := cfg.RepoPath(repo)
		if gitx.BranchExists(source, branch) {
			return Task{}, fmt.Errorf("branch %s already exists in %s", branch, source)
		}
		base, baseErr := gitx.ResolveBase(source, cfg.DefaultBaseBranch, opts.Offline)
		if baseErr != nil {
			return Task{}, baseErr
		}
		worktreeDir := filepath.Join(dir, repo+"-"+hash)
		if addErr := gitx.AddWorktree(source, worktreeDir, branch, base.Commit); addErr != nil {
			return Task{}, addErr
		}
		t.Repos = append(t.Repos, Repo{
			Name:       repo,
			Dir:        worktreeDir,
			Branch:     branch,
			BaseBranch: base.Branch,
			BaseCommit: base.Commit,
			Source:     source,
		})
	}

	if err := t.makeDirs(cfg); err != nil {
		return Task{}, err
	}
	if err := t.Save(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return v
		}
		seen[v] = struct{}{}
	}
	return ""
}

// Save writes the task record into the task directory.
func (t Task) Save() error {
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the task record: %w", err)
	}
	path := filepath.Join(t.Dir, MetaFile)
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// SavePrompt writes the task's prompt to input/prompt.md, so the text an agent
// was launched with sits beside the assets it was given rather than only inside
// the JSON record. A task with no prompt writes nothing.
func (t Task) SavePrompt(cfg config.Config) error {
	prompt := strings.TrimSpace(t.Prompt)
	if prompt == "" {
		return nil
	}
	if err := t.makeDirs(cfg); err != nil {
		return err
	}
	path := filepath.Join(t.Input(cfg), PromptFile)
	if err := os.WriteFile(path, []byte(prompt+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Load reads the task record from a task directory.
func Load(dir string) (Task, error) {
	path := filepath.Join(dir, MetaFile)
	blob, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := json.Unmarshal(blob, &t); err != nil {
		return Task{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	t.Dir = dir
	return t, nil
}

// List returns every task under the tasks root, newest first.
func List(cfg config.Config) ([]Task, error) {
	entries, err := os.ReadDir(cfg.Tasks())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", cfg.Tasks(), err)
	}

	var tasks []Task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := Load(filepath.Join(cfg.Tasks(), e.Name()))
		if err != nil {
			continue // not a task directory
		}
		tasks = append(tasks, t)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})
	return tasks, nil
}

// RepoStatus pairs a repository with the state of its worktree.
type RepoStatus struct {
	Repo   Repo
	Status gitx.Status
	Err    error
}

// View is a task plus everything the list view needs to describe it.
type View struct {
	Task      Task
	Repos     []RepoStatus
	Artifacts int // number of files under the artifacts directory
}

// Dirty reports whether any repository holds work that exists nowhere else.
func (v View) Dirty() bool {
	for _, r := range v.Repos {
		if r.Status.Dirty() {
			return true
		}
	}
	return false
}

// Hazards lists, per repository, what deleting the task would destroy.
func (v View) Hazards() []string {
	var out []string
	for _, r := range v.Repos {
		if h := r.Status.Hazards(); len(h) > 0 {
			out = append(out, fmt.Sprintf("%s: %s", r.Repo.Name, strings.Join(h, ", ")))
		}
	}
	if v.Artifacts > 0 {
		out = append(out, fmt.Sprintf("artifacts/ holds %s", plural(v.Artifacts, "file", "files")))
	}
	return out
}

// Totals sums the status counters across every repository in the task.
func (v View) Totals() gitx.Status {
	var total gitx.Status
	for _, r := range v.Repos {
		total.Staged += r.Status.Staged
		total.Unstaged += r.Status.Unstaged
		total.Untracked += r.Status.Untracked
		total.Unpushed += r.Status.Unpushed
		if r.Status.LastCommit.After(total.LastCommit) {
			total.LastCommit = r.Status.LastCommit
		}
	}
	return total
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// Survey lists every task with its repository statuses and artifact counts.
func Survey(cfg config.Config) ([]View, error) {
	tasks, err := List(cfg)
	if err != nil {
		return nil, err
	}
	views := make([]View, len(tasks))

	var wg sync.WaitGroup
	for i, t := range tasks {
		views[i] = View{Task: t, Repos: make([]RepoStatus, len(t.Repos))}
		wg.Add(1)
		go func(v *View) {
			defer wg.Done()
			for j, repo := range v.Task.Repos {
				st, err := gitx.GetStatus(repo.Dir)
				v.Repos[j] = RepoStatus{Repo: repo, Status: st, Err: err}
			}
			v.Artifacts, _ = CountArtifacts(v.Task.Artifacts(cfg))
		}(&views[i])
	}
	wg.Wait()
	return views, nil
}

// CountArtifacts counts the files under a task's artifacts directory.
func CountArtifacts(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return count, err
}

// Delete removes every worktree in a task and then the task directory. It
// returns notes about anything deliberately left behind.
func Delete(t Task, force bool) (notes []string, err error) {
	for _, r := range t.Repos {
		source := r.Source
		if source == "" {
			if main, mainErr := gitx.MainWorktree(r.Dir); mainErr == nil {
				source = main
			}
		}
		if source == "" {
			notes = append(notes, fmt.Sprintf("could not find the repository owning %s", r.Dir))
			continue
		}
		note, removeErr := worktree.Remove(source, r.Dir, r.Branch, force)
		if removeErr != nil {
			return notes, removeErr
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	if err := os.RemoveAll(t.Dir); err != nil {
		return notes, fmt.Errorf("removing %s: %w", t.Dir, err)
	}
	return notes, nil
}
