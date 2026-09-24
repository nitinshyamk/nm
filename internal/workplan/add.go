package workplan

import (
	"fmt"
	"os"
)

// AddTask writes one task definition into the workplan's planned directory and
// creates the escalation directory it will write to if it gets stuck.
//
// A task whose id is already in the workplan is refused wherever that id sits.
// Checking every state rather than only planned/ is what stops a re-added task
// from reappearing as planned while the original is halfway through review.
func (w Workplan) AddTask(t Task) error {
	if err := t.Validate(); err != nil {
		return fmt.Errorf("task %w", err)
	}
	if existing, ok := w.FindTask(t.ID); ok {
		return fmt.Errorf("task %s is already in this workplan, in %s", t.ID, existing.State)
	}

	blob, err := t.Encode()
	if err != nil {
		return err
	}

	// The escalation directory comes first: a task file present with nowhere to
	// escalate to is the worse of the two half-states, because the orchestrator
	// would start the task and only then find the gap.
	if err := os.MkdirAll(w.TaskEscalations(t.ID), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", w.TaskEscalations(t.ID), err)
	}
	if err := os.MkdirAll(w.StateDir(Planned), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", w.StateDir(Planned), err)
	}

	path := w.TaskFile(Planned, t.ID)
	// O_EXCL rather than a stat first: FindTask above can race with another
	// writer, and this is the check that cannot.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("task %s is already in this workplan", t.ID)
		}
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := file.Write(blob); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
