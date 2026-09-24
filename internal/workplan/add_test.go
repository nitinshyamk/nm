package workplan

import (
	"os"
	"strings"
	"testing"
)

func TestAddTaskWritesTheFileAndTheEscalationDirectory(t *testing.T) {
	cfg, w := planWith(t)

	if err := w.AddTask(task("01-a")); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if _, err := os.Stat(w.TaskFile(Planned, "01-a")); err != nil {
		t.Errorf("the task file is missing: %v", err)
	}
	if info, err := os.Stat(w.TaskEscalations("01-a")); err != nil || !info.IsDir() {
		t.Errorf("the escalation directory is missing: %v", err)
	}
	// What was written must be what Verify accepts, or add and verify disagree.
	if report := Verify(cfg, w); !report.OK() {
		t.Errorf("a task written by AddTask does not verify: %v", report.Problems)
	}
}

func TestAddTaskRefusesADuplicateID(t *testing.T) {
	_, w := planWith(t)
	if err := w.AddTask(task("01-a")); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	err := w.AddTask(task("01-a"))
	if err == nil {
		t.Fatal("AddTask overwrote an existing task")
	}
	if !strings.Contains(err.Error(), "already in this workplan") {
		t.Errorf("error = %q, want it to say the task is already there", err)
	}
}

// A task already in flight must not be re-addable as planned: that would put the
// same id in two states and make its state ambiguous.
func TestAddTaskRefusesAnIDThatIsInAnotherState(t *testing.T) {
	_, w := planWith(t)
	writeTask(t, w, Review, task("01-a"))

	err := w.AddTask(task("01-a"))
	if err == nil {
		t.Fatal("AddTask added a task that is already in review")
	}
	if !strings.Contains(err.Error(), string(Review)) {
		t.Errorf("error = %q, want it to name the state the task is in", err)
	}
}

func TestAddTaskRejectsAnInvalidDefinition(t *testing.T) {
	_, w := planWith(t)

	err := w.AddTask(Task{ID: "Bad Id", Description: "d", AcceptanceCriteria: []string{"c"}})
	if err == nil {
		t.Fatal("AddTask accepted an invalid definition")
	}
	// Nothing is left behind by a rejected add.
	if entries, readErr := os.ReadDir(w.StateDir(Planned)); readErr != nil || len(entries) != 0 {
		t.Errorf("a rejected AddTask wrote something: %v, %v", entries, readErr)
	}
}

func TestResolveFindsAWorkplanByNameAndPrefix(t *testing.T) {
	cfg := cfgAt(t)
	for _, name := range []string{"build-the-thing", "build-something-else", "other"} {
		if _, err := Define(cfg, Options{Name: name}); err != nil {
			t.Fatalf("Define(%s): %v", name, err)
		}
	}

	// An unambiguous prefix resolves.
	got, err := Resolve(cfg, "other")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "other" {
		t.Errorf("Name = %q, want other", got.Name)
	}

	// An exact name wins even when it is a prefix of nothing else.
	if got, err = Resolve(cfg, "build-the-thing"); err != nil {
		t.Fatalf("Resolve exact: %v", err)
	}
	if got.Name != "build-the-thing" {
		t.Errorf("Name = %q", got.Name)
	}

	// An ambiguous prefix names the candidates rather than guessing.
	_, err = Resolve(cfg, "build")
	if err == nil {
		t.Fatal("Resolve picked one of two matching workplans")
	}
	for _, want := range []string{"build-the-thing", "build-something-else"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the candidate %s", err, want)
		}
	}

	if _, err := Resolve(cfg, "nope"); err == nil {
		t.Error("Resolve found a workplan that does not exist")
	}
}

// An exact match must win over a prefix, so a workplan whose name is a prefix of
// another is still reachable by typing it in full.
func TestResolvePrefersAnExactMatch(t *testing.T) {
	cfg := cfgAt(t)
	for _, name := range []string{"plan", "plan-two"} {
		if _, err := Define(cfg, Options{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Resolve(cfg, "plan")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "plan" {
		t.Errorf("Name = %q, want the exact match plan", got.Name)
	}
}

func TestNamesIsSilentOnAMissingRoot(t *testing.T) {
	cfg := cfgAt(t)
	if got := Names(cfg, ""); len(got) != 0 {
		t.Errorf("Names on a missing root = %v, want none", got)
	}
}

func TestCoerce(t *testing.T) {
	cases := map[string]string{
		"Build The Thing":      "build-the-thing",
		"build_the_thing":      "build_the_thing",
		"  spaced  out  ":      "spaced-out",
		"Already-Fine":         "already-fine",
		"my/plan":              "my-plan",
		`my\plan`:              "my-plan",
		"v1.2 migration":       "v1.2-migration",
		`"quoted plan"`:        "quoted-plan",
		"-leading-dash-":       "leading-dash",
		"double--dash":         "double-dash",
		"Workplan: The Sequel": "workplan-the-sequel",
		"01 first":             "01-first",
	}
	for in, want := range cases {
		got, err := Coerce(in)
		if err != nil {
			t.Errorf("Coerce(%q) = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Coerce(%q) = %q, want %q", in, got, want)
		}
		// Whatever comes out must be usable, or coercion has not done its job.
		if err := ValidateName(got); err != nil {
			t.Errorf("Coerce(%q) produced %q, which ValidateName rejects: %v", in, got, err)
		}
	}
}

// A name with nothing usable in it is an error rather than an invented one: the
// workplan directory is where the author looks for their work.
func TestCoerceRefusesToInventAName(t *testing.T) {
	for _, in := range []string{"", "   ", "!!!", "---", "...", "🙂"} {
		if got, err := Coerce(in); err == nil {
			t.Errorf("Coerce(%q) = %q, want an error", in, got)
		}
	}
}
