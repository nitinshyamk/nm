package workplan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
)

// Transition is one task moving between states.
type Transition struct {
	TaskID string
	From   State
	To     State
	Note   string // why, or what it produced: an agent id, a merge, a pull request
}

// String renders a transition the way execute prints it.
func (t Transition) String() string {
	line := fmt.Sprintf("%s %s -> %s", t.TaskID, t.From, t.To)
	if t.Note != "" {
		line += "  (" + t.Note + ")"
	}
	return line
}

// Result is what one pass of the orchestrator did.
type Result struct {
	Transitions []Transition
	Escalations []Escalation
	Refines     []string // task ids whose own agent is actioning review feedback
	Waiting     []string // human-readable notes about what is blocked, and on what
	Problems    []string // what went wrong without stopping the pass
}

// Quiet reports whether the pass changed nothing worth telling anyone about.
//
// A poller runs this every minute; a pass that prints "no change" sixty times an
// hour is noise its reader then has to filter out.
func (r Result) Quiet() bool {
	return len(r.Transitions) == 0 && len(r.Escalations) == 0 &&
		len(r.Refines) == 0 && len(r.Problems) == 0
}

// Agents is the slice of the agent CLI the orchestrator needs: start one, and say
// whether one is still alive.
//
// An interface so the state machine is testable without launching anything, which
// matters most for liveness: the review step answers "is this task's agent still
// there" on every pass, and a test that had to start a real agent would not be run.
type Agents interface {
	// Launch is called once per task, and once only. A task keeps the agent that
	// started it for its whole life, including its review rounds.
	Launch(dir, name, prompt string, addDirs []string) (id string, err error)
	// Alive distinguishes a task whose agent is waiting on a reviewer from one whose
	// agent has died with feedback unanswered — the second needs a human, and looks
	// identical from outside without this.
	Alive(id, dir string) bool
	// Stalled reports that an agent exists but is neither working nor finished:
	// blocked on something, or stopped without saying anything. It is separate
	// from Alive because the two answer different questions — this one is about an
	// agent that is present and going nowhere.
	Stalled(id, dir string) bool
}

// ExecuteOptions controls one pass.
type ExecuteOptions struct {
	Config config.Config
	Review Reviewer
	Agents Agents
	Now    func() time.Time

	// Merge allows the last transition. Without it an approved task is reported
	// as ready to merge and left alone, so a poller running unattended never puts
	// anything into production on its own.
	Merge bool

	// MergeMethod is passed to the forge, defaulting to a squash merge.
	MergeMethod string
}

// Reviewer is what the orchestrator needs to read a pull request. It mirrors
// task.Reviewer, restated here so this package does not force its callers to
// construct one through the task package.
type Reviewer interface {
	StatusForBranch(dir, branch string) (*forge.Status, error)
	Merge(dir string, number int, method string) error
}

// Execute runs one pass of the orchestrator.
//
// The five steps run in a fixed order, and each is independently idempotent, so a
// crashed pass is repaired by running again rather than by cleaning up after it.
// Starting work comes last so a single pass can observe a merge and start the
// successor it unblocked, instead of needing two.
func Execute(w Workplan, opts ExecuteOptions) (Result, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	release, err := w.TakeLock(now)
	if err != nil {
		// A held lock is not a failure: the other run is doing this work. The
		// caller distinguishes it with errors.Is when it cares.
		return Result{}, err
	}
	defer release()

	placed, readErrs := w.Tasks()
	var result Result
	for _, err := range readErrs {
		result.Problems = append(result.Problems, err.Error())
	}

	state := newPass(w, opts, now, placed)

	state.collectEscalations(&result)  // 1
	state.reviewReady(&result)         // 2
	state.reviewToApproved(&result)    // 3
	state.approvedToCompleted(&result) // 4
	state.plannedToInProgress(&result) // 5
	return result, nil
}

// pass is one invocation's working state: the tasks as they were read, and the
// state each has reached as this pass moves them.
type pass struct {
	w     Workplan
	opts  ExecuteOptions
	now   func() time.Time
	tasks []Placed

	// state is the live view, updated as transitions happen, so step 5 sees what
	// step 4 just completed.
	state map[string]State
}

func newPass(w Workplan, opts ExecuteOptions, now func() time.Time, placed []Placed) *pass {
	state := make(map[string]State, len(placed))
	for _, p := range placed {
		state[p.Task.ID] = p.State
	}
	return &pass{w: w, opts: opts, now: now, tasks: placed, state: state}
}

// move performs a transition: the file moves, and the live view follows.
func (p *pass) move(result *Result, id string, from, to State, note string) bool {
	src, dst := p.w.TaskFile(from, id), p.w.TaskFile(to, id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
		return false
	}
	// Rename is the transition. It is atomic within a filesystem, so a task is
	// never in two states or in none, even if the process dies mid-pass.
	if err := os.Rename(src, dst); err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("moving %s to %s: %v", id, to, err))
		return false
	}
	p.state[id] = to
	result.Transitions = append(result.Transitions, Transition{TaskID: id, From: from, To: to, Note: note})
	return true
}

// taskDir finds the task directory for a workplan task, by the id recorded in it.
//
// The id is the link, not the hash: a hash is derived from the name and the
// repositories, so it cannot be recomputed without knowing what the task was
// created with, and reading the record is both cheaper and correct.
func (p *pass) taskDir(id string) (task.Task, bool) {
	tasks, err := task.List(p.opts.Config)
	if err != nil {
		return task.Task{}, false
	}
	for _, t := range tasks {
		if t.Workplan == p.w.Name && t.TaskID == id {
			return t, true
		}
	}
	return task.Task{}, false
}

// collectEscalations is step 1: copy anything new out of each task's escalations
// directory, and deliver any answers back.
func (p *pass) collectEscalations(result *Result) {
	for _, placed := range p.tasks {
		id := placed.Task.ID
		t, ok := p.taskDir(id)
		if !ok {
			continue // not started yet, so nothing to collect
		}
		dir := t.Escalations(p.opts.Config)

		fresh, err := p.w.CollectEscalations(id, dir)
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
		}
		result.Escalations = append(result.Escalations, fresh...)

		// Delivering answers here rather than in a separate command means a
		// resolution written between passes reaches the agent on the next one,
		// with no second thing to remember to run.
		if _, err := p.w.DeliverResolutions(id, dir); err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
		}
	}
}

// reviewReady is step 2: in-progress -> review, when the agent has said the work
// is published and green.
//
// It also reports the one state that is otherwise invisible: an agent that is
// stuck with nothing open. A task in progress whose agent is blocked, with no
// escalation and no ready marker, looks exactly like a task being worked on — and
// nm cannot fix it, because a blocked agent is waiting on stdin rather than
// reading files, so the resolution mechanism cannot reach it. Saying so is the
// whole remedy available.
func (p *pass) reviewReady(result *Result) {
	for _, placed := range p.tasks {
		if p.state[placed.Task.ID] != InProgress {
			continue
		}
		id := placed.Task.ID
		t, ok := p.taskDir(id)
		if !ok {
			continue
		}
		if _, err := os.Stat(t.Ready(p.opts.Config)); err == nil {
			p.move(result, id, InProgress, Review, "ready to review")
			continue
		}
		p.reportIfStuck(result, t, id)
	}
}

// reportIfStuck names a task whose agent has stopped without producing anything.
func (p *pass) reportIfStuck(result *Result, t task.Task, id string) {
	agentID := ""
	if t.Agent != nil {
		agentID = t.Agent.ID
	}
	if !p.opts.Agents.Stalled(agentID, t.Dir) {
		return
	}
	// An open escalation is the agent asking for something, which is a different
	// and already-reported state.
	open, err := p.w.OpenEscalations(id)
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
		return
	}
	if len(open) > 0 {
		return
	}
	result.Problems = append(result.Problems, fmt.Sprintf(
		"%s: its agent has stopped without escalating or finishing — nothing nm writes "+
			"can reach a blocked agent, so look at it with: claude attach %s",
		id, agentOrDir(agentID, t.Dir)))
}

func agentOrDir(id, dir string) string {
	if id != "" {
		return id
	}
	return "<id unknown; the task is at " + dir + ">"
}

// reviewToApproved is step 3: review -> approved when a human has approved.
//
// Feedback arriving after the work was published needs nothing from this step. The
// task's own agent is blocked on `nm workplan await-feedback` and wakes to action it
// in the session that built the work, so all the orchestrator does is report.
//
// That used to be a launch, and it was the one transition nothing latched: the task
// stays in review throughout, so the trigger held true and a one-minute poller
// started an agent a minute. A trigger that starts no process cannot re-fire, so the
// `refining.md` latch went with it.
func (p *pass) reviewToApproved(result *Result) {
	for _, placed := range p.tasks {
		id := placed.Task.ID
		if p.state[id] != Review {
			continue
		}
		t, ok := p.taskDir(id)
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s is in review but has no task directory", id))
			continue
		}

		// A task that changes no code has no pull request to approve, so it waits
		// for a human to answer an escalation instead.
		if len(t.Repos) == 0 {
			p.zeroRepoReview(result, t, id)
			continue
		}

		statuses, problems := p.readPRs(t)
		result.Problems = append(result.Problems, problems...)
		if len(statuses) != len(t.Repos) {
			// Without every pull request there is no honest answer to "has this
			// been approved", so the task waits rather than advancing on a
			// partial read.
			result.Waiting = append(result.Waiting,
				fmt.Sprintf("%s: waiting for a pull request in every repository (%d of %d open)",
					id, len(statuses), len(t.Repos)))
			continue
		}

		if allApproved(statuses) {
			p.move(result, id, Review, Approved, approvalNote(statuses))
			continue
		}

		// Not approved. The task's own agent is watching for feedback and actions it
		// in the session that published the work, so there is nothing to start here —
		// the orchestrator reports rather than intervenes.
		published, err := ReadStamp(t.Ready(p.opts.Config))
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if at, ok := latestFeedback(statuses, published); ok {
			p.reportRefining(result, t, id, at)
			continue
		}
		result.Waiting = append(result.Waiting, fmt.Sprintf("%s: waiting on a reviewer", id))
	}
}

// zeroRepoReview handles a task with no repositories, which cannot be approved by
// a pull request.
//
// It reaches approved when a human answers its escalation, which is the only
// signal such a task has. The orchestrator does not invent one.
func (p *pass) zeroRepoReview(result *Result, t task.Task, id string) {
	open, err := p.w.OpenEscalations(id)
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", id, err))
		return
	}
	if len(open) > 0 {
		result.Waiting = append(result.Waiting,
			fmt.Sprintf("%s: no repositories, waiting on %s", id, plural(len(open), "escalation", "escalations")))
		return
	}
	// Every escalation answered and nothing to merge: the work is done.
	if _, err := os.Stat(t.Ready(p.opts.Config)); err != nil {
		result.Waiting = append(result.Waiting, fmt.Sprintf("%s: no repositories, not yet ready", id))
		return
	}
	p.move(result, id, Review, Approved, "no repositories; escalations answered")
}

// readPRs reads the latest pull request for each of a task's repositories,
// whatever state it is in.
//
// Merged ones included, deliberately. A merged pull request is how a task's life
// normally ends, and reading only open ones made a merged task look like work that
// was never published at all.
func (p *pass) readPRs(t task.Task) ([]*forge.Status, []string) {
	var statuses []*forge.Status
	var problems []string
	for _, repo := range t.Repos {
		status, err := p.opts.Review.StatusForBranch(repo.Dir, repo.Branch)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: reading %s: %v", t.TaskID, repo.Name, err))
			continue
		}
		if status == nil {
			continue
		}
		statuses = append(statuses, status)
	}
	return statuses, problems
}

// allApproved reports whether every repository's pull request is approved.
//
// All of them, because a task spanning three repositories is one unit of work and
// two thirds of it being approved is not approval.
func allApproved(statuses []*forge.Status) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !s.Approved() && !s.Merged() {
			return false
		}
	}
	return true
}

// approvalNote says why the task advanced, distinguishing a human's approval from
// a merge that happened without one — which is a stronger signal, not a weaker
// one, and is worth naming so the output does not claim an approval nobody gave.
func approvalNote(statuses []*forge.Status) string {
	numbers := make([]string, 0, len(statuses))
	merged := 0
	for _, s := range statuses {
		numbers = append(numbers, fmt.Sprintf("#%d", s.Number))
		if s.Merged() {
			merged++
		}
	}
	verb := "approved"
	if merged == len(statuses) {
		verb = "already merged"
	} else if merged > 0 {
		verb = "approved or merged"
	}
	return verb + ": " + strings.Join(numbers, ", ")
}

// allMerged reports whether every pull request is already in its base branch.
func allMerged(statuses []*forge.Status) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !s.Merged() {
			return false
		}
	}
	return true
}

func numbersOf(statuses []*forge.Status) string {
	numbers := make([]string, 0, len(statuses))
	for _, s := range statuses {
		numbers = append(numbers, fmt.Sprintf("#%d", s.Number))
	}
	return strings.Join(numbers, ", ")
}

// latestFeedback is the most recent review or comment after the given time,
// across every repository.
func latestFeedback(statuses []*forge.Status, since time.Time) (time.Time, bool) {
	var latest time.Time
	for _, s := range statuses {
		if at, ok := s.FeedbackAfter(since); ok && at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

// reportRefining says a task has feedback its own agent is dealing with, and
// escalates when that agent is gone.
//
// The orchestrator no longer starts anything here, and that is the point. One agent
// owns a task from its definition to an approved pull request: it publishes, then
// blocks on `nm workplan await-feedback` in the same session, so it actions a
// reviewer's comment already knowing what it built and why. Launching a second agent
// was what made two processes possible in one worktree — nothing latched the handoff
// between the first agent and its replacement, so a task whose agent had written
// ready-to-review.md but not yet exited got a second one committing beside it.
//
// The `refining.md` latch that guarded the old respawn is gone with it. There is
// nothing left to latch: a trigger that starts no process cannot re-fire.
//
// What is left is the case the latch could not fix anyway. An agent that died while
// its pull request has unanswered feedback needs a human, because nothing else is
// watching that task — and unlike a stalled agent, this one is invisible: the task
// sits in review looking exactly like one waiting on a slow reviewer.
func (p *pass) reportRefining(result *Result, t task.Task, id string, feedbackAt time.Time) {
	agentID := ""
	if t.Agent != nil {
		agentID = t.Agent.ID
	}

	if p.opts.Agents.Alive(agentID, t.Dir) {
		result.Refines = append(result.Refines, id)
		result.Waiting = append(result.Waiting, fmt.Sprintf(
			"%s: its agent is actioning feedback from %s (agent %s)",
			id, feedbackAt.Format(time.RFC3339), agentID))
		return
	}

	// No agent, and a reviewer is waiting. Reported for a human rather than fixed.
	//
	// Nothing here starts a replacement, and that is deliberate twice over: starting
	// one is what allowed two agents in a worktree, and whatever killed the first will
	// very likely kill the next, so an automatic restart hides a repeating failure
	// behind apparent activity. The message says "needs review" rather than naming a
	// remedy, because the remedy depends on why it died — which a human has to look at.
	result.Problems = append(result.Problems, fmt.Sprintf(
		"%s: a reviewer left feedback at %s but its agent is gone, so nothing is "+
			"actioning it — this needs manual review: look at %s, decide whether the work "+
			"so far is sound, and do not let anything restart the agent automatically",
		id, feedbackAt.Format(time.RFC3339), t.Dir))
}

// approvedToCompleted is step 4: merge, when asked to.
func (p *pass) approvedToCompleted(result *Result) {
	for _, placed := range p.tasks {
		id := placed.Task.ID
		if p.state[id] != Approved {
			continue
		}
		t, ok := p.taskDir(id)
		if !ok {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s is approved but has no task directory", id))
			continue
		}

		// Nothing to merge: a task that changes no code is complete once approved.
		if len(t.Repos) == 0 {
			p.move(result, id, Approved, Completed, "no repositories to merge")
			continue
		}

		statuses, problems := p.readPRs(t)
		result.Problems = append(result.Problems, problems...)

		// Already in production, however it got there. --merge gates nm *doing* a
		// merge; it cannot gate observing one somebody else did, and requiring the
		// flag to notice would leave a merged task stuck in approved forever.
		if len(statuses) == len(t.Repos) && allMerged(statuses) {
			p.move(result, id, Approved, Completed, "merged outside nm: "+numbersOf(statuses))
			continue
		}

		if !p.opts.Merge {
			for _, s := range statuses {
				result.Waiting = append(result.Waiting,
					fmt.Sprintf("%s: approved, ready to merge: %s", id, s.URL))
			}
			continue
		}
		p.mergeTask(result, t, id, statuses)
	}
}

// mergeTask merges every repository's pull request, and moves the task only when
// all of them are in.
//
// A partial merge leaves the task in approved and says which repositories landed,
// so a re-run finishes the rest rather than starting over.
func (p *pass) mergeTask(result *Result, t task.Task, id string, statuses []*forge.Status) {
	if len(statuses) != len(t.Repos) {
		result.Problems = append(result.Problems, fmt.Sprintf(
			"%s: only %d of %d repositories have a pull request, so it was not merged",
			id, len(statuses), len(t.Repos)))
		return
	}

	merged := make([]string, 0, len(statuses))
	for i, s := range statuses {
		if s.Merged() {
			merged = append(merged, fmt.Sprintf("#%d", s.Number))
			continue
		}
		repo := t.Repos[i]
		if err := p.opts.Review.Merge(repo.Dir, s.Number, p.opts.MergeMethod); err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s: merging %s #%d: %v", id, repo.Name, s.Number, err))
			continue
		}
		merged = append(merged, fmt.Sprintf("#%d", s.Number))
	}

	if len(merged) != len(statuses) {
		result.Waiting = append(result.Waiting, fmt.Sprintf(
			"%s: merged %s of %d — re-run to finish the rest",
			id, strings.Join(merged, ", "), len(statuses)))
		return
	}
	p.move(result, id, Approved, Completed, "merged "+strings.Join(merged, ", "))
}

// plannedToInProgress is step 5: start the work that is unblocked.
func (p *pass) plannedToInProgress(result *Result) {
	for _, placed := range p.tasks {
		id := placed.Task.ID
		if p.state[id] != Planned {
			continue
		}
		ready, why := p.eligible(placed.Task)
		if !ready {
			if why != "" {
				result.Waiting = append(result.Waiting, fmt.Sprintf("%s: %s", id, why))
			}
			continue
		}
		p.start(result, placed.Task)
	}
}

// eligible answers whether a planned task may start, and says what it is waiting
// for when it may not.
//
// The rule: no predecessors starts immediately; exactly one starts once that
// predecessor has reached review; two or more wait for all of them to be
// completed.
//
// The asymmetry is deliberate. A linear chain should not stall — a successor
// stacks on its predecessor's branch, so the work it builds on is there the moment
// that branch exists, and review latency is the largest delay in the pipeline. A
// fan-in is different: it has several branches to integrate at once, and one base
// cannot stack on three predecessors, so it waits for merged code.
//
// Review rather than approved is a deliberate relaxation of the gate, and it costs
// something worth naming: a successor built on a branch that review later changes
// has to be rebased. That is the trade — the chain keeps moving, and the rework is
// bounded by how much review alters the predecessor.
//
// Whatever states this accepts, stackBase must know how to branch from. The two
// are one decision, and TestEligibleAndStackBaseAgree holds them together.
func (p *pass) eligible(t Task) (bool, string) {
	switch len(t.Predecessors) {
	case 0:
		return true, ""
	case 1:
		pred := t.Predecessors[0]
		switch p.state[pred] {
		case Review, Approved, Completed:
			return true, ""
		default:
			return false, fmt.Sprintf("waiting for %s to reach review (it is %s)", pred, p.stateOf(pred))
		}
	default:
		var pending []string
		for _, pred := range t.Predecessors {
			if p.state[pred] != Completed {
				pending = append(pending, fmt.Sprintf("%s (%s)", pred, p.stateOf(pred)))
			}
		}
		if len(pending) == 0 {
			return true, ""
		}
		return false, "waiting for " + strings.Join(pending, ", ") + " to complete"
	}
}

func (p *pass) stateOf(id string) string {
	if s, ok := p.state[id]; ok {
		return string(s)
	}
	return "missing"
}

// start creates the task directory and hands it to an agent.
//
// The move comes last. A task that started but whose file did not move would be
// started again next pass; task.Create then refuses the directory that already
// exists, so the duplicate fails loudly rather than producing a second agent.
func (p *pass) start(result *Result, t Task) {
	base, err := p.stackBase(t)
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", t.ID, err))
		return
	}

	created, err := task.Create(p.opts.Config, task.Options{
		Name:      t.ID,
		Repos:     t.Repositories,
		Now:       p.now,
		Artifacts: p.artifactsIfAny(),
		TaskFile:  p.w.TaskFile(Planned, t.ID),
		Workplan:  p.w.Name,
		TaskID:    t.ID,
		Base:      base,
	})
	if err != nil {
		result.Problems = append(result.Problems, fmt.Sprintf("%s: starting: %v", t.ID, err))
		return
	}

	prompt := task.TaskFilePrompt(p.opts.Config, created)
	agentID, launchErr := p.opts.Agents.Launch(created.Dir, "nm-"+created.Label(), prompt, created.Dirs())

	var note string
	switch {
	case launchErr != nil:
		// The task directory is built and correct; only the agent failed. Saying
		// so and moving the task anyway is right: the work exists and can be
		// picked up by hand, where undoing it would throw away worktrees.
		result.Problems = append(result.Problems,
			fmt.Sprintf("%s: started, but no agent: %v", t.ID, launchErr))
		note = "no agent"
	case agentID == "":
		// Launched, but its id could not be read. The record still says an agent
		// is here, and nm finds it by directory.
		note = "agent started"
	default:
		note = "agent " + agentID
	}

	if launchErr == nil {
		created.Prompt = prompt
		created.Agent = &task.Agent{ID: agentID, LaunchedAt: p.now()}
		if saveErr := created.Save(); saveErr != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", t.ID, saveErr))
		}
		if saveErr := created.SavePrompt(p.opts.Config); saveErr != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s: %v", t.ID, saveErr))
		}
	}
	p.move(result, t.ID, Planned, InProgress, note)
}

// artifactsIfAny returns the workplan's artifacts directory when it has anything
// in it, so an empty one is not copied as an empty one.
func (p *pass) artifactsIfAny() string {
	entries, err := os.ReadDir(p.w.Artifacts())
	if err != nil || len(entries) == 0 {
		return ""
	}
	return p.w.Artifacts()
}

// stackBase resolves where a task's branch is cut from.
//
// A task whose single predecessor is approved but not yet merged must branch from
// that predecessor's branch: main does not contain the work this task was scoped
// to build on, so starting there would duplicate or conflict with it. A completed
// predecessor is already in main, so the usual resolution is correct and nil is
// returned.
func (p *pass) stackBase(t Task) (*gitx.Base, error) {
	if len(t.Predecessors) != 1 {
		return nil, nil
	}
	pred := t.Predecessors[0]
	if _, stackable := stackableStates()[p.state[pred]]; !stackable {
		return nil, nil // completed: it is in the base branch already
	}
	if len(t.Repositories) == 0 {
		return nil, nil
	}

	predTask, ok := p.taskDir(pred)
	if !ok {
		return nil, fmt.Errorf("predecessor %s is at %s but its task directory is gone, "+
			"so there is nothing to stack on", pred, p.state[pred])
	}
	// Stacking is per repository, and Options carries one base. A task sharing
	// every repository with its predecessor is the case that matters; anything
	// else is reported rather than guessed at.
	for _, repo := range t.Repositories {
		if _, found := repoIn(predTask, repo); !found {
			return nil, fmt.Errorf("cannot stack on %s: it does not include %s", pred, repo)
		}
	}
	first, _ := repoIn(predTask, t.Repositories[0])
	head, err := gitx.Head(first.Dir)
	if err != nil {
		return nil, fmt.Errorf("reading the head of %s: %w", pred, err)
	}
	return &gitx.Base{Branch: first.Branch, Commit: head, Source: "predecessor " + pred}, nil
}

// stackableStates are the predecessor states whose work exists on a branch but is
// not in the base branch yet, so a successor has to be stacked on it.
//
// Completed is deliberately absent: it is merged, so the ordinary base already
// contains it and stacking would pin the successor to a branch it does not need.
//
// This is the single place the set is written down, because eligible and stackBase
// are one decision. A state the first accepts and the second does not would start a
// successor from main, silently missing the work it was scoped to build on — which
// looks like a successful start rather than a failure.
func stackableStates() map[State]struct{} {
	return map[State]struct{}{Review: {}, Approved: {}}
}

func repoIn(t task.Task, name string) (task.Repo, bool) {
	for _, r := range t.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return task.Repo{}, false
}

// ReadStamp reads the timestamp out of a marker file.
//
// A marker with no parseable time is an error rather than a zero time: a zero
// time would make every comment ever left look newer than the work, so every pass
// would treat every comment ever left as new.
//
// Exported because await-feedback has to read the same marker the same way. "Newer
// than the work" must mean one thing, or the agent and the orchestrator disagree
// about what a reviewer has already been answered on.
func ReadStamp(path string) (time.Time, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	for _, line := range strings.Split(string(blob), "\n") {
		if at, ok := parseStamp(strings.TrimSpace(line)); ok {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("%s holds no timestamp this can read", filepath.Base(path))
}

// StampLayouts are the timestamp spellings a marker may use: RFC 3339, and the
// filename-safe form escalations are named with.
var StampLayouts = []string{
	time.RFC3339,
	"2006-01-02-15-04-05",
	"2006-01-02 15:04:05",
}

func parseStamp(s string) (time.Time, bool) {
	// A markdown marker may wrap its timestamp in prose or a heading, so the
	// leading decoration is stripped before parsing.
	s = strings.TrimLeft(s, "#*_- \t")
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range StampLayouts {
		if at, err := time.Parse(layout, s); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// The refine latch is gone. It stopped a one-minute poller from starting one agent
// per pass during a refine round; now that a task keeps the agent that published it
// and the orchestrator starts nothing on feedback, there is no round to latch.
//
// task.RefiningFile survives so escalation collection keeps skipping a marker left
// by an older nm, which would otherwise be read as an escalation and shown to a
// human every pass.

// plural renders a count with its noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
