package workplan

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/filecopy"
)

// nameOK rejects names that would make a confusing directory name.
//
// This is stricter than worktree.ValidateName in one way that matters: a
// workplan name becomes exactly one directory under the workplans root, so a
// slash would bury the workplan a level down rather than naming it. A worktree
// name may contain slashes because it becomes a branch.
var nameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateName reports whether name can be used for a workplan.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("name is empty")
	case !nameOK.MatchString(name):
		return fmt.Errorf("name %q must start with a letter or digit and contain only letters, digits, and . _ -", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("name %q may not contain a %q path segment", name, "..")
	case strings.HasPrefix(name, "."):
		// Unreachable through nameOK, kept because the workplans root is listed
		// by prefix elsewhere and a dotted name would hide from it.
		return fmt.Errorf("name %q may not start with a dot", name)
	}
	return nil
}

// Options describes a workplan to create.
type Options struct {
	Name string
	// Artifacts is a file or directory whose contents become the workplan's
	// artifacts. Empty means the workplan starts with none.
	Artifacts string
	Now       func() time.Time
}

// Define creates a workplan directory: one directory per state, escalations/,
// artifacts/, and the record that marks the directory as a workplan.
//
// It is idempotent in the useful direction. Re-running on an existing workplan
// creates whatever directories are missing and leaves the rest alone, so a
// workplan made before a directory existed grows it. It is not a way to replace
// artifacts: copying refuses to overwrite a file that is already there, because
// a workplan's artifacts are the context its tasks were scoped against and
// silently rewriting them would change the meaning of work already in flight.
func Define(cfg config.Config, opts Options) (Workplan, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if err := ValidateName(opts.Name); err != nil {
		return Workplan{}, fmt.Errorf("workplan %w", err)
	}

	dir := DirFor(cfg, opts.Name)
	w := Workplan{Name: opts.Name, CreatedAt: now(), Version: SchemaVersion, Dir: dir}

	// An existing workplan keeps its creation time: the record is about when the
	// work was first scoped, not when a directory was last repaired.
	fresh := true
	if existing, err := Load(dir); err == nil {
		w.CreatedAt = existing.CreatedAt
		fresh = false
	}

	if err := w.makeDirs(); err != nil {
		return Workplan{}, err
	}
	if err := w.Save(); err != nil {
		return Workplan{}, err
	}

	if opts.Artifacts != "" {
		if err := w.CopyArtifacts(opts.Artifacts); err != nil {
			// A workplan whose artifacts failed to copy is worse than no
			// workplan: its tasks would be scoped against context that is not
			// there. Unwind, but only what this call created.
			if fresh {
				_ = os.RemoveAll(dir)
			}
			return Workplan{}, err
		}
	}
	return w, nil
}

// CopyArtifacts copies a file, or the contents of a directory, into the
// workplan's artifacts directory.
func (w Workplan) CopyArtifacts(src string) error {
	if err := filecopy.Into(src, w.Artifacts()); err != nil {
		return fmt.Errorf("copying artifacts: %w", err)
	}
	return nil
}
