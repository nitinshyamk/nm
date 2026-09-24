package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
)

// runNM executes the real command tree against a temporary configuration, so a
// test drives nm the way a shell does without touching the user's directories.
func runNM(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}

// isolatedConfig points every root at a temporary directory and returns the
// configuration the commands under test will load.
func isolatedConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ProjectsRoot = filepath.Join(dir, "repos")
	cfg.TasksRoot = filepath.Join(dir, "tasks")
	cfg.WorkplansRoot = filepath.Join(dir, "workplans")
	cfg.WorktreesRoot = filepath.Join(dir, "worktrees")

	blob, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".nm.json")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvPath, path)
	return cfg
}

func TestWorkplanDefineCreatesTheDirectories(t *testing.T) {
	cfg := isolatedConfig(t)

	out, err := runNM(t, "workplan", "define", "-n", "build-the-thing")
	if err != nil {
		t.Fatalf("define: %v\n%s", err, out)
	}

	dir := filepath.Join(cfg.Workplans(), "build-the-thing")
	for _, name := range []string{"planned", "in-progress", "review", "approved", "completed", "escalations", "artifacts"} {
		if info, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil || !info.IsDir() {
			t.Errorf("%s/ is missing: %v", name, statErr)
		}
	}
	if !strings.Contains(out, dir) {
		t.Errorf("define did not print where it created the workplan:\n%s", out)
	}
}

func TestWorkplanDefineNeedsAName(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define"); err == nil {
		t.Fatal("define without -n succeeded")
	}
}

func TestWorkplanDefineCopiesArtifacts(t *testing.T) {
	cfg := isolatedConfig(t)
	src := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(src, []byte("the design"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runNM(t, "workplan", "define", "-n", "p", "-a", src); err != nil {
		t.Fatalf("define -a: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.Workplans(), "p", "artifacts", "design.md"))
	if err != nil {
		t.Fatalf("the artifact was not copied: %v", err)
	}
	if string(got) != "the design" {
		t.Errorf("artifact = %q", got)
	}
}

const addBody = `{
  "id": "01-prepare-codebase",
  "predecessors": [],
  "repositories": [],
  "description": "Get the codebase ready.",
  "acceptance-criteria": ["It is ready."]
}`

func TestWorkplanAddFromAFile(t *testing.T) {
	cfg := isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(file, []byte(addBody), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runNM(t, "workplan", "add", "p", "--content", "@"+file)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(cfg.Workplans(), "p", "planned", "01-prepare-codebase.json")); err != nil {
		t.Errorf("the task was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Workplans(), "p", "escalations", "01-prepare-codebase")); err != nil {
		t.Errorf("the escalation directory was not created: %v", err)
	}
	// A task with no repositories should say so rather than printing nothing,
	// since an empty list is a deliberate choice in this schema.
	if !strings.Contains(out, "no code changes expected") {
		t.Errorf("add did not explain the empty repository list:\n%s", out)
	}
}

// Inline JSON has to work too, for a one-line task typed by hand.
func TestWorkplanAddInline(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p", "--content", addBody); err != nil {
		t.Fatalf("add inline: %v", err)
	}
}

func TestWorkplanAddNeedsContent(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p"); err == nil {
		t.Fatal("add without --content succeeded")
	}
}

func TestWorkplanAddRejectsABadDefinition(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}

	_, err := runNM(t, "workplan", "add", "p", "--content", `{"id":"Bad Id","description":"d","acceptance-criteria":["c"]}`)
	if err == nil {
		t.Fatal("add accepted a task with an invalid id")
	}
}

func TestWorkplanAddRefusesADuplicate(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p", "--content", addBody); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p", "--content", addBody); err == nil {
		t.Fatal("add overwrote an existing task")
	}
}

func TestWorkplanVerifyReportsASoundPlan(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p", "--content", addBody); err != nil {
		t.Fatal(err)
	}

	out, err := runNM(t, "workplan", "verify", "p")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sound") || !strings.Contains(out, "1 task") {
		t.Errorf("verify output does not report a sound plan:\n%s", out)
	}
}

// verify must exit non-zero so it can gate a script, and must name the problem
// rather than only saying something is wrong.
func TestWorkplanVerifyFailsAndNamesTheCycle(t *testing.T) {
	cfg := isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	planned := filepath.Join(cfg.Workplans(), "p", "planned")
	for _, tc := range []struct{ id, pred string }{{"01-a", "02-b"}, {"02-b", "01-a"}} {
		body := `{"id":"` + tc.id + `","predecessors":["` + tc.pred + `"],"repositories":[],` +
			`"description":"d","acceptance-criteria":["c"]}`
		if err := os.WriteFile(filepath.Join(planned, tc.id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runNM(t, "workplan", "verify", "p")
	if err == nil {
		t.Fatal("verify exited zero on a cyclic workplan")
	}
	if !strings.Contains(out, "cycle") {
		t.Errorf("verify did not report the cycle:\n%s", out)
	}
	for _, id := range []string{"01-a", "02-b"} {
		if !strings.Contains(out, id) {
			t.Errorf("verify did not name %s:\n%s", id, out)
		}
	}
}

func TestWorkplanVerifyOnAnUnknownWorkplan(t *testing.T) {
	isolatedConfig(t)
	if _, err := runNM(t, "workplan", "verify", "nope"); err == nil {
		t.Fatal("verify succeeded on a workplan that does not exist")
	}
}

func TestWorkplanListCountsByState(t *testing.T) {
	cfg := isolatedConfig(t)
	if _, err := runNM(t, "workplan", "define", "-n", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := runNM(t, "workplan", "add", "p", "--content", addBody); err != nil {
		t.Fatal(err)
	}
	// A second task, already in review, so the listing has two states to show.
	body := `{"id":"02-b","predecessors":[],"repositories":[],"description":"d","acceptance-criteria":["c"]}`
	if err := os.WriteFile(filepath.Join(cfg.Workplans(), "p", "review", "02-b.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runNM(t, "workplan", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"p", "2 tasks", "planned", "review"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output is missing %q:\n%s", want, out)
		}
	}
}

func TestWorkplanListWithNoWorkplans(t *testing.T) {
	isolatedConfig(t)
	out, err := runNM(t, "workplan", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "no workplans") || !strings.Contains(out, "define") {
		t.Errorf("list should say there are none and how to make one:\n%s", out)
	}
}

func TestReadContent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(file, []byte("from a file"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		value string
		stdin string
		want  string
	}{
		"inline":    {value: "inline json", want: "inline json"},
		"from file": {value: "@" + file, want: "from a file"},
		"stdin":     {value: "-", stdin: "from stdin", want: "from stdin"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := readContent(strings.NewReader(tc.stdin), tc.value)
			if err != nil {
				t.Fatalf("readContent: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("readContent = %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := readContent(strings.NewReader(""), "@"+filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("readContent accepted a file that does not exist")
	}
}

func TestPlural(t *testing.T) {
	cases := map[int]string{0: "0 tasks", 1: "1 task", 2: "2 tasks"}
	for n, want := range cases {
		if got := plural(n, "task", "tasks"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}

// `nm task new` with no repositories and no --taskfile is a command line that
// lost its arguments, and must say so rather than making an empty task.
func TestTaskNewStillRequiresARepository(t *testing.T) {
	isolatedConfig(t)
	out, err := runNM(t, "task", "new", "-n", "empty")
	if err == nil {
		t.Fatalf("task new with no repositories succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "taskfile") {
		t.Errorf("the error should mention the --taskfile alternative: %v", err)
	}
}

// --no-agent is what makes a task with a definition inspectable before it is
// handed to an agent, and is how the tests below avoid launching real ones.
func TestTaskNewWithTaskfileAndNoAgent(t *testing.T) {
	cfg := isolatedConfig(t)
	def := filepath.Join(t.TempDir(), "01-a.json")
	body := `{"id":"01-a","predecessors":[],"repositories":[],` +
		`"description":"Clarify something.","acceptance-criteria":["Recorded."]}`
	if err := os.WriteFile(def, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runNM(t, "task", "new", "-n", "01-a", "--taskfile", def, "--no-agent")
	if err != nil {
		t.Fatalf("task new --taskfile: %v\n%s", err, out)
	}

	// A zero-repository task is a real case, and must say so rather than looking
	// like a mistake.
	if !strings.Contains(out, "no repositories") {
		t.Errorf("output does not mention the empty repository list:\n%s", out)
	}
	// The generated prompt is shown, so --no-agent is actionable rather than a
	// dead end.
	if !strings.Contains(out, "/nm-task-execute the task in input/01-a.json") {
		t.Errorf("output does not show the generated prompt:\n%s", out)
	}

	entries, err := os.ReadDir(cfg.Tasks())
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one task directory, got %v (%v)", entries, err)
	}
	dir := filepath.Join(cfg.Tasks(), entries[0].Name())
	for _, rel := range []string{"input/01-a.json", "escalations", "artifacts", "scratch"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s is missing: %v", rel, err)
		}
	}
}
