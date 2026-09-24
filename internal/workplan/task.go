package workplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// idOK is the shape of a task id: a leading number, then hyphen-joined
// lowercase words, as in 01-modify-contract-to-include-gdt.
//
// The leading number is what makes a directory listing read in the order the
// work was scoped. Lowercase is required rather than merely conventional,
// because the id becomes a filename and 01-Foo.json and 01-foo.json are the
// same file on Windows and macOS but not on Linux — accepting both would make a
// workplan mean different things on different machines.
var idOK = regexp.MustCompile(`^[0-9]+(-[a-z0-9]+)+$`)

// Task is one unit of work in a workplan.
//
// The JSON field names are the schema the author writes by hand, so
// acceptance-criteria is hyphenated rather than spelled the Go way.
type Task struct {
	ID                 string   `json:"id"`
	Predecessors       []string `json:"predecessors"`
	Repositories       []string `json:"repositories"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance-criteria"`
}

// ParseTask decodes one task definition, rejecting anything the schema does not
// describe.
//
// Unknown fields are an error rather than an ignored extra: the likeliest
// unknown field is a typo of a real one ("acceptance_criteria",
// "predecessor"), and silently dropping it would lose the very content that
// defines when the task is done.
func ParseTask(blob []byte) (Task, error) {
	dec := json.NewDecoder(bytes.NewReader(blob))
	dec.DisallowUnknownFields()

	var t Task
	if err := dec.Decode(&t); err != nil {
		return Task{}, fmt.Errorf("reading the task definition: %w", err)
	}
	// A second value in the same document is a sign of a concatenated file
	// rather than a task, and would otherwise be discarded unread.
	if dec.More() {
		return Task{}, fmt.Errorf("reading the task definition: more than one JSON value")
	}
	if err := t.Validate(); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Validate reports every way one task definition fails the schema, as a single
// error listing all of them.
//
// All of them, rather than the first: an author fixing a hand-written task wants
// the whole list, and returning one problem at a time turns a single correction
// into four round trips.
func (t Task) Validate() error {
	var problems []string

	switch {
	case t.ID == "":
		problems = append(problems, "id is empty")
	case !idOK.MatchString(t.ID):
		problems = append(problems,
			fmt.Sprintf("id %q must be a number then hyphen-joined lowercase words, as in 01-prepare-codebase", t.ID))
	}

	if strings.TrimSpace(t.Description) == "" {
		problems = append(problems, "description is empty")
	}

	// A task with no acceptance criteria has no definition of done, so an agent
	// told to work until they are met would never stop.
	if len(t.AcceptanceCriteria) == 0 {
		problems = append(problems, "acceptance-criteria is empty; a task with no criteria has no definition of done")
	}
	for i, c := range t.AcceptanceCriteria {
		if strings.TrimSpace(c) == "" {
			problems = append(problems, fmt.Sprintf("acceptance-criteria[%d] is empty", i))
		}
	}

	for i, p := range t.Predecessors {
		if strings.TrimSpace(p) == "" {
			problems = append(problems, fmt.Sprintf("predecessors[%d] is empty", i))
			continue
		}
		if !idOK.MatchString(p) {
			problems = append(problems, fmt.Sprintf("predecessors[%d] %q is not a task id", i, p))
		}
		if p == t.ID {
			problems = append(problems, fmt.Sprintf("%s lists itself as a predecessor", t.ID))
		}
	}
	if dup := firstDuplicate(t.Predecessors); dup != "" {
		problems = append(problems, fmt.Sprintf("predecessor %q is listed twice", dup))
	}

	for i, r := range t.Repositories {
		if strings.TrimSpace(r) == "" {
			problems = append(problems, fmt.Sprintf("repositories[%d] is empty", i))
		}
	}
	if dup := firstDuplicate(t.Repositories); dup != "" {
		problems = append(problems, fmt.Sprintf("repository %q is listed twice", dup))
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return v
		}
		seen[v] = struct{}{}
	}
	return ""
}

// Encode renders a task definition as the JSON that goes on disk.
func (t Task) Encode() ([]byte, error) {
	// Neither list is omitempty in the struct, so a task with no predecessors
	// writes an explicit [] rather than nothing. The file is read by humans as
	// well as by nm, and an absent key reads as an oversight where an empty list
	// reads as a decision.
	if t.Predecessors == nil {
		t.Predecessors = []string{}
	}
	if t.Repositories == nil {
		t.Repositories = []string{}
	}
	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding task %s: %w", t.ID, err)
	}
	return append(blob, '\n'), nil
}

// LoadTask reads one task definition from a file.
func LoadTask(path string) (Task, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	t, err := ParseTask(blob)
	if err != nil {
		return Task{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	// The filename is the id: a mismatch means a file was renamed or copied, and
	// every lookup by id would then miss it.
	if want := t.ID + ".json"; filepath.Base(path) != want {
		return Task{}, fmt.Errorf("%s: holds task %q, so the file should be named %s",
			filepath.Base(path), t.ID, want)
	}
	return t, nil
}

// Placed is a task definition together with the state it was found in.
type Placed struct {
	Task  Task
	State State
}

// Tasks reads every task definition in a workplan, from every state directory.
//
// A file that does not parse is returned as an error rather than skipped: an
// unreadable task is a hole in the graph, and verify has to be able to say so.
func (w Workplan) Tasks() ([]Placed, []error) {
	var found []Placed
	var errs []error

	for _, s := range States() {
		dir := w.StateDir(s)
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("reading %s: %w", dir, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			t, err := LoadTask(filepath.Join(dir, e.Name()))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s, err))
				continue
			}
			found = append(found, Placed{Task: t, State: s})
		}
	}

	sort.SliceStable(found, func(i, j int) bool { return found[i].Task.ID < found[j].Task.ID })
	return found, errs
}

// FindTask returns one task and the state it is in.
func (w Workplan) FindTask(id string) (Placed, bool) {
	placed, _ := w.Tasks()
	for _, p := range placed {
		if p.Task.ID == id {
			return p, true
		}
	}
	return Placed{}, false
}
