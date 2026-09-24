// Package filecopy copies the documents nm moves between directories: a design
// into a workplan's artifacts, a workplan's artifacts into a task, a task
// definition into a task's input.
//
// Two rules hold everywhere, and both exist because what is being copied is
// context that work gets scoped against:
//
//   - An existing destination is never overwritten. Replacing the design a task
//     was scoped against would change the meaning of work already in flight, so
//     a collision is reported and the caller decides.
//   - Only regular files and directories are copied. A symlink or a device node
//     in an artifacts directory is not a document, and following one would copy
//     from somewhere nobody named.
package filecopy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Into copies src into the directory dst.
//
// A file lands under its own name. A directory's *contents* land directly in
// dst rather than under an extra level named after the source, which is what
// makes copying ./design and copying ./design/plan.md produce comparably shaped
// results.
func Into(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if !info.IsDir() {
		return File(src, filepath.Join(dst, filepath.Base(src)))
	}
	return Tree(src, dst)
}

// Tree copies the contents of src into dst, creating directories as it goes.
func Tree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if err := os.MkdirAll(to, 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", to, err)
			}
			if err := Tree(from, to); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := File(from, to); err != nil {
				return err
			}
		default:
			// A symlink, socket, or device is not a document. Skipped rather
			// than followed, so a copy never reaches outside the tree it was
			// given.
		}
	}
	return nil
}

// File copies one file, refusing to overwrite an existing destination.
func File(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	// Closing a file that was only read cannot lose data, so its error says
	// nothing a caller could act on. The write side below is checked, because
	// there a failed close means bytes never reached the disk.
	defer func() { _ = in.Close() }()

	// O_EXCL is the refusal. Without it a second copy would silently replace
	// the context that work was already scoped against.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; remove it to replace it", dst)
		}
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		// The copy error is the one worth reporting. The partial file is removed
		// so a later attempt is not blocked by the half-written copy O_EXCL
		// would then refuse.
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
