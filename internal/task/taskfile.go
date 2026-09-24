package task

import (
	"fmt"
	"path/filepath"

	"github.com/nitinshyamk/nm/internal/config"
)

// ReadyFile is what an agent writes when the acceptance criteria are met, the
// verification gate passes, and a pull request is open and green. It holds a
// timestamp.
//
// It lands in the escalations directory rather than beside the task record
// because it is part of the same conversation: the orchestrator reads it to move
// the task into review, and a refine round rewrites it to say the round trip is
// done. Its timestamp is the line between work already published and feedback
// that arrived after.
const ReadyFile = "ready-to-review.md"

// RefiningFile records that a refine round is under way, so an orchestrator
// running every minute starts one agent rather than one per pass.
//
// Every other transition in a workplan is latched by the file move that performs
// it. A refine round is the exception — the task stays in review throughout —
// which is why the latch has to be written down.
const RefiningFile = "refining.md"

// TaskFilePrompt is what an agent working from a task definition is told.
//
// It is deliberately one line. The definition already says what the work is, the
// acceptance criteria already say when it is done, and the skill already knows
// the procedure; restating any of that here would give the agent two sources for
// the same thing and no way to tell which is current.
func TaskFilePrompt(cfg config.Config, t Task) string {
	return fmt.Sprintf("/nm-task-execute the task in %s",
		filepath.ToSlash(filepath.Join(cfg.InputDir, t.TaskFile)))
}

// Ready returns the path of the ready-to-review marker.
func (t Task) Ready(cfg config.Config) string {
	return filepath.Join(t.Escalations(cfg), ReadyFile)
}

// Refining returns the path of the refine-in-progress latch.
func (t Task) Refining(cfg config.Config) string {
	return filepath.Join(t.Escalations(cfg), RefiningFile)
}
