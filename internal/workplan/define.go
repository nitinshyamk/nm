package workplan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
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
//
// A file lands under its own name; a directory's contents land directly in
// artifacts/ rather than under an extra level named after the source, which is
// what makes `define -a ./design` and `define -a ./design/plan.md` produce
// comparably shaped results.
func (w Workplan) CopyArtifacts(src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("reading artifacts from %s: %w", src, err)
	}
	if err := os.MkdirAll(w.Artifacts(), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", w.Artifacts(), err)
	}

	if !info.IsDir() {
		return copyFile(src, filepath.Join(w.Artifacts(), filepath.Base(src)))
	}
	return copyTree(src, w.Artifacts())
}

// copyTree copies the contents of src into dst, creating directories as it goes.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(to, 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", to, err)
			}
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		// Anything that is not a regular file or a directory — a symlink, a
		// socket — is skipped rather than followed. Artifacts are documents.
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one file, refusing to overwrite an existing destination.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	// Closing a file that was only read cannot lose data, so the error says
	// nothing a caller could act on. The write side below is checked, because
	// there a failed close means bytes never reached the disk.
	defer func() { _ = in.Close() }()

	// O_EXCL is the refusal: it is what makes a second `define -a` report a
	// collision rather than quietly replacing the context tasks were scoped
	// against.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; remove it to replace it", dst)
		}
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		// The copy error is the one worth reporting; a close failure on a file
		// that is already being abandoned adds nothing. Remove the partial file
		// so a later run is not blocked by the half-written copy O_EXCL would
		// then refuse.
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
