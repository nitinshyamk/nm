package workplan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/filecopy"
	"github.com/nitinshyamk/nm/internal/task"
)

// ResolutionSuffix marks a human's answer to an escalation. An escalation written
// at 2026-05-01-08-34-02.md is answered by
// 2026-05-01-08-34-02-resolution.md.
//
// The suffix rather than a separate directory keeps the two adjacent when sorted,
// and makes it impossible for an answer to overwrite the question it answers.
const ResolutionSuffix = "-resolution.md"

// Escalation is one thing an agent needs a human to decide.
type Escalation struct {
	TaskID string
	Name   string // the filename, which is the timestamp
	Body   string
}

// Stamp is the timestamp an escalation is named for, without the extension.
func (e Escalation) Stamp() string { return strings.TrimSuffix(e.Name, ".md") }

// IsResolution reports whether a filename is an answer rather than a question.
func IsResolution(name string) bool { return strings.HasSuffix(name, ResolutionSuffix) }

// CollectEscalations copies escalations out of one task's directory into the
// workplan, and returns the ones that had not been seen before.
//
// "Not seen before" is by filename, not by modification time. The filename is a
// timestamp, so it is already unique and already ordered — and copying a file
// rewrites its mtime, which would make every escalation look new on the pass
// after it arrived.
//
// Resolutions are skipped: they travel the other way, and copying one back would
// make the workplan the source of an answer it was the destination for.
func (w Workplan) CollectEscalations(id, taskEscalations string) ([]Escalation, error) {
	entries, err := os.ReadDir(taskEscalations)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", taskEscalations, err)
	}

	dst := w.TaskEscalations(id)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dst, err)
	}

	var fresh []Escalation
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || IsResolution(name) {
			continue
		}
		// The markers are bookkeeping between the agent and the orchestrator, not
		// things a human is asked to read.
		if name == task.ReadyFile || name == task.RefiningFile {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(dst, name)); statErr == nil {
			continue // already collected on an earlier pass
		}
		if err := filecopy.File(filepath.Join(taskEscalations, name), filepath.Join(dst, name)); err != nil {
			return fresh, err
		}
		body, readErr := os.ReadFile(filepath.Join(dst, name))
		if readErr != nil {
			return fresh, fmt.Errorf("reading %s: %w", name, readErr)
		}
		fresh = append(fresh, Escalation{TaskID: id, Name: name, Body: string(body)})
	}

	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Name < fresh[j].Name })
	return fresh, nil
}

// DeliverResolutions copies a task's resolutions out of the workplan and into the
// task directory, where the agent waiting on one will see it.
//
// Returns the names delivered. Files already there are skipped, so this is safe
// to run on every pass.
func (w Workplan) DeliverResolutions(id, taskEscalations string) ([]string, error) {
	src := w.TaskEscalations(id)
	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(taskEscalations, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", taskEscalations, err)
	}

	var delivered []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !IsResolution(name) {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(taskEscalations, name)); statErr == nil {
			continue
		}
		if err := filecopy.File(filepath.Join(src, name), filepath.Join(taskEscalations, name)); err != nil {
			return delivered, err
		}
		delivered = append(delivered, name)
	}
	sort.Strings(delivered)
	return delivered, nil
}

// OpenEscalations lists the escalations for one task that have no resolution
// beside them, which is what "waiting on a human" means.
func (w Workplan) OpenEscalations(id string) ([]Escalation, error) {
	dir := w.TaskEscalations(id)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	answered := make(map[string]struct{})
	var questions []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() || !strings.HasSuffix(name, ".md"):
		case IsResolution(name):
			answered[strings.TrimSuffix(name, ResolutionSuffix)] = struct{}{}
		default:
			questions = append(questions, name)
		}
	}

	var open []Escalation
	for _, name := range questions {
		stamp := strings.TrimSuffix(name, ".md")
		if _, ok := answered[stamp]; ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return open, fmt.Errorf("reading %s: %w", name, err)
		}
		open = append(open, Escalation{TaskID: id, Name: name, Body: string(body)})
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Name < open[j].Name })
	return open, nil
}

// Delivered is one resolution that reached a task.
type Delivered struct {
	TaskID string
	Name   string
}

// DeliverAll delivers every task's resolutions, for the resolve command.
//
// A task that has not started yet is skipped rather than reported: its answer
// will be delivered when it starts and the orchestrator copies it in. A
// resolution naming an id the workplan does not have is reported, because that is
// a typo somebody needs to hear about rather than a file to leave lying there.
func DeliverAll(cfg config.Config, w Workplan) ([]Delivered, []string, error) {
	placed, readErrs := w.Tasks()
	problems := make([]string, 0, len(readErrs))
	for _, err := range readErrs {
		problems = append(problems, err.Error())
	}

	known := make(map[string]struct{}, len(placed))
	for _, p := range placed {
		known[p.Task.ID] = struct{}{}
	}

	tasks, err := task.List(cfg)
	if err != nil {
		return nil, problems, err
	}
	dirs := make(map[string]task.Task, len(tasks))
	for _, t := range tasks {
		if t.Workplan == w.Name && t.TaskID != "" {
			dirs[t.TaskID] = t
		}
	}

	var delivered []Delivered
	for _, p := range placed {
		id := p.Task.ID
		t, started := dirs[id]
		if !started {
			continue
		}
		names, err := w.DeliverResolutions(id, t.Escalations(cfg))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		for _, name := range names {
			delivered = append(delivered, Delivered{TaskID: id, Name: name})
		}
	}

	// An escalation directory for an id no task has is a typo, and silently
	// ignoring it would leave an answer nobody ever receives.
	entries, err := os.ReadDir(w.Escalations())
	if err != nil && !os.IsNotExist(err) {
		return delivered, problems, fmt.Errorf("reading %s: %w", w.Escalations(), err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := known[e.Name()]; !ok {
			problems = append(problems, fmt.Sprintf(
				"escalations/%s is not a task in this workplan", e.Name()))
		}
	}
	return delivered, problems, nil
}
