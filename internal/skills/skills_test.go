package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The four skills the design specifies. Named here so removing one is a
// deliberate edit to this list rather than a file that quietly vanished.
var expected = []string{
	"nm-task-execute",
	"nm-task-refine",
	"nm-work-plan",
	"nm-workplan-execute",
}

func TestAllEmbedsEverySkill(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != len(expected) {
		t.Fatalf("All returned %d skills, want %d", len(all), len(expected))
	}
	for i, want := range expected {
		if all[i].Name != want {
			t.Errorf("skill %d is %q, want %q", i, all[i].Name, want)
		}
		if !strings.HasPrefix(all[i].Body, "---\n") {
			t.Errorf("%s does not start with frontmatter", all[i].Name)
		}
	}
}

// Every skill needs name and description frontmatter, because that is what the
// client uses to decide whether a skill is relevant. A skill with no description
// is a skill nothing invokes.
func TestEverySkillHasFrontmatter(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		front, _, found := strings.Cut(strings.TrimPrefix(s.Body, "---\n"), "\n---")
		if !found {
			t.Errorf("%s has no closing frontmatter delimiter", s.Name)
			continue
		}
		if !strings.Contains(front, "name: "+s.Name) {
			t.Errorf("%s frontmatter does not declare name: %s", s.Name, s.Name)
		}
		if !strings.Contains(front, "description: ") {
			t.Errorf("%s has no description", s.Name)
		}
		// A description is what a client matches against, so a one-word one is
		// useless. This is a floor, not a style rule.
		for _, line := range strings.Split(front, "\n") {
			if desc, ok := strings.CutPrefix(line, "description: "); ok && len(desc) < 60 {
				t.Errorf("%s has a %d-character description; too short to match against", s.Name, len(desc))
			}
		}
	}
}

func TestInstallWritesEverySkill(t *testing.T) {
	dir := t.TempDir()

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != len(expected) {
		t.Fatalf("Install reported %d results, want %d", len(results), len(expected))
	}
	for _, r := range results {
		if r.Action != Installed {
			t.Errorf("%s: %s, want installed", r.Name, r.Action)
		}
		body, err := os.ReadFile(filepath.Join(dir, r.Name, File))
		if err != nil {
			t.Errorf("%s was not written: %v", r.Name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s was written empty", r.Name)
		}
	}
}

// Installing twice must be a no-op, so `mise run install` can run on every build.
func TestInstallIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(InstallOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	for _, r := range results {
		if r.Action != Unchanged {
			t.Errorf("%s: %s on a second install, want unchanged", r.Name, r.Action)
		}
	}
}

// A user's own edit is a conflict, not something to overwrite silently.
func TestInstallRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "nm-task-execute", File)
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("my own version\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	var conflicted bool
	for _, r := range results {
		if r.Name == "nm-task-execute" {
			conflicted = r.Action == Conflict
			if r.Reason == "" {
				t.Error("a conflict was reported with no reason")
			}
		}
	}
	if !conflicted {
		t.Fatalf("an edited skill was not reported as a conflict: %+v", results)
	}
	// The edit survives.
	body, err := os.ReadFile(mine)
	if err != nil || string(body) != "my own version\n" {
		t.Errorf("the user's own version was overwritten: %q, %v", body, err)
	}
	// And nothing else was written either: a half-updated set is worse than an
	// unchanged one, because the skills reference each other's commands.
	for _, name := range expected {
		if name == "nm-task-execute" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name, File)); err == nil {
			t.Errorf("%s was installed despite a conflict elsewhere in the set", name)
		}
	}
}

func TestInstallForceReplaces(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "nm-task-execute", File)
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("my own version\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Install(InstallOptions{Dir: dir, Force: true})
	if err != nil {
		t.Fatalf("Install --force: %v", err)
	}
	for _, r := range results {
		if !r.OK() {
			t.Errorf("%s: %s under --force", r.Name, r.Action)
		}
	}
	body, err := os.ReadFile(mine)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "my own version\n" {
		t.Error("--force did not replace the edited skill")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()

	results, err := Install(InstallOptions{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("Install --dry-run: %v", err)
	}
	if len(results) != len(expected) {
		t.Errorf("dry run reported %d results, want %d", len(results), len(expected))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run wrote %d entries", len(entries))
	}
}

// Following a symlink writes through to wherever it points, which is not a place
// the caller named. Refused even with --force.
func TestInstallRefusesASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink on Windows needs elevation")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte("elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "nm-task-execute", File)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	for _, force := range []bool{false, true} {
		results, err := Install(InstallOptions{Dir: dir, Force: force})
		if err != nil {
			t.Fatalf("Install(force=%v): %v", force, err)
		}
		for _, r := range results {
			if r.Name == "nm-task-execute" && r.Action != Refused {
				t.Errorf("a symlink was %s with force=%v, want refused", r.Action, force)
			}
		}
		body, err := os.ReadFile(target)
		if err != nil || string(body) != "elsewhere\n" {
			t.Fatalf("the symlink target was written through: %q, %v", body, err)
		}
	}
}

func TestCommandsFindsWhatTheSkillsInvoke(t *testing.T) {
	got, err := Commands()
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}

	// The commands the skills are built around. If one of these stops being
	// found, the extraction is broken and the drift test in package cli is
	// silently passing on an empty list.
	for _, want := range []string{
		"nm workplan define",
		"nm workplan add",
		"nm workplan verify",
		"nm workplan execute",
		"nm workplan resolve",
		"nm workplan await-resolution",
	} {
		if !contains(got, want) {
			t.Errorf("Commands did not find %q; found %v", want, got)
		}
	}
}

func TestCommandPath(t *testing.T) {
	cases := map[string]string{
		"nm workplan resolve <name>":                    "nm workplan resolve",
		"nm workplan define -n <name> -a <doc>":         "nm workplan define",
		"nm workplan await-resolution escalations/x.md": "nm workplan await-resolution",
		"nm workplan execute name --json":               "nm workplan execute name",
		"nm task list":                                  "nm task list",
		"tg pr comments 42":                             "",
		"nm":                                            "",
		"":                                              "",
		"the nm workplan command":                       "",
	}
	for in, want := range cases {
		if got := commandPath(in); got != want {
			t.Errorf("commandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	want := filepath.Join("/home/x", ".claude", "skills")
	if got := DefaultDir("/home/x"); got != want {
		t.Errorf("DefaultDir = %q, want %q", got, want)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// The two unattended skills must tell the agent never to stop and ask.
//
// This is the rule that cost a whole dogfood run: an agent that asks
// interactively is waiting on stdin nobody is attached to, so it is not reading
// files either and the resolution mechanism cannot reach it. Only `claude attach`
// recovers it, and from the outside the task looks like one being worked on. The
// rule is easy to soften back into "escalate and also surface it", so it is
// pinned here.
func TestUnattendedSkillsForbidInteractiveQuestions(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	unattended := map[string]bool{"nm-task-execute": true, "nm-task-refine": true}

	for _, s := range all {
		if !unattended[s.Name] {
			continue
		}
		body := strings.ToLower(s.Body)
		if !strings.Contains(body, "never stop to ask") &&
			!strings.Contains(body, "must never stop to ask") {
			t.Errorf("%s does not tell the agent never to stop and ask", s.Name)
		}
		if !strings.Contains(body, "no terminal") {
			t.Errorf("%s does not say why: there is no terminal attached", s.Name)
		}
		// The earlier wording told the agent to write a file *and* surface it the
		// normal way, which is what made the deadlock reachable.
		for _, banned := range []string{
			"surface it the normal way as well",
			"and surfaced as normal",
		} {
			if strings.Contains(body, banned) {
				t.Errorf("%s still says %q, which reintroduces the interactive path", s.Name, banned)
			}
		}
	}
}
