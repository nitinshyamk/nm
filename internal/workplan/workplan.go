// Package workplan manages a body of work as a directory of task definitions
// moving through five states.
//
// A workplan lives at <workplans_root>/<name>/ and holds one directory per
// state — planned, in-progress, review, approved, completed — plus escalations/
// for the conversation between an agent and a human, and artifacts/ for the
// design context the tasks were scoped against.
//
// The state of a task is where its file sits: planned/01-foo.json means 01-foo
// is planned, and a transition is a file move. That makes the whole workplan
// legible with ls and every transition idempotent, which is what lets the
// orchestrator run repeatedly and recover from a crash by simply running again.
package workplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
)

// MetaFile records what a workplan directory is. Its presence is what
// distinguishes a workplan from any other directory under the workplans root.
const MetaFile = ".nm-workplan.json"

// SchemaVersion is the layout version written into MetaFile. It exists so a
// future change to the directory shape can be detected rather than guessed at.
const SchemaVersion = 1

// State is one stage of a task's life. The value is also the directory name, so
// there is exactly one spelling of each state in the program.
type State string

// The five states a task moves through, in order.
const (
	// Planned is scoped work that has not started.
	Planned State = "planned"
	// InProgress is being actively worked by an agent.
	InProgress State = "in-progress"
	// Review passes local validation, has a branch and a pull request, and
	// passes CI. It needs a human to approve it.
	Review State = "review"
	// Approved has been approved by a human and needs merging. Other reviewers
	// may still be pending.
	Approved State = "approved"
	// Completed is merged into production.
	Completed State = "completed"
)

// States lists every state in pipeline order. Iterating this rather than
// hardcoding a list at each call site is what keeps a new state from being
// silently missed by one of them.
func States() []State {
	return []State{Planned, InProgress, Review, Approved, Completed}
}

// EscalationsDirName is the directory holding one subdirectory per task, in
// which escalations and their resolutions accumulate.
const EscalationsDirName = "escalations"

// ArtifactsDirName is the directory holding the design context the tasks were
// scoped against. It exists only when `define -a` was given something to copy.
const ArtifactsDirName = "artifacts"

// LockFile guards against two orchestrator runs acting on one workplan at once.
const LockFile = ".lock"

// Workplan is one directory under the workplans root.
type Workplan struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"schema_version"`

	Dir string `json:"-"` // absolute path, implied by the file's location
}

// Label is how a workplan is identified in the UI. A workplan has no hash, so
// its label is just its name — but callers should not have to know that.
func (w Workplan) Label() string { return w.Name }

// StateDir returns the directory holding the tasks in one state.
func (w Workplan) StateDir(s State) string { return filepath.Join(w.Dir, string(s)) }

// Escalations returns the directory holding per-task escalation directories.
func (w Workplan) Escalations() string { return filepath.Join(w.Dir, EscalationsDirName) }

// TaskEscalations returns the escalation directory belonging to one task.
func (w Workplan) TaskEscalations(id string) string { return filepath.Join(w.Escalations(), id) }

// Artifacts returns the directory holding the copied design context.
func (w Workplan) Artifacts() string { return filepath.Join(w.Dir, ArtifactsDirName) }

// Lock returns the path of the orchestrator's lock file.
func (w Workplan) Lock() string { return filepath.Join(w.Dir, LockFile) }

// TaskFile returns where a task's definition sits when it is in one state.
func (w Workplan) TaskFile(s State, id string) string {
	return filepath.Join(w.StateDir(s), id+".json")
}

// Dir returns the directory a workplan of this name would occupy. It does not
// check whether anything is there.
func DirFor(cfg config.Config, name string) string {
	return filepath.Join(cfg.Workplans(), name)
}

// makeDirs creates the fixed directories every workplan has.
//
// Like the equivalent in package task it runs on creation and again on demand,
// so a workplan made before one of these directories existed grows it rather
// than staying half-shaped. artifacts/ is included: a workplan defined without
// artifacts can still be given them later, and an empty directory says where
// they go.
func (w Workplan) makeDirs() error {
	dirs := make([]string, 0, len(States())+2)
	for _, s := range States() {
		dirs = append(dirs, w.StateDir(s))
	}
	dirs = append(dirs, w.Escalations(), w.Artifacts())

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// Save writes the workplan record into the workplan directory.
func (w Workplan) Save() error {
	blob, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the workplan record: %w", err)
	}
	path := filepath.Join(w.Dir, MetaFile)
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// Load reads the workplan record from a workplan directory.
func Load(dir string) (Workplan, error) {
	path := filepath.Join(dir, MetaFile)
	blob, err := os.ReadFile(path)
	if err != nil {
		return Workplan{}, err
	}
	var w Workplan
	if err := json.Unmarshal(blob, &w); err != nil {
		return Workplan{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	w.Dir = dir
	return w, nil
}

// List returns every workplan under the workplans root, newest first.
func List(cfg config.Config) ([]Workplan, error) {
	root := cfg.Workplans()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var plans []Workplan
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		w, err := Load(filepath.Join(root, e.Name()))
		if err != nil {
			continue // not a workplan directory
		}
		plans = append(plans, w)
	}
	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].CreatedAt.After(plans[j].CreatedAt)
	})
	return plans, nil
}
