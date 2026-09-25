package workplan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
)

// fakeAgents records what was launched and answers liveness from a set the test
// controls, so the latch can be exercised without starting anything.
type fakeAgents struct {
	launched []string // prompts, in order
	names    []string // session names, in order, parallel to launched
	dirs     []string
	next     int
	live     map[string]bool
	stalled  map[string]bool
	failWith error
}

func (f *fakeAgents) Launch(dir, name, prompt string, _ []string) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}
	f.launched = append(f.launched, prompt)
	f.names = append(f.names, name)
	f.dirs = append(f.dirs, dir)
	f.next++
	id := fmt.Sprintf("agent%d", f.next)
	if f.live == nil {
		f.live = map[string]bool{}
	}
	f.live[id] = true
	return id, nil
}

func (f *fakeAgents) Alive(id, _ string) bool { return f.live[id] }

func (f *fakeAgents) Stalled(id, _ string) bool { return f.stalled[id] }

// count returns how many agents were launched with a prompt naming the skill.
func (f *fakeAgents) count(skill string) int {
	n := 0
	for _, p := range f.launched {
		if strings.Contains(p, skill) {
			n++
		}
	}
	return n
}

// refines counts refine rounds, which is the session name and no longer the
// prompt: one skill both does the work and actions the feedback on it, so every
// launch names /nm-task-execute and only `nm-refine-…` says which phase it is.
func (f *fakeAgents) refines() int {
	n := 0
	for _, name := range f.names {
		if strings.HasPrefix(name, "nm-refine-") {
			n++
		}
	}
	return n
}

// starts counts launches that began a task rather than a refine round.
func (f *fakeAgents) starts() int { return len(f.names) - f.refines() }

// fakeReview answers per-branch, so a multi-repo task can have one approved pull
// request and one not.
type fakeReview struct {
	byBranch map[string]*forge.Status
	merged   []int
	mergeErr error
}

func (f *fakeReview) StatusForBranch(_, branch string) (*forge.Status, error) {
	return f.byBranch[branch], nil
}

func (f *fakeReview) Merge(_ string, number int, _ string) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = append(f.merged, number)
	return nil
}

// harness is a workplan with real git repositories behind it, so tasks can
// actually be created and branched.
type harness struct {
	cfg    config.Config
	w      Workplan
	agents *fakeAgents
	review *fakeReview
	now    time.Time
}

func newHarness(t *testing.T, repos ...string) *harness {
	t.Helper()
	cfg := taskEnv(t, repos...)
	cfg.WorkplansRoot = filepath.Join(filepath.Dir(cfg.Tasks()), "workplans")

	w, err := Define(cfg, Options{Name: "p", Now: fixedNow()})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	return &harness{
		cfg:    cfg,
		w:      w,
		agents: &fakeAgents{live: map[string]bool{}, stalled: map[string]bool{}},
		review: &fakeReview{byBranch: map[string]*forge.Status{}},
		now:    fixedNow()(),
	}
}

func (h *harness) add(t *testing.T, task Task) {
	t.Helper()
	if err := h.w.AddTask(task); err != nil {
		t.Fatalf("AddTask(%s): %v", task.ID, err)
	}
}

func (h *harness) run(t *testing.T, merge bool) Result {
	t.Helper()
	result, err := Execute(h.w, ExecuteOptions{
		Config: h.cfg,
		Review: h.review,
		Agents: h.agents,
		Now:    func() time.Time { return h.now },
		Merge:  merge,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

// stateOf reads a task's state off the filesystem, which is where it lives.
func (h *harness) stateOf(t *testing.T, id string) State {
	t.Helper()
	placed, ok := h.w.FindTask(id)
	if !ok {
		t.Fatalf("task %s is in no state directory", id)
	}
	return placed.State
}

func (h *harness) taskFor(t *testing.T, id string) task.Task {
	t.Helper()
	tasks, err := task.List(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, found := range tasks {
		if found.TaskID == id {
			return found
		}
	}
	t.Fatalf("no task directory for %s", id)
	return task.Task{}
}

// markReady writes the ready-to-review marker the way an agent would.
func (h *harness) markReady(t *testing.T, id string, at time.Time) {
	t.Helper()
	found := h.taskFor(t, id)
	if err := os.MkdirAll(found.Escalations(h.cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("# Ready for review\n\n%s\n", at.Format(time.RFC3339))
	if err := os.WriteFile(found.Ready(h.cfg), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// approve makes every one of a task's branches read as approved.
func (h *harness) approve(t *testing.T, id string, number int) {
	t.Helper()
	for _, repo := range h.taskFor(t, id).Repos {
		h.review.byBranch[repo.Branch] = &forge.Status{
			Number: number, URL: fmt.Sprintf("https://example.invalid/pull/%d", number),
			State: forge.StateOpen, Decision: forge.DecisionApproved, Mergeable: forge.MergeableYes,
		}
	}
}

// comment puts feedback on every one of a task's branches at the given time.
func (h *harness) comment(t *testing.T, id string, at time.Time) {
	t.Helper()
	for _, repo := range h.taskFor(t, id).Repos {
		h.review.byBranch[repo.Branch] = &forge.Status{
			Number: 1, URL: "https://example.invalid/pull/1", State: forge.StateOpen,
			Mergeable: forge.MergeableYes,
			Comments:  []forge.Comment{{Author: "reviewer", CreatedAt: at}},
		}
	}
}

func TestStartsATaskWithNoPredecessors(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, Task{
		ID: "01-a", Repositories: []string{"nm"},
		Description: "Do it.", AcceptanceCriteria: []string{"Done."},
	})

	result := h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != InProgress {
		t.Errorf("state = %s, want in-progress", got)
	}
	if len(result.Transitions) != 1 {
		t.Fatalf("transitions = %v, want one", result.Transitions)
	}
	if h.agents.starts() != 1 || h.agents.count("/nm-task-execute") != 1 {
		t.Errorf("launched %v as %v, want one nm-task-execute start",
			h.agents.launched, h.agents.names)
	}
	// The task directory is a real task, discoverable like any other.
	found := h.taskFor(t, "01-a")
	if found.Workplan != "p" || found.TaskFile != "01-a.json" {
		t.Errorf("the task lost its workplan link: %+v", found)
	}
	if _, err := os.Stat(filepath.Join(found.Input(h.cfg), "01-a.json")); err != nil {
		t.Errorf("the definition was not copied: %v", err)
	}
}

// The eligibility rule, at its boundaries. One predecessor starts once it reaches
// review; two or more wait for all of them to complete.
func TestEligibilityBoundaries(t *testing.T) {
	cases := map[string]struct {
		predStates map[string]State
		preds      []string
		wantStart  bool
	}{
		"no predecessors":        {preds: nil, wantStart: true},
		"one, still in progress": {preds: []string{"01-a"}, predStates: map[string]State{"01-a": InProgress}, wantStart: false},
		// Review is the relaxed gate: the predecessor's branch exists, so a
		// successor can stack on it without waiting for a human.
		"one, in review":               {preds: []string{"01-a"}, predStates: map[string]State{"01-a": Review}, wantStart: true},
		"one, approved":                {preds: []string{"01-a"}, predStates: map[string]State{"01-a": Approved}, wantStart: true},
		"one, completed":               {preds: []string{"01-a"}, predStates: map[string]State{"01-a": Completed}, wantStart: true},
		"two, both approved":           {preds: []string{"01-a", "02-b"}, predStates: map[string]State{"01-a": Approved, "02-b": Approved}, wantStart: false},
		"two, one completed":           {preds: []string{"01-a", "02-b"}, predStates: map[string]State{"01-a": Completed, "02-b": Approved}, wantStart: false},
		"two, both completed":          {preds: []string{"01-a", "02-b"}, predStates: map[string]State{"01-a": Completed, "02-b": Completed}, wantStart: true},
		"three, two completed one not": {preds: []string{"01-a", "02-b", "03-c"}, predStates: map[string]State{"01-a": Completed, "02-b": Completed, "03-c": Review}, wantStart: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			// Predecessors are placed directly in their states, with no
			// repositories, so this test is about the rule and nothing else.
			for id, state := range tc.predStates {
				writeTask(t, h.w, state, def(id))
			}
			h.add(t, def("09-target", tc.preds...))

			h.run(t, false)

			got := h.stateOf(t, "09-target")
			want := Planned
			if tc.wantStart {
				want = InProgress
			}
			if got != want {
				t.Errorf("state = %s, want %s", got, want)
			}
		})
	}
}

// THE test for §5.5.1: a task in review with actionable feedback must launch
// exactly one refine agent, no matter how many passes run while it works.
func TestRefineLaunchesExactlyOneAgentAcrossManyPasses(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, Task{
		ID: "01-a", Repositories: []string{"nm"},
		Description: "Do it.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false) // start it

	published := h.now
	h.markReady(t, "01-a", published)
	h.run(t, false) // in-progress -> review

	// A reviewer comments after the work was published.
	h.comment(t, "01-a", published.Add(time.Minute))

	for pass := range 10 {
		h.now = h.now.Add(time.Minute)
		result := h.run(t, false)
		if pass > 0 && len(result.Refines) != 0 {
			t.Errorf("pass %d started another refine round: %v", pass+1, result.Refines)
		}
	}

	if got := h.agents.refines(); got != 1 {
		t.Fatalf("launched %d refine agents across ten passes, want exactly 1:\n%v",
			got, h.agents.names)
	}
	// And it stayed in review throughout, which is why nothing latched it.
	if got := h.stateOf(t, "01-a"); got != Review {
		t.Errorf("state = %s, want review", got)
	}
}

// A refine round launches the one task skill, with the ordinary task-file prompt.
//
// Actioning feedback is a phase of /nm-task-execute rather than a skill of its
// own, so there is nothing else to launch — and the prompt must not try to say
// which phase to run, because the skill reads that off the task directory. A
// prompt naming a skill that is no longer installed fails mid-round, unattended.
func TestARefineRoundLaunchesTheTaskSkill(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	published := h.now
	h.markReady(t, "01-a", published)
	h.run(t, false)
	h.comment(t, "01-a", published.Add(time.Minute))

	h.now = h.now.Add(time.Minute)
	h.run(t, false)

	if got := h.agents.refines(); got != 1 {
		t.Fatalf("refine agents = %d, want 1", got)
	}
	// Both launches — the start and the refine round — say the same thing.
	want := task.TaskFilePrompt(h.cfg, h.taskFor(t, "01-a"))
	for i, prompt := range h.agents.launched {
		if prompt != want {
			t.Errorf("launch %d (%s) prompted %q, want %q", i, h.agents.names[i], prompt, want)
		}
	}
	if strings.Contains(strings.Join(h.agents.launched, "\n"), "nm-task-refine") {
		t.Error("a launch still names nm-task-refine, which is no longer installed")
	}
}

// A refine agent that died without finishing must not be silently replaced: a
// repeating failure hidden behind apparent activity is the worst outcome.
func TestRefineReportsACrashedRoundRatherThanRelaunching(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	published := h.now
	h.markReady(t, "01-a", published)
	h.run(t, false)
	h.comment(t, "01-a", published.Add(time.Minute))

	h.now = h.now.Add(time.Minute)
	h.run(t, false) // starts the round
	if got := h.agents.refines(); got != 1 {
		t.Fatalf("refine agents = %d, want 1", got)
	}

	// The agent dies without advancing ready-to-review.
	h.agents.live = map[string]bool{}
	h.now = h.now.Add(10 * time.Minute)
	result := h.run(t, false)

	if got := h.agents.refines(); got != 1 {
		t.Errorf("a dead refine round was silently relaunched (%d agents)", got)
	}
	if len(result.Problems) == 0 {
		t.Fatal("a crashed refine round was not reported")
	}
	joined := strings.Join(result.Problems, "\n")
	if !strings.Contains(joined, "never finished") || !strings.Contains(joined, task.RefiningFile) {
		t.Errorf("the problem does not say what happened or what to do:\n%s", joined)
	}
}

// A finished round advances the timestamp, which releases the latch and leaves
// the task waiting on a reviewer again.
func TestRefineLatchClearsWhenTheRoundFinishes(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	published := h.now
	h.markReady(t, "01-a", published)
	h.run(t, false)
	h.comment(t, "01-a", published.Add(time.Minute))

	h.now = h.now.Add(2 * time.Minute)
	h.run(t, false) // round starts

	// The agent finishes: it advances the timestamp and exits.
	h.agents.live = map[string]bool{}
	finished := h.now.Add(time.Minute)
	found := h.taskFor(t, "01-a")
	if err := os.Remove(found.Ready(h.cfg)); err != nil {
		t.Fatal(err)
	}
	h.markReady(t, "01-a", finished)

	h.now = h.now.Add(5 * time.Minute)
	result := h.run(t, false)

	if got := h.agents.refines(); got != 1 {
		t.Errorf("refine agents = %d, want still 1 — the feedback is older than the new timestamp", got)
	}
	if len(result.Problems) != 0 {
		t.Errorf("a finished round reported problems: %v", result.Problems)
	}
	if !containsSubstring(result.Waiting, "waiting on a reviewer") {
		t.Errorf("waiting = %v, want it to say the task is with a reviewer", result.Waiting)
	}
}

// Feedback newer than a dead round is genuinely new work, so the round is
// replaced rather than reported as crashed.
func TestRefineRestartsWhenNewFeedbackArrivesAfterADeadRound(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	published := h.now
	h.markReady(t, "01-a", published)
	h.run(t, false)
	h.comment(t, "01-a", published.Add(time.Minute))

	h.now = h.now.Add(2 * time.Minute)
	h.run(t, false) // round starts
	roundStarted := h.now

	// The agent dies, and then a reviewer comments again.
	h.agents.live = map[string]bool{}
	h.comment(t, "01-a", roundStarted.Add(time.Minute))
	h.now = h.now.Add(5 * time.Minute)
	result := h.run(t, false)

	if got := h.agents.refines(); got != 2 {
		t.Errorf("refine agents = %d, want 2: new feedback after a dead round is new work", got)
	}
	if len(result.Refines) != 1 {
		t.Errorf("refines = %v, want the task to have been refined", result.Refines)
	}
}

func TestInProgressToReviewNeedsTheMarker(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	// No marker: it stays where it is, however many passes run.
	h.run(t, false)
	if got := h.stateOf(t, "01-a"); got != InProgress {
		t.Fatalf("state = %s, want in-progress without a marker", got)
	}

	h.markReady(t, "01-a", h.now)
	result := h.run(t, false)
	if got := h.stateOf(t, "01-a"); got != Review {
		t.Errorf("state = %s, want review", got)
	}
	if len(result.Transitions) != 1 || result.Transitions[0].To != Review {
		t.Errorf("transitions = %v", result.Transitions)
	}
}

func TestReviewToApprovedOnApproval(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)

	h.approve(t, "01-a", 42)
	result := h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != Approved {
		t.Fatalf("state = %s, want approved", got)
	}
	if !containsSubstring(transitionNotes(result), "#42") {
		t.Errorf("the transition does not name the pull request: %v", result.Transitions)
	}
	// No refine agent: approval is not feedback.
	if got := h.agents.refines(); got != 0 {
		t.Errorf("an approval started %d refine agents", got)
	}
}

// A task spanning two repositories is one unit of work: two thirds approved is
// not approval.
func TestReviewNeedsEveryRepositoryApproved(t *testing.T) {
	h := newHarness(t, "nm", "site")
	h.add(t, Task{
		ID: "01-a", Repositories: []string{"nm", "site"},
		Description: "Both.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)

	// Approve one, leave the other merely open.
	found := h.taskFor(t, "01-a")
	h.review.byBranch[found.Repos[0].Branch] = &forge.Status{
		Number: 1, State: forge.StateOpen, Decision: forge.DecisionApproved, Mergeable: forge.MergeableYes,
	}
	h.review.byBranch[found.Repos[1].Branch] = &forge.Status{
		Number: 2, State: forge.StateOpen, Decision: "REVIEW_REQUIRED", Mergeable: forge.MergeableYes,
	}

	h.run(t, false)
	if got := h.stateOf(t, "01-a"); got != Review {
		t.Errorf("state = %s, want review while one repository is unapproved", got)
	}
}

// Without --merge nothing is ever merged, which is what makes a one-minute poller
// safe to leave running.
func TestApprovedIsNotMergedWithoutTheFlag(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)
	h.approve(t, "01-a", 7)
	h.run(t, false) // -> approved

	result := h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != Approved {
		t.Errorf("state = %s, want approved without --merge", got)
	}
	if len(h.review.merged) != 0 {
		t.Errorf("merged %v without --merge", h.review.merged)
	}
	if !containsSubstring(result.Waiting, "ready to merge") {
		t.Errorf("waiting = %v, want it to say the task is ready to merge", result.Waiting)
	}
}

func TestApprovedToCompletedWithMerge(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)
	h.approve(t, "01-a", 7)
	h.run(t, false)

	result := h.run(t, true)

	if got := h.stateOf(t, "01-a"); got != Completed {
		t.Fatalf("state = %s, want completed", got)
	}
	if len(h.review.merged) != 1 || h.review.merged[0] != 7 {
		t.Errorf("merged = %v, want [7]", h.review.merged)
	}
	if !containsSubstring(transitionNotes(result), "merged") {
		t.Errorf("the transition does not mention the merge: %v", result.Transitions)
	}
}

// A partial merge leaves the task approved and says what landed, so a re-run
// finishes the rest rather than starting over.
func TestPartialMergeLeavesTheTaskApproved(t *testing.T) {
	h := newHarness(t, "nm", "site")
	h.add(t, Task{
		ID: "01-a", Repositories: []string{"nm", "site"},
		Description: "Both.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)
	h.approve(t, "01-a", 5)
	h.run(t, false)

	h.review.mergeErr = errors.New("blocked by a required check")
	result := h.run(t, true)

	if got := h.stateOf(t, "01-a"); got != Approved {
		t.Errorf("state = %s, want approved after a failed merge", got)
	}
	if len(result.Problems) == 0 {
		t.Error("a failed merge was not reported")
	}
}

// A run with nothing to do prints nothing: sixty "no change" lines an hour is
// noise its reader has to filter.
func TestAQuietPassSaysNothing(t *testing.T) {
	h := newHarness(t)
	writeTask(t, h.w, Completed, def("01-a"))

	result := h.run(t, false)
	if !result.Quiet() {
		t.Errorf("a pass with nothing to do was not quiet: %+v", result)
	}
}

// Running the same pass twice must not duplicate anything. This is the property
// that makes a crashed pass repairable by running again.
func TestRunningTwiceChangesNothingTheSecondTime(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))

	first := h.run(t, false)
	second := h.run(t, false)

	if len(first.Transitions) != 1 {
		t.Fatalf("first pass transitions = %v, want one", first.Transitions)
	}
	if len(second.Transitions) != 0 {
		t.Errorf("second pass repeated transitions: %v", second.Transitions)
	}
	if got := h.agents.starts(); got != 1 {
		t.Errorf("launched %d agents over two passes, want 1", got)
	}
}

func TestLockStopsASecondRun(t *testing.T) {
	h := newHarness(t)
	h.add(t, def("01-a"))

	release, err := h.w.TakeLock(func() time.Time { return h.now })
	if err != nil {
		t.Fatalf("TakeLock: %v", err)
	}
	defer release()

	_, err = Execute(h.w, ExecuteOptions{
		Config: h.cfg, Review: h.review, Agents: h.agents,
		Now: func() time.Time { return h.now },
	})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Execute while locked returned %v, want ErrLocked", err)
	}
	// Nothing happened: the task is untouched.
	if got := h.stateOf(t, "01-a"); got != Planned {
		t.Errorf("state = %s, want planned — a locked run must not act", got)
	}
}

// A lock old enough to be certainly abandoned is broken, or a crashed
// orchestrator would stop the workplan until someone noticed.
func TestAStaleLockIsBroken(t *testing.T) {
	h := newHarness(t)
	h.add(t, def("01-a"))

	release, err := h.w.TakeLock(func() time.Time { return h.now })
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	h.now = h.now.Add(StaleLockAfter + time.Minute)
	result := h.run(t, false)
	if len(result.Transitions) == 0 {
		t.Error("a stale lock was not broken, so the workplan stopped moving")
	}
}

func TestEscalationsAreCollectedOnceEach(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	found := h.taskFor(t, "01-a")
	stamp := "2026-05-01-08-34-02.md"
	body := "# Stuck\n\nI need a decision about the contract.\n"
	if err := os.WriteFile(filepath.Join(found.Escalations(h.cfg), stamp), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	first := h.run(t, false)
	if len(first.Escalations) != 1 {
		t.Fatalf("escalations = %v, want one", first.Escalations)
	}
	if first.Escalations[0].Body != body {
		t.Errorf("the escalation body was not carried through: %q", first.Escalations[0].Body)
	}
	if _, err := os.Stat(filepath.Join(h.w.TaskEscalations("01-a"), stamp)); err != nil {
		t.Errorf("the escalation was not copied into the workplan: %v", err)
	}

	// Copying rewrites mtime, so an mtime-based check would report it again here.
	second := h.run(t, false)
	if len(second.Escalations) != 0 {
		t.Errorf("the same escalation was reported twice: %v", second.Escalations)
	}
}

func TestResolutionsAreDeliveredToTheTask(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	found := h.taskFor(t, "01-a")
	stamp := "2026-05-01-08-34-02"
	if err := os.WriteFile(filepath.Join(found.Escalations(h.cfg), stamp+".md"), []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.run(t, false) // collect it

	// A human answers in the workplan.
	answer := filepath.Join(h.w.TaskEscalations("01-a"), stamp+ResolutionSuffix)
	if err := os.WriteFile(answer, []byte("Use the second option."), 0o644); err != nil {
		t.Fatal(err)
	}

	h.run(t, false)

	delivered := filepath.Join(found.Escalations(h.cfg), stamp+ResolutionSuffix)
	got, err := os.ReadFile(delivered)
	if err != nil {
		t.Fatalf("the resolution was not delivered: %v", err)
	}
	if string(got) != "Use the second option." {
		t.Errorf("delivered %q", got)
	}
	// And the escalation is no longer open.
	open, err := h.w.OpenEscalations("01-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("open escalations = %v, want none once answered", open)
	}
}

// The markers are bookkeeping between the agent and the orchestrator, and must
// never be shown to a human as an escalation.
func TestMarkersAreNotCollectedAsEscalations(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)

	result := h.run(t, false)
	for _, e := range result.Escalations {
		if e.Name == task.ReadyFile || e.Name == task.RefiningFile {
			t.Errorf("marker %s was reported as an escalation", e.Name)
		}
	}
}

// A successor whose predecessor is approved but not merged must branch from that
// predecessor's branch: main does not contain the work it builds on.
func TestSuccessorStacksOnAnApprovedPredecessor(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.add(t, Task{
		ID: "02-b", Predecessors: []string{"01-a"}, Repositories: []string{"nm"},
		Description: "After a.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false) // starts 01-a

	// 01-a does some work and is approved.
	first := h.taskFor(t, "01-a")
	marker := filepath.Join(first.Repos[0].Dir, "from-01a.txt")
	if err := os.WriteFile(marker, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, first.Repos[0].Dir, "add", "-A")
	gitIn(t, first.Repos[0].Dir, "commit", "-m", "01-a work")
	head := gitIn(t, first.Repos[0].Dir, "rev-parse", "HEAD")

	h.markReady(t, "01-a", h.now)
	h.run(t, false)
	h.approve(t, "01-a", 1)
	h.run(t, false) // 01-a -> approved, and 02-b starts

	if got := h.stateOf(t, "02-b"); got != InProgress {
		t.Fatalf("02-b state = %s, want in-progress once 01-a is approved", got)
	}
	second := h.taskFor(t, "02-b")
	if got := second.Repos[0].BaseCommit; got != head {
		t.Errorf("02-b branched from %q, want the predecessor's head %q", got, head)
	}
	// The real assertion: the successor's worktree contains the predecessor's work.
	if _, err := os.Stat(filepath.Join(second.Repos[0].Dir, "from-01a.txt")); err != nil {
		t.Errorf("02-b does not contain 01-a's work: %v", err)
	}
}

// A completed predecessor is already in the base branch, so the usual resolution
// is right and no stacking is needed.
func TestSuccessorOfACompletedPredecessorUsesTheDefaultBase(t *testing.T) {
	h := newHarness(t, "nm")
	writeTask(t, h.w, Completed, def("01-a"))
	h.add(t, Task{
		ID: "02-b", Predecessors: []string{"01-a"}, Repositories: []string{"nm"},
		Description: "After a.", AcceptanceCriteria: []string{"Done."},
	})

	h.run(t, false)

	if got := h.stateOf(t, "02-b"); got != InProgress {
		t.Fatalf("02-b state = %s, want in-progress", got)
	}
	found := h.taskFor(t, "02-b")
	if got := found.Repos[0].BaseBranch; got != "main" {
		t.Errorf("BaseBranch = %q, want main", got)
	}
}

// A task with no repositories has no pull request, so it reaches approved by a
// human answering its escalation and completes with nothing to merge.
func TestZeroRepositoryTaskFlowsThroughEscalations(t *testing.T) {
	h := newHarness(t)
	h.add(t, Task{
		ID: "01-q", Description: "Clarify the contract.",
		AcceptanceCriteria: []string{"An answer is recorded."},
	})
	h.run(t, false)

	found := h.taskFor(t, "01-q")
	stamp := "2026-05-01-08-34-02"
	if err := os.WriteFile(filepath.Join(found.Escalations(h.cfg), stamp+".md"), []byte("which option?"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.markReady(t, "01-q", h.now)
	h.run(t, false) // collects, and moves to review

	if got := h.stateOf(t, "01-q"); got != Review {
		t.Fatalf("state = %s, want review", got)
	}
	// It must not advance while the question is open.
	result := h.run(t, false)
	if got := h.stateOf(t, "01-q"); got != Review {
		t.Fatalf("state = %s, want review while an escalation is open", got)
	}
	if !containsSubstring(result.Waiting, "waiting on") {
		t.Errorf("waiting = %v, want it to say what is blocked", result.Waiting)
	}

	// Answered: it reaches completed in one pass, because step 4 runs after step
	// 3 and a task with nothing to merge needs no --merge. Both transitions are
	// reported, so the single pass is legible rather than a jump.
	answer := filepath.Join(h.w.TaskEscalations("01-q"), stamp+ResolutionSuffix)
	if err := os.WriteFile(answer, []byte("the second one"), 0o644); err != nil {
		t.Fatal(err)
	}
	final := h.run(t, false)

	if got := h.stateOf(t, "01-q"); got != Completed {
		t.Errorf("state = %s, want completed once the question is answered", got)
	}
	if len(final.Transitions) != 2 {
		t.Fatalf("transitions = %v, want review->approved and approved->completed", final.Transitions)
	}
	if final.Transitions[0].To != Approved || final.Transitions[1].To != Completed {
		t.Errorf("transitions = %v, want it to pass through approved rather than skipping it", final.Transitions)
	}
	// Nothing was merged, because there was nothing to merge.
	if len(h.review.merged) != 0 {
		t.Errorf("merged %v for a task with no repositories", h.review.merged)
	}
}

// A pass observes a merge and starts the successor it unblocked, rather than
// needing a second pass. This is why step 5 runs last.
func TestOnePassMergesAndStartsTheSuccessor(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.add(t, Task{
		ID: "02-b", Predecessors: []string{"01-a", "03-c"}, Repositories: []string{"nm"},
		Description: "Needs both.", AcceptanceCriteria: []string{"Done."},
	})
	writeTask(t, h.w, Completed, def("03-c"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)
	h.approve(t, "01-a", 9)
	h.run(t, false) // -> approved

	// With two predecessors, 02-b needs both completed. This pass merges 01-a and
	// must then start 02-b.
	result := h.run(t, true)

	if got := h.stateOf(t, "01-a"); got != Completed {
		t.Fatalf("01-a state = %s, want completed", got)
	}
	if got := h.stateOf(t, "02-b"); got != InProgress {
		t.Errorf("02-b state = %s, want in-progress in the same pass as the merge", got)
	}
	if len(result.Transitions) != 2 {
		t.Errorf("transitions = %v, want both the merge and the start", result.Transitions)
	}
}

// An agent that fails to launch must not lose the task: the worktrees exist and
// the work can be picked up by hand.
func TestATaskIsKeptWhenItsAgentFailsToStart(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.agents.failWith = errors.New("claude is not on PATH")

	result := h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != InProgress {
		t.Errorf("state = %s, want in-progress even without an agent", got)
	}
	if len(result.Problems) == 0 {
		t.Error("the failure to start an agent was not reported")
	}
	h.taskFor(t, "01-a") // fatals if the directory is not there
}

func TestReadStamp(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"rfc3339":            "2026-05-01T08:34:02Z",
		"with a heading":     "# Ready\n\n2026-05-01T08:34:02Z\n",
		"filename form":      "2026-05-01-08-34-02",
		"space separated":    "2026-05-01 08:34:02",
		"prose then a stamp": "This is ready.\n\n2026-05-01T08:34:02Z\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".md")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			at, err := readStamp(path)
			if err != nil {
				t.Fatalf("readStamp: %v", err)
			}
			if at.IsZero() {
				t.Error("readStamp returned the zero time")
			}
		})
	}

	// A marker with no timestamp is an error, not a zero time: a zero time would
	// make every comment ever left look like new feedback.
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte("# Ready\n\nno date here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readStamp(path); err == nil {
		t.Error("readStamp accepted a marker with no timestamp")
	}
}

// helpers ------------------------------------------------------------------

// def2 is a valid definition with one repository.
func def2(id, repo string) Task {
	return Task{
		ID: id, Repositories: []string{repo},
		Description: "Do " + id + ".", AcceptanceCriteria: []string{"It is done."},
	}
}

func transitionNotes(r Result) []string {
	out := make([]string, 0, len(r.Transitions))
	for _, t := range r.Transitions {
		out = append(out, t.Note)
	}
	return out
}

func containsSubstring(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// taskEnv builds real git repositories with a remote, so tasks can be created and
// branched for real rather than against a fake.
//
// It mirrors the helper in package task, which is not exported. Duplicating it is
// better than exporting test scaffolding from a production package.
func taskEnv(t *testing.T, repos ...string) config.Config {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "nm test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "nm test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	// The pre-commit hook exports these; left set, they would point every git
	// invocation below at this repository instead of the temporary one.
	for _, key := range []string{
		"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_PREFIX", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}

	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		bare := filepath.Join(root, repo+".git")
		gitIn(t, root, "init", "--bare", "-b", "main", bare)
		dir := filepath.Join(projects, repo)
		gitIn(t, projects, "init", "-b", "main", dir)
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(repo+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-m", "init")
		gitIn(t, dir, "remote", "add", "origin", bare)
		gitIn(t, dir, "push", "--quiet", "-u", "origin", "main")
	}

	cfg := config.Defaults()
	cfg.ProjectsRoot = projects
	cfg.WorktreesRoot = filepath.Join(root, "worktrees")
	cfg.TasksRoot = filepath.Join(root, "tasks")
	cfg.WorkplansRoot = filepath.Join(root, "workplans")
	return cfg
}

// A task whose agent has stopped without escalating or finishing is otherwise
// invisible: it looks exactly like a task being worked on. nm cannot fix it — a
// blocked agent is waiting on stdin, so nothing written to its escalations
// directory reaches it — so saying so is the entire remedy.
func TestStuckAgentWithNothingOpenIsReported(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	// The agent stops with no escalation and no ready marker.
	h.agents.live = map[string]bool{}
	h.agents.stalled = map[string]bool{"agent1": true}

	result := h.run(t, false)

	if len(result.Problems) == 0 {
		t.Fatal("a stuck agent was not reported, so the workplan stops silently")
	}
	joined := strings.Join(result.Problems, "\n")
	if !strings.Contains(joined, "01-a") {
		t.Errorf("the problem does not name the task:\n%s", joined)
	}
	// It has to say what to do, because nm cannot do anything about it.
	if !strings.Contains(joined, "claude attach") {
		t.Errorf("the problem does not say how to look at the agent:\n%s", joined)
	}
	if got := h.stateOf(t, "01-a"); got != InProgress {
		t.Errorf("state = %s; a stuck task must stay where it is", got)
	}
}

// An agent that stopped *after* escalating is a different, already-reported state,
// and must not also be reported as stuck.
func TestAnAgentBlockedOnAnOpenEscalationIsNotReportedAsStuck(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	found := h.taskFor(t, "01-a")
	if err := os.WriteFile(
		filepath.Join(found.Escalations(h.cfg), "2026-05-01-08-34-02.md"),
		[]byte("which option?"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.agents.live = map[string]bool{}
	h.agents.stalled = map[string]bool{"agent1": true}

	result := h.run(t, false)

	for _, p := range result.Problems {
		if strings.Contains(p, "without escalating") {
			t.Errorf("an agent waiting on its own escalation was reported as stuck: %s", p)
		}
	}
}

// A working agent is not stuck, and must not be reported every minute.
func TestAWorkingAgentIsNotReported(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)

	result := h.run(t, false) // agent1 is still live, not stalled
	if len(result.Problems) != 0 {
		t.Errorf("a working agent produced problems: %v", result.Problems)
	}
}

// mergedStatus is a pull request that is already in its base branch, with nobody
// having formally approved it.
func (h *harness) markMerged(t *testing.T, id string, number int) {
	t.Helper()
	for _, repo := range h.taskFor(t, id).Repos {
		h.review.byBranch[repo.Branch] = &forge.Status{
			Number: number, URL: fmt.Sprintf("https://example.invalid/pull/%d", number),
			State: forge.StateMerged, Decision: "",
		}
	}
}

// A task whose pull request was merged without a review must still complete.
//
// This is the bug the first real dogfood run found. A merge is how a task's life
// normally ends, and merging without a separate approval is the ordinary way a
// solo maintainer works — so the path that broke was the common one. The task sat
// in review forever reporting "0 of 1 open", because the status read filtered to
// open pull requests and a merged one came back as nothing at all.
func TestAMergedPullRequestCompletesTheTaskWithoutAnApproval(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false) // -> review

	h.markMerged(t, "01-a", 8)

	// No --merge: nm is observing a merge, not performing one, and requiring the
	// flag to notice would leave the task stuck.
	result := h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != Completed {
		t.Fatalf("state = %s, want completed for a merged pull request", got)
	}
	if len(h.review.merged) != 0 {
		t.Errorf("nm merged %v when the pull request was already merged", h.review.merged)
	}
	// The output must not claim an approval nobody gave.
	notes := strings.Join(transitionNotes(result), " | ")
	if !strings.Contains(notes, "merged") {
		t.Errorf("notes = %q, want them to say the work was merged", notes)
	}
	if strings.Contains(notes, "approved:") {
		t.Errorf("notes = %q claim an approval that never happened", notes)
	}
}

// A merged pull request must not be read as feedback and start a refine round.
func TestAMergedPullRequestDoesNotStartARefineRound(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)

	h.markMerged(t, "01-a", 8)
	h.now = h.now.Add(time.Minute)
	h.run(t, false)

	if got := h.agents.refines(); got != 0 {
		t.Errorf("a merged pull request started %d refine agents", got)
	}
}

// Merging one of two repositories is not merging the task.
func TestAPartiallyMergedTaskDoesNotComplete(t *testing.T) {
	h := newHarness(t, "nm", "site")
	h.add(t, Task{
		ID: "01-a", Repositories: []string{"nm", "site"},
		Description: "Both.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false)
	h.markReady(t, "01-a", h.now)
	h.run(t, false)

	found := h.taskFor(t, "01-a")
	h.review.byBranch[found.Repos[0].Branch] = &forge.Status{
		Number: 1, State: forge.StateMerged,
	}
	h.review.byBranch[found.Repos[1].Branch] = &forge.Status{
		Number: 2, State: forge.StateOpen, Decision: "REVIEW_REQUIRED",
	}

	h.run(t, false)
	if got := h.stateOf(t, "01-a"); got == Completed {
		t.Error("a task with one of two repositories merged was completed")
	}
}

// A successor starts once its predecessor reaches review, and stacks on that
// predecessor's branch rather than on main.
//
// Review rather than approved is a deliberate relaxation: review latency is the
// largest delay in the pipeline, and the work a successor builds on exists the
// moment the predecessor's branch does. The stacking is what makes it safe — this
// asserts the worktree really contains the predecessor's commit, because branching
// from main would silently lose it and still look like a successful start.
func TestSuccessorStartsAtReviewAndStacksOnIt(t *testing.T) {
	h := newHarness(t, "nm")
	h.add(t, def2("01-a", "nm"))
	h.add(t, Task{
		ID: "02-b", Predecessors: []string{"01-a"}, Repositories: []string{"nm"},
		Description: "After a.", AcceptanceCriteria: []string{"Done."},
	})
	h.run(t, false) // starts 01-a

	// 01-a commits work and says it is ready. Nobody has reviewed it.
	first := h.taskFor(t, "01-a")
	if err := os.WriteFile(filepath.Join(first.Repos[0].Dir, "from-01a.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, first.Repos[0].Dir, "add", "-A")
	gitIn(t, first.Repos[0].Dir, "commit", "-m", "01-a work")
	head := gitIn(t, first.Repos[0].Dir, "rev-parse", "HEAD")
	h.markReady(t, "01-a", h.now)

	// One pass: 01-a reaches review, and 02-b starts in the same pass.
	h.run(t, false)

	if got := h.stateOf(t, "01-a"); got != Review {
		t.Fatalf("01-a state = %s, want review", got)
	}
	if got := h.stateOf(t, "02-b"); got != InProgress {
		t.Fatalf("02-b state = %s, want in-progress once 01-a reached review", got)
	}

	second := h.taskFor(t, "02-b")
	if got := second.Repos[0].BaseCommit; got != head {
		t.Errorf("02-b branched from %q, want the predecessor's head %q", got, head)
	}
	if got := second.Repos[0].BaseBranch; got != first.Repos[0].Branch {
		t.Errorf("BaseBranch = %q, want the predecessor's branch %q", got, first.Repos[0].Branch)
	}
	// The assertion that matters: the work is actually there.
	if _, err := os.Stat(filepath.Join(second.Repos[0].Dir, "from-01a.txt")); err != nil {
		t.Errorf("02-b does not contain 01-a's work, so it branched from the wrong base: %v", err)
	}
}

// eligible and stackBase are one decision in two places. Every state eligible
// accepts for a single predecessor must be one stackBase knows how to branch from,
// or a successor starts from main and silently loses the work it was scoped to
// build on — which looks like a successful start, not a failure.
func TestEligibleAndStackBaseAgree(t *testing.T) {
	successor := Task{
		ID: "02-b", Predecessors: []string{"01-a"}, Repositories: []string{"nm"},
		Description: "d", AcceptanceCriteria: []string{"c"},
	}

	for _, state := range States() {
		p := &pass{state: map[string]State{"01-a": state}}
		starts, _ := p.eligible(successor)
		if !starts {
			continue
		}
		// It starts from this state, so stacking must have an answer: either a
		// base to branch from, or Completed, where main already has the work.
		if state == Completed {
			continue
		}
		if _, ok := stackableStates()[state]; !ok {
			t.Errorf("eligible starts a successor when its predecessor is %s, but stackBase "+
				"does not stack on that state — the successor would branch from main and lose "+
				"the work it builds on", state)
		}
	}
}
