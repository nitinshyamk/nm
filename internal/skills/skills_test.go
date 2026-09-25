package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The three skills the design specifies. Named here so removing one is a
// deliberate edit to this list rather than a file that quietly vanished.
//
// There is no separate refine skill: actioning review feedback is a phase of
// nm-task-execute, because it is the same task and the same worktree, and two
// skills meant two escalation paths to keep in step.
var expected = []string{
	"nm-task-execute",
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

// Nothing may be both bundled and retired. Install writes the bundle and then
// deletes the retired ones, so an overlap would delete a skill it had just written
// and report both actions for it — and the missing skill only surfaces later, in
// unattended work, as a slash command that does not resolve.
func TestNothingIsBothBundledAndRetired(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if contains(Retired, s.Name) {
			t.Errorf("%s is both embedded and listed as retired", s.Name)
		}
	}
}

// A retired skill has to be deleted, not merely left out of the bundle.
//
// This is the one that matters for nm-task-refine: a file still sitting in
// ~/.claude/skills is still invocable, and a retired skill is retired because
// something else covers its job now. What lingers is an older contradictory copy
// of live instructions — two escalation paths, two answers about the ready marker.
func TestInstallRemovesARetiredSkill(t *testing.T) {
	dir := t.TempDir()
	if len(Retired) == 0 {
		t.Skip("nothing is retired, so there is nothing to check")
	}
	stale := filepath.Join(dir, Retired[0], File)
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("%s survived the install (%v)", Retired[0], err)
	}
	var reported Result
	for _, r := range results {
		if r.Name == Retired[0] {
			reported = r
		}
	}
	if reported.Action != Removed {
		t.Errorf("%s was reported as %q, want removed", Retired[0], reported.Action)
	}
	if !reported.OK() {
		t.Error("a removed retired skill does not count as OK, so install would report failure")
	}
	// The live skills still landed: retiring one is not a reason to hold the rest.
	for _, name := range expected {
		if _, err := os.Stat(filepath.Join(dir, name, File)); err != nil {
			t.Errorf("%s was not installed alongside the removal: %v", name, err)
		}
	}
}

// A retired skill that was never installed is not worth a line of output. Every
// run would carry it otherwise, forever, on every fresh machine.
func TestInstallSaysNothingAboutAnAbsentRetiredSkill(t *testing.T) {
	dir := t.TempDir()

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != len(expected) {
		t.Errorf("Install reported %d results, want %d: %+v", len(results), len(expected), results)
	}
	for _, r := range results {
		if r.Action == Removed || r.Action == Kept {
			t.Errorf("%s was reported as %q though it was never installed", r.Name, r.Action)
		}
	}
}

// --dry-run must report the removal and not perform it.
func TestDryRunReportsARetiredSkillWithoutRemovingIt(t *testing.T) {
	dir := t.TempDir()
	if len(Retired) == 0 {
		t.Skip("nothing is retired")
	}
	stale := filepath.Join(dir, Retired[0], File)
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Install(InstallOptions{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("--dry-run removed %s: %v", Retired[0], err)
	}
	var found bool
	for _, r := range results {
		if r.Name == Retired[0] && r.Action == Removed {
			found = true
		}
	}
	if !found {
		t.Errorf("--dry-run did not say the retired skill would go: %+v", results)
	}
}

// A symlinked retired skill is reported and left, and must not block the install.
//
// Refusing the whole run over it would be the wrong trade: the live skills are what
// an unattended agent needs, and withholding all of them to protest one stale
// symlink turns a small mess into a stopped workplan. So it reports Kept, the live
// skills still land, and the command still succeeds.
func TestASymlinkedRetiredSkillIsKeptWithoutBlockingTheInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs privilege on Windows")
	}
	if len(Retired) == 0 {
		t.Skip("nothing is retired")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, Retired[0], File)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere.md")
	if err := os.WriteFile(target, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dest); err != nil {
		t.Skipf("symlink: %v", err)
	}

	results, err := Install(InstallOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	var kept Result
	for _, r := range results {
		if r.Name == Retired[0] {
			kept = r
		}
	}
	if kept.Action != Kept {
		t.Errorf("a symlinked retired skill was %q, want kept", kept.Action)
	}
	if kept.Reason == "" {
		t.Error("it was kept with no reason, so nobody learns why")
	}
	// OK() is what callers read to decide the skill ended up in the wanted state,
	// and a stale symlink is not that state — so Kept must not report OK.
	if kept.OK() {
		t.Error("a kept symlink reports OK, which hides a retired skill still in place")
	}
	// The link and its target survive.
	if _, err := os.Lstat(dest); err != nil {
		t.Errorf("the symlink was deleted: %v", err)
	}
	// And the live skills were still written: this is the part that must not block.
	for _, name := range expected {
		if _, err := os.Stat(filepath.Join(dir, name, File)); err != nil {
			t.Errorf("%s was not installed: %v", name, err)
		}
	}
}

// A conflict elsewhere holds the removal too. The rule is all-or-nothing, and a
// half-applied run is what that rule exists to prevent.
func TestAConflictHoldsBackTheRemovalToo(t *testing.T) {
	dir := t.TempDir()
	if len(Retired) == 0 {
		t.Skip("nothing is retired")
	}
	stale := filepath.Join(dir, Retired[0], File)
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "nm-task-execute", File)
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("my own version\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(InstallOptions{Dir: dir}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("the retired skill was removed while a conflict blocked the install: %v", err)
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

// The unattended skill must tell the agent never to stop and ask.
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
	unattended := map[string]bool{"nm-task-execute": true}

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

// nm-task-execute has to cover the review loop, because it is now the only skill
// that can.
//
// The orchestrator launches this one skill for both phases, so a body that lost
// the refine half would leave a task in review with a reviewer's comment and an
// agent that reads the acceptance criteria, finds them met, and stops. That
// failure is silent from the outside: the task keeps sitting in review.
func TestTaskExecuteCoversTheReviewLoop(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, s := range all {
		if s.Name == "nm-task-execute" {
			body = strings.ToLower(s.Body)
		}
	}
	if body == "" {
		t.Fatal("nm-task-execute is not embedded")
	}

	for _, want := range []struct{ what, substr string }{
		// How feedback is read at all.
		{"reading review comments", "tg pr comments"},
		// The assertion rule from O6, which is the one that must not be lost.
		{"advancing the ready marker", "ready-to-review.md"},
		// A second pull request is what a refine round must never open. `tg pr new`
		// fails on one, so an agent that tries it stalls on an error it cannot fix.
		{"the no-second-PR rule", "second pull request"},
		// One escalation path, reachable from both phases.
		{"the escalation command", "nm workplan await-resolution"},
	} {
		if !strings.Contains(body, want.substr) {
			t.Errorf("nm-task-execute says nothing about %s (no %q)", want.what, want.substr)
		}
	}

	// The skill is started identically for both phases, so it has to work out
	// which one it is from the task directory rather than from its prompt.
	if !strings.Contains(body, "where you are") {
		t.Error("nm-task-execute does not tell the agent to work out which phase it is in")
	}
}

// The skill must tell the agent to wait for review rather than exit, because it is
// the only agent the task gets.
//
// This is the whole of the one-agent design as the agent experiences it. Nothing
// starts a replacement, so a skill that lost these lines would leave every task
// sitting in review with a reviewer's comment and nobody reading it — and the
// orchestrator would report that as a dead agent rather than fixing it.
func TestTaskExecuteWaitsForReviewRatherThanExiting(t *testing.T) {
	body := skillBody(t, "nm-task-execute")

	for _, want := range []struct{ what, substr string }{
		// The blocking command, which is what makes staying alive affordable.
		{"the feedback wait", "nm workplan await-feedback"},
		// It must not exit when the wait times out; that is a resumable outcome.
		{"resuming after a timeout", "run it again"},
		// Polling in the model loop is the thing await-feedback exists to replace.
		{"not polling itself", "do not poll for feedback"},
		// The terminal verdicts, so the agent knows when it may stop.
		{"the approved verdict", "approved"},
		{"the merged verdict", "merged"},
	} {
		if !strings.Contains(body, want.substr) {
			t.Errorf("nm-task-execute says nothing about %s (no %q)", want.what, want.substr)
		}
	}
}

// Every pull request comment the agent leaves has to be attributable to an agent.
//
// A reviewer reads an unprefixed reply as a colleague's and may weigh it very
// differently from an unattended agent's, and a thread with both in it is unreadable
// if nobody can tell them apart.
func TestTaskExecutePrefixesItsPullRequestComments(t *testing.T) {
	body := skillBody(t, "nm-task-execute")

	if !strings.Contains(body, "nm-agent:") {
		t.Error("nm-task-execute does not require the nm-agent: prefix on its comments")
	}
	// Named as a rule, not only shown in an example a skimming agent may skip.
	if !strings.Contains(body, "without the `nm-agent:` prefix") {
		t.Error("the nm-agent: prefix is not stated as a rule in the Never list")
	}
}

// The orchestration skill must refuse to restart a task's agent, and hand the case
// to a human instead.
//
// It is the one actor in the system with both the means and the motive: it reads the
// problem saying a task has lost its agent, it can run `claude`, and "fix it" looks
// like helpfulness. Doing so would put a second agent in a worktree that may still
// hold the first, and whatever killed that one will very likely kill the replacement —
// so a restart loop hides a repeating failure behind apparent activity.
func TestWorkplanExecuteRefusesToRestartAgents(t *testing.T) {
	body := skillBody(t, "nm-workplan-execute")

	if !strings.Contains(body, "never restart a task's agent yourself") {
		t.Error("nm-workplan-execute does not forbid restarting a task's agent")
	}
	// The lost-agent cases have to be surfaced, or the prohibition leaves a task
	// silently stuck instead: in review, looking exactly like one awaiting a reviewer.
	for _, want := range []string{
		"nothing is actioning it",    // dead agent, reviewer waiting
		"stopped without escalating", // stalled agent
		"manual review",              // what to ask for
	} {
		if !strings.Contains(body, want) {
			t.Errorf("nm-workplan-execute does not surface %q", want)
		}
	}
}

// The orchestration skill must not tell the poller to watch for a message the
// orchestrator no longer emits.
//
// It used to quote the crashed-refine-round problem verbatim. That message went with
// the latch, and a skill watching for a string that can never appear is worse than
// one that says nothing: it reads as coverage of a case nobody is actually watching.
func TestWorkplanExecuteDoesNotQuoteRetiredProblems(t *testing.T) {
	body := skillBody(t, "nm-workplan-execute")

	for _, gone := range []string{"refine round started", "never finished"} {
		if strings.Contains(body, gone) {
			t.Errorf("nm-workplan-execute still watches for %q, which nm no longer prints", gone)
		}
	}
}

// skillBody returns one embedded skill's body, lowercased for substring checks.
func skillBody(t *testing.T, name string) string {
	t.Helper()
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Name == name {
			return strings.ToLower(s.Body)
		}
	}
	t.Fatalf("%s is not embedded", name)
	return ""
}
