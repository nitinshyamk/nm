package workplan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
)

// planWith builds a workplan whose planned/ directory holds the given tasks,
// and a projects root holding a checkout for every repository they name.
func planWith(t *testing.T, tasks ...Task) (config.Config, Workplan) {
	t.Helper()
	cfg := cfgAt(t)
	cfg.ProjectsRoot = filepath.Join(t.TempDir(), "projects")

	w, err := Define(cfg, Options{Name: "p", Now: fixedNow()})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	for _, task := range tasks {
		writeTask(t, w, Planned, task)
		for _, repo := range task.Repositories {
			makeCheckout(t, cfg, repo)
		}
	}
	return cfg, w
}

func writeTask(t *testing.T, w Workplan, s State, task Task) {
	t.Helper()
	blob, err := task.Encode()
	if err != nil {
		t.Fatalf("Encode(%s): %v", task.ID, err)
	}
	if err := os.WriteFile(w.TaskFile(s, task.ID), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.TaskEscalations(task.ID), 0o755); err != nil {
		t.Fatal(err)
	}
}

// makeCheckout creates something repos.IsCheckout accepts: a directory with a
// .git entry in it.
func makeCheckout(t *testing.T, cfg config.Config, name string) {
	t.Helper()
	dir := cfg.RepoPath(name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// task is a valid definition with the given id and predecessors, for graph tests
// where only the edges matter.
func task(id string, preds ...string) Task {
	return Task{
		ID:                 id,
		Predecessors:       preds,
		Description:        "Do " + id + ".",
		AcceptanceCriteria: []string{"It is done."},
	}
}

func TestVerifyAcceptsASoundWorkplan(t *testing.T) {
	cfg, w := planWith(t,
		task("01-first"),
		task("02-second", "01-first"),
		task("03-third", "01-first", "02-second"),
	)

	report := Verify(cfg, w)
	if !report.OK() {
		t.Fatalf("Verify rejected a sound workplan: %v", report.Problems)
	}
	if report.Tasks != 3 {
		t.Errorf("Tasks = %d, want 3", report.Tasks)
	}
}

func TestVerifyFindsAMissingPredecessor(t *testing.T) {
	cfg, w := planWith(t, task("02-second", "01-never-written"))

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted a predecessor that names nothing")
	}
	if !strings.Contains(report.Err().Error(), "01-never-written") {
		t.Errorf("problems %v do not name the missing predecessor", report.Problems)
	}
}

func TestVerifyFindsAMissingRepository(t *testing.T) {
	cfg, w := planWith(t)
	// Written directly, so planWith does not create a checkout for it.
	writeTask(t, w, Planned, Task{
		ID:                 "01-a",
		Repositories:       []string{"no-such-repo"},
		Description:        "d",
		AcceptanceCriteria: []string{"c"},
	})

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted a repository with no checkout")
	}
	if !strings.Contains(report.Err().Error(), "no-such-repo") {
		t.Errorf("problems %v do not name the missing repository", report.Problems)
	}
}

// A directory that exists but is not a git checkout is not a repository. Without
// the IsCheckout call this would pass on any directory that happened to exist.
func TestVerifyRejectsADirectoryThatIsNotACheckout(t *testing.T) {
	cfg, w := planWith(t)
	if err := os.MkdirAll(cfg.RepoPath("just-a-folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask(t, w, Planned, Task{
		ID:                 "01-a",
		Repositories:       []string{"just-a-folder"},
		Description:        "d",
		AcceptanceCriteria: []string{"c"},
	})

	if Verify(cfg, w).OK() {
		t.Fatal("Verify accepted a directory with no .git as a repository")
	}
}

func TestVerifyNamesTheCycle(t *testing.T) {
	cases := map[string]struct {
		tasks []Task
		want  []string
	}{
		"a two-cycle": {
			tasks: []Task{task("01-a", "02-b"), task("02-b", "01-a")},
			want:  []string{"01-a", "02-b"},
		},
		"a three-cycle": {
			tasks: []Task{task("01-a", "03-c"), task("02-b", "01-a"), task("03-c", "02-b")},
			want:  []string{"01-a", "02-b", "03-c"},
		},
		"a cycle hanging off a sound chain": {
			tasks: []Task{
				task("01-a"),
				task("02-b", "01-a", "04-d"),
				task("03-c", "02-b"),
				task("04-d", "03-c"),
			},
			want: []string{"02-b", "03-c", "04-d"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, w := planWith(t, tc.tasks...)
			report := Verify(cfg, w)
			if report.OK() {
				t.Fatalf("Verify accepted %s", name)
			}
			got := report.Err().Error()
			if !strings.Contains(got, "cycle") {
				t.Errorf("problems %v do not mention a cycle", report.Problems)
			}
			// Naming the members is the point: "there is a cycle" is not
			// something an author can act on.
			for _, id := range tc.want {
				if !strings.Contains(got, id) {
					t.Errorf("the cycle report %q does not name %s", got, id)
				}
			}
		})
	}
}

// A self-cycle is caught by the schema, before the graph is walked, and reported
// in terms the author wrote rather than as a graph path.
func TestVerifyCatchesASelfCycle(t *testing.T) {
	cfg, w := planWith(t)
	// Encode bypasses Validate, which is how a hand-edited file gets here.
	blob := []byte(`{"id":"01-a","predecessors":["01-a"],"repositories":[],"description":"d","acceptance-criteria":["c"]}`)
	if err := os.WriteFile(w.TaskFile(Planned, "01-a"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted a task that is its own predecessor")
	}
	if !strings.Contains(report.Err().Error(), "01-a") {
		t.Errorf("problems %v do not name the task", report.Problems)
	}
}

// Every node in a cycle of n nodes: the whole graph is one loop, so there is no
// sound task to anchor the walk.
func TestVerifyFindsACycleSpanningTheWholeGraph(t *testing.T) {
	const n = 6
	tasks := make([]Task, 0, n)
	for i := range n {
		id := fmt.Sprintf("%02d-t%d", i, i)
		pred := fmt.Sprintf("%02d-t%d", (i+n-1)%n, (i+n-1)%n)
		tasks = append(tasks, task(id, pred))
	}
	cfg, w := planWith(t, tasks...)

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatalf("Verify accepted a %d-node cycle", n)
	}
	got := report.Err().Error()
	for i := range n {
		id := fmt.Sprintf("%02d-t%d", i, i)
		if !strings.Contains(got, id) {
			t.Errorf("the cycle report %q does not name %s", got, id)
		}
	}
}

// A long chain must not be reported as a cycle, and must not exhaust anything.
func TestVerifyAcceptsALongChain(t *testing.T) {
	const n = 60
	tasks := make([]Task, 0, n)
	for i := range n {
		id := fmt.Sprintf("%02d-t%d", i, i)
		if i == 0 {
			tasks = append(tasks, task(id))
			continue
		}
		tasks = append(tasks, task(id, fmt.Sprintf("%02d-t%d", i-1, i-1)))
	}
	cfg, w := planWith(t, tasks...)

	if report := Verify(cfg, w); !report.OK() {
		t.Fatalf("Verify rejected a %d-task chain: %v", n, report.Problems)
	}
}

// A diamond is not a cycle: two tasks may share a predecessor and a successor.
func TestVerifyAcceptsADiamond(t *testing.T) {
	cfg, w := planWith(t,
		task("01-a"),
		task("02-b", "01-a"),
		task("03-c", "01-a"),
		task("04-d", "02-b", "03-c"),
	)
	if report := Verify(cfg, w); !report.OK() {
		t.Fatalf("Verify rejected a diamond: %v", report.Problems)
	}
}

func TestVerifyReportsAFileThatDoesNotParse(t *testing.T) {
	cfg, w := planWith(t, task("01-a"))
	if err := os.WriteFile(w.TaskFile(Planned, "02-broken"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted a file that does not parse")
	}
	if !strings.Contains(report.Err().Error(), "02-broken") {
		t.Errorf("problems %v do not name the broken file", report.Problems)
	}
	// The sound task is still counted, so one bad file does not hide the rest.
	if report.Tasks != 1 {
		t.Errorf("Tasks = %d, want the one task that did parse", report.Tasks)
	}
}

// The filename is the id. A mismatch means every lookup by id would miss the
// file, so it is reported rather than tolerated.
func TestVerifyReportsAFilenameThatIsNotItsID(t *testing.T) {
	cfg, w := planWith(t)
	blob, err := task("01-a").Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.TaskFile(Planned, "09-wrong-name"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted a file whose name is not its id")
	}
	if !strings.Contains(report.Err().Error(), "should be named 01-a.json") {
		t.Errorf("problems %v do not say what the file should be called", report.Problems)
	}
}

// One id in two states makes its state ambiguous, which would let the
// orchestrator transition it twice.
func TestVerifyReportsATaskInTwoStates(t *testing.T) {
	cfg, w := planWith(t, task("01-a"))
	writeTask(t, w, Review, task("01-a"))

	report := Verify(cfg, w)
	if report.OK() {
		t.Fatal("Verify accepted the same task in two state directories")
	}
	got := report.Err().Error()
	if !strings.Contains(got, "one state") {
		t.Errorf("problems %v do not explain the ambiguity", report.Problems)
	}
	for _, s := range []string{string(Planned), string(Review)} {
		if !strings.Contains(got, s) {
			t.Errorf("the report %q does not name the state %s", got, s)
		}
	}
}

// Verification must report a missing predecessor and a missing repository in one
// run, not stop at the first.
func TestVerifyReportsEveryProblemInOneRun(t *testing.T) {
	cfg, w := planWith(t)
	writeTask(t, w, Planned, Task{
		ID:                 "02-b",
		Predecessors:       []string{"01-never-written"},
		Repositories:       []string{"no-such-repo"},
		Description:        "d",
		AcceptanceCriteria: []string{"c"},
	})

	report := Verify(cfg, w)
	if len(report.Problems) < 2 {
		t.Fatalf("Verify reported %d problems, want both: %v", len(report.Problems), report.Problems)
	}
	got := report.Err().Error()
	for _, want := range []string{"01-never-written", "no-such-repo"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %v do not mention %q", report.Problems, want)
		}
	}
}

// Tasks read from every state directory, not only planned/: the orchestrator
// verifies a workplan that is already in flight.
func TestTasksReadsEveryStateDirectory(t *testing.T) {
	cfg, w := planWith(t)
	for i, s := range States() {
		writeTask(t, w, s, task(fmt.Sprintf("%02d-t%d", i, i)))
	}

	placed, errs := w.Tasks()
	if len(errs) != 0 {
		t.Fatalf("Tasks reported errors: %v", errs)
	}
	if len(placed) != len(States()) {
		t.Fatalf("Tasks found %d tasks, want one per state", len(placed))
	}
	for i, p := range placed {
		if want := States()[i]; p.State != want {
			t.Errorf("task %s is in %s, want %s", p.Task.ID, p.State, want)
		}
	}
	if report := Verify(cfg, w); !report.OK() {
		t.Errorf("Verify rejected tasks spread across states: %v", report.Problems)
	}
}

func TestFindTaskReportsTheState(t *testing.T) {
	_, w := planWith(t, task("01-a"))
	writeTask(t, w, Approved, task("02-b"))

	got, ok := w.FindTask("02-b")
	if !ok {
		t.Fatal("FindTask did not find 02-b")
	}
	if got.State != Approved {
		t.Errorf("State = %s, want approved", got.State)
	}
	if _, ok := w.FindTask("99-nope"); ok {
		t.Error("FindTask found a task that does not exist")
	}
}

// Verification repairs a missing escalation directory rather than only
// complaining: an agent with nowhere to write would fail at the worst moment.
func TestVerifyRestoresAMissingEscalationDirectory(t *testing.T) {
	cfg, w := planWith(t, task("01-a"))
	if err := os.RemoveAll(w.TaskEscalations("01-a")); err != nil {
		t.Fatal(err)
	}

	report := Verify(cfg, w)
	if !report.OK() {
		t.Fatalf("Verify: %v", report.Problems)
	}
	if info, err := os.Stat(w.TaskEscalations("01-a")); err != nil || !info.IsDir() {
		t.Errorf("Verify did not restore the escalation directory: %v", err)
	}
}

func TestVerifyOnAnEmptyWorkplan(t *testing.T) {
	cfg, w := planWith(t)
	report := Verify(cfg, w)
	if !report.OK() {
		t.Errorf("Verify rejected an empty workplan: %v", report.Problems)
	}
	if report.Tasks != 0 {
		t.Errorf("Tasks = %d, want 0", report.Tasks)
	}
}
