package workplan

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/repos"
)

// Report is what verification found. An empty Problems means the workplan is
// sound.
type Report struct {
	Tasks    int
	Problems []string
}

// OK reports whether the workplan passed every check.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Err returns the problems as one error, or nil when there are none.
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(r.Problems, "\n"))
}

// Verify checks a workplan three ways and reports everything wrong with it at
// once.
//
//	V1 schema      every task file parses and conforms, and is named for its id
//	V2 resolution  predecessors name tasks in this workplan, repositories exist
//	V3 acyclicity  the graph the predecessors imply has no cycle
//
// All three run even when an earlier one fails, because an author correcting a
// hand-written workplan wants the whole list rather than one problem per run. A
// task that failed V1 is excluded from V2 and V3, since a definition that did
// not parse has no predecessors to resolve.
func Verify(cfg config.Config, w Workplan) Report {
	var report Report

	// V1. Tasks collects the parse and naming failures as it reads.
	placed, errs := w.Tasks()
	for _, err := range errs {
		report.Problems = append(report.Problems, err.Error())
	}
	report.Tasks = len(placed)

	byID := make(map[string]Placed, len(placed))
	for _, p := range placed {
		if first, ok := byID[p.Task.ID]; ok {
			// The same id in two state directories makes its state ambiguous,
			// which would let the orchestrator move it twice.
			report.Problems = append(report.Problems,
				fmt.Sprintf("%s is in both %s and %s; a task has one state", p.Task.ID, first.State, p.State))
			continue
		}
		byID[p.Task.ID] = p
	}

	report.Problems = append(report.Problems, verifyResolution(cfg, w, placed, byID)...)
	report.Problems = append(report.Problems, verifyAcyclic(placed, byID)...)
	return report
}

// verifyResolution is V2: every predecessor names a task in this workplan, and
// every repository is a real checkout under the projects root.
func verifyResolution(cfg config.Config, w Workplan, placed []Placed, byID map[string]Placed) []string {
	var problems []string

	// Repositories are cached: a workplan of twenty tasks in three repositories
	// should stat each repository once, not twenty times.
	checked := make(map[string]bool)
	repoOK := func(name string) bool {
		if ok, seen := checked[name]; seen {
			return ok
		}
		dir := cfg.RepoPath(name)
		info, err := os.Stat(dir)
		ok := err == nil && info.IsDir() && repos.IsCheckout(dir)
		checked[name] = ok
		return ok
	}

	for _, p := range placed {
		for _, pred := range p.Task.Predecessors {
			if _, ok := byID[pred]; !ok {
				problems = append(problems,
					fmt.Sprintf("%s lists predecessor %q, which is not a task in this workplan", p.Task.ID, pred))
			}
		}
		for _, name := range p.Task.Repositories {
			if !repoOK(name) {
				problems = append(problems,
					fmt.Sprintf("%s lists repository %q, which is not a git checkout in %s",
						p.Task.ID, name, cfg.Projects()))
			}
		}
		// Every task gets an escalation directory at add time; a missing one
		// means an agent would have nowhere to write, so it is reported and
		// repaired rather than discovered later.
		dir := w.TaskEscalations(p.Task.ID)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				problems = append(problems, fmt.Sprintf("%s has no escalations directory: %v", p.Task.ID, mkErr))
			}
		}
	}
	return problems
}

// verifyAcyclic is V3. It names the members of each cycle it finds, because
// "there is a cycle" is not something an author can act on.
//
// Only edges to tasks that exist are followed: a predecessor naming nothing is
// V2's problem, and treating it as an edge here would report a second, confusing
// failure for the same mistake.
func verifyAcyclic(placed []Placed, byID map[string]Placed) []string {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[string]int, len(placed))

	// Deterministic order, so the same broken workplan reports the same cycle
	// every run rather than whichever one the map happened to yield first.
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var problems []string
	var stack []string

	// An explicit stack rather than recursion: a workplan is small, but a cycle
	// found deep in a long chain should report the cycle, not overflow.
	var walk func(id string)
	walk = func(id string) {
		state[id] = onStack
		stack = append(stack, id)

		for _, pred := range sortedPredecessors(byID[id].Task.Predecessors) {
			if _, exists := byID[pred]; !exists {
				continue // V2 reports this
			}
			switch state[pred] {
			case unvisited:
				walk(pred)
			case onStack:
				problems = append(problems, describeCycle(stack, pred))
			}
		}

		stack = stack[:len(stack)-1]
		state[id] = done
	}

	for _, id := range ids {
		if state[id] == unvisited {
			walk(id)
		}
	}
	return problems
}

// sortedPredecessors copies before sorting, so verification never reorders the
// author's own list on disk.
func sortedPredecessors(preds []string) []string {
	out := append([]string(nil), preds...)
	sort.Strings(out)
	return out
}

// describeCycle renders the cycle as the path that closes it, so an author can
// see which edge to cut: "03-c -> 05-e -> 03-c".
func describeCycle(stack []string, closes string) string {
	from := 0
	for i, id := range stack {
		if id == closes {
			from = i
			break
		}
	}
	path := append(append([]string(nil), stack[from:]...), closes)
	if len(path) == 2 {
		return fmt.Sprintf("%s depends on itself", closes)
	}
	return fmt.Sprintf("the predecessors form a cycle: %s", strings.Join(path, " -> "))
}
