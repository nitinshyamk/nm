package task

import (
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
)

// Resolve finds the one task a query names.
//
// The query may be the full label (auth-9c31a0), the bare name (auth), or an
// unambiguous prefix of either. Completion offers the same set, so what the
// shell suggests is always something this accepts.
func Resolve(cfg config.Config, query string) (Task, error) {
	tasks, err := List(cfg)
	if err != nil {
		return Task{}, err
	}
	return resolveAmong(tasks, query, cfg.Tasks())
}

func resolveAmong(tasks []Task, query, root string) (Task, error) {
	if len(tasks) == 0 {
		return Task{}, fmt.Errorf("no tasks in %s", root)
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return Task{}, fmt.Errorf("which task? pass a name, or run without one to pick from the list")
	}

	// Exact matches win outright, so a task whose name is a prefix of another
	// is still reachable by typing it in full.
	for _, t := range tasks {
		if strings.EqualFold(t.Label(), needle) {
			return t, nil
		}
	}
	var byName []Task
	for _, t := range tasks {
		if strings.EqualFold(t.Name, needle) {
			byName = append(byName, t)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		return Task{}, ambiguous(query, byName)
	}

	var byPrefix []Task
	for _, t := range tasks {
		label, name := strings.ToLower(t.Label()), strings.ToLower(t.Name)
		if strings.HasPrefix(label, needle) || strings.HasPrefix(name, needle) {
			byPrefix = append(byPrefix, t)
		}
	}
	switch len(byPrefix) {
	case 1:
		return byPrefix[0], nil
	case 0:
		return Task{}, fmt.Errorf("no task matching %q in %s", query, root)
	default:
		return Task{}, ambiguous(query, byPrefix)
	}
}

func ambiguous(query string, matches []Task) error {
	labels := make([]string, 0, len(matches))
	for _, t := range matches {
		labels = append(labels, t.Label())
	}
	return fmt.Errorf("%q matches %d tasks: %s", query, len(matches), strings.Join(labels, ", "))
}

// Labels lists every task label, for shell completion. A failure to read the
// tasks directory yields no suggestions rather than an error, because
// completion runs on every tab press and must stay silent.
func Labels(cfg config.Config, prefix string) []Task {
	tasks, err := List(cfg)
	if err != nil {
		return nil
	}
	needle := strings.ToLower(prefix)
	if needle == "" {
		return tasks
	}
	var matches []Task
	for _, t := range tasks {
		if strings.HasPrefix(strings.ToLower(t.Label()), needle) ||
			strings.HasPrefix(strings.ToLower(t.Name), needle) {
			matches = append(matches, t)
		}
	}
	return matches
}
