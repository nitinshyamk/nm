package workplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/task"
)

func TestAwaitReturnsImmediatelyWhenTheAnswerIsAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	escalation := filepath.Join(dir, "2026-05-01-08-34-02.md")
	if err := os.WriteFile(escalation, []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}
	answer := ResolutionFor(escalation)
	if err := os.WriteFile(answer, []byte("do the second thing"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No timeout is needed: an agent restarting after an answer landed must not
	// wait at all, so this would hang rather than fail if it were wrong.
	start := time.Now()
	got, err := Await(context.Background(), escalation, time.Minute)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if got != answer {
		t.Errorf("Await = %q, want %q", got, answer)
	}
	if elapsed := time.Since(start); elapsed > AwaitInterval {
		t.Errorf("Await took %s for an answer that was already there", elapsed)
	}
}

func TestAwaitSeesAnAnswerThatArrivesWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	escalation := filepath.Join(dir, "2026-05-01-08-34-02.md")
	if err := os.WriteFile(escalation, []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(AwaitInterval / 2)
		_ = os.WriteFile(ResolutionFor(escalation), []byte("answered"), 0o644)
	}()

	got, err := Await(context.Background(), escalation, 30*time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if filepath.Base(got) != "2026-05-01-08-34-02"+ResolutionSuffix {
		t.Errorf("Await = %q", got)
	}
}

// A timeout is an outcome the skill handles, not a crash: the escalation stays
// open and the answer can still arrive later.
func TestAwaitTimesOut(t *testing.T) {
	dir := t.TempDir()
	escalation := filepath.Join(dir, "2026-05-01-08-34-02.md")
	if err := os.WriteFile(escalation, []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Await(context.Background(), escalation, 10*time.Millisecond)
	if !errors.Is(err, ErrAwaitTimeout) {
		t.Fatalf("Await returned %v, want ErrAwaitTimeout", err)
	}
}

func TestAwaitRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	escalation := filepath.Join(dir, "2026-05-01-08-34-02.md")
	if err := os.WriteFile(escalation, []byte("stuck"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := Await(ctx, escalation, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Await returned %v, want context.Canceled", err)
	}
}

// A mistyped filename must fail rather than block, because blocking forever looks
// exactly like waiting for a slow human.
func TestAwaitRejectsAnEscalationThatDoesNotExist(t *testing.T) {
	_, err := Await(context.Background(), filepath.Join(t.TempDir(), "nope.md"), time.Minute)
	if err == nil {
		t.Fatal("Await accepted an escalation that does not exist")
	}
	if errors.Is(err, ErrAwaitTimeout) {
		t.Error("a missing escalation timed out rather than reporting the missing file")
	}
}

func TestAwaitRejectsAResolution(t *testing.T) {
	dir := t.TempDir()
	answer := filepath.Join(dir, "2026-05-01-08-34-02"+ResolutionSuffix)
	if err := os.WriteFile(answer, []byte("answered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Await(context.Background(), answer, time.Minute); err == nil {
		t.Error("Await accepted a resolution as the thing to wait for")
	}
}

func TestResolutionFor(t *testing.T) {
	got := ResolutionFor(filepath.Join("escalations", "2026-05-01-08-34-02.md"))
	want := filepath.Join("escalations", "2026-05-01-08-34-02"+ResolutionSuffix)
	if got != want {
		t.Errorf("ResolutionFor = %q, want %q", got, want)
	}
}

// The backoff schedule is the number that decides whether a persistent agent is
// affordable, so it is pinned rather than left to a reading of the table.
//
// A flat 30s is ~2900 calls a day per waiting task, and a workplan with eight tasks
// in review has eight agents doing it — against a rate limit shared with everything
// else using gh. The shape matters as much as the total: tight while a reviewer is
// still looking, wide once they have plainly gone.
func TestFeedbackIntervalWidensAsTheWaitGoesOn(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, 30 * time.Second},                // just published
		{4 * time.Minute, 30 * time.Second},  // still in the first window
		{5 * time.Minute, 2 * time.Minute},   // boundary: second window
		{20 * time.Minute, 2 * time.Minute},  // still in it
		{25 * time.Minute, 10 * time.Minute}, // boundary: third
		{2 * time.Hour, 10 * time.Minute},    // still in it
		{3 * time.Hour, time.Hour},           // boundary: hourly from here
		{48 * time.Hour, time.Hour},          // and it stays hourly
	}
	for _, tc := range cases {
		if got := feedbackInterval(tc.elapsed); got != tc.want {
			t.Errorf("feedbackInterval(%s) = %s, want %s", tc.elapsed, got, tc.want)
		}
	}
}

// A zero interval would spin, so no schedule entry may be zero however the table is
// edited. Cheap to assert, and the failure it prevents is a busy loop against the
// GitHub API from an unattended agent.
func TestFeedbackBackoffNeverReturnsZero(t *testing.T) {
	for _, elapsed := range []time.Duration{
		0, time.Second, time.Minute, time.Hour, 24 * time.Hour, 365 * 24 * time.Hour,
	} {
		if got := feedbackInterval(elapsed); got <= 0 {
			t.Errorf("feedbackInterval(%s) = %s, which would spin", elapsed, got)
		}
	}
}

// The total call count over a day, stated as a number so a change to the table that
// makes waiting expensive fails here rather than on someone's rate limit.
func TestADayOfWaitingIsCheap(t *testing.T) {
	var calls int
	for elapsed := time.Duration(0); elapsed < 24*time.Hour; {
		elapsed += feedbackInterval(elapsed)
		calls++
	}
	// 10 + 10 + ~10 + ~21 with the current table.
	if calls > 80 {
		t.Errorf("a day of waiting costs %d GitHub calls per task, which is too many "+
			"for eight tasks in review to share a rate limit", calls)
	}
	t.Logf("a day of waiting costs %d calls per task", calls)
}

// fakeWaiter answers with a scripted status and counts how often it was asked.
type fakeWaiter struct {
	status *forge.Status
	calls  int
	err    error
}

func (f *fakeWaiter) StatusForBranch(_, _ string) (*forge.Status, error) {
	f.calls++
	return f.status, f.err
}

func oneRepoTask(dir string) task.Task {
	return task.Task{Dir: dir, TaskID: "01-a", Repos: []task.Repo{{Name: "nm", Dir: dir, Branch: "b"}}}
}

// A verdict that is already true must come back without waiting out an interval.
//
// This is what makes a resumed wait cheap: an agent whose 12h wait timed out runs the
// command again, and a reviewer who commented in the meantime is reported at once
// rather than after another 30 seconds.
func TestAwaitFeedbackReturnsWhatIsAlreadyTrue(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		status *forge.Status
		want   Verdict
	}{
		"a comment after publishing": {
			status: &forge.Status{
				Number: 12, State: forge.StateOpen,
				Comments: []forge.Comment{{Author: "r", CreatedAt: published.Add(time.Minute)}},
			},
			want: VerdictFeedback,
		},
		"approved": {
			status: &forge.Status{Number: 12, State: forge.StateOpen, Decision: forge.DecisionApproved},
			want:   VerdictApproved,
		},
		// Merged ends the wait too. An agent blocked only on comments would sit
		// forever on the normal way a good task ends.
		"merged": {
			status: &forge.Status{Number: 12, State: forge.StateMerged},
			want:   VerdictMerged,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := &fakeWaiter{status: tc.status}
			start := time.Now()
			got, err := AwaitFeedback(context.Background(), w, oneRepoTask(t.TempDir()), published, time.Minute)
			if err != nil {
				t.Fatalf("AwaitFeedback: %v", err)
			}
			if got.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tc.want)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("took %s for something already true", elapsed)
			}
		})
	}
}

// Feedback older than the published timestamp is feedback already dealt with, and
// must not wake the agent: it would hand it the same comment forever.
func TestAwaitFeedbackIgnoresFeedbackOlderThanTheWork(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	w := &fakeWaiter{status: &forge.Status{
		Number: 12, State: forge.StateOpen,
		Comments: []forge.Comment{{Author: "r", CreatedAt: published.Add(-time.Hour)}},
	}}

	_, err := AwaitFeedback(context.Background(), w, oneRepoTask(t.TempDir()), published, 50*time.Millisecond)
	if !errors.Is(err, ErrFeedbackTimeout) {
		t.Errorf("err = %v, want a timeout: a comment older than the work is not new", err)
	}
}

// A task with no repositories has no pull request, so waiting would never end. It
// says so rather than blocking forever.
func TestAwaitFeedbackRefusesATaskWithNoPullRequest(t *testing.T) {
	_, err := AwaitFeedback(context.Background(), &fakeWaiter{},
		task.Task{TaskID: "01-a"}, time.Time{}, time.Minute)
	if err == nil {
		t.Fatal("a task with no repositories was allowed to wait")
	}
	if !strings.Contains(err.Error(), "escalation") {
		t.Errorf("the error does not say what to do instead: %v", err)
	}
}

// A forge error is not fatal. A blocked agent is the wrong place to die on a
// transient gh failure: exiting ends the round and loses the session, where waiting
// one more interval costs one call.
func TestAwaitFeedbackKeepsWaitingThroughAForgeError(t *testing.T) {
	w := &fakeWaiter{err: errors.New("gh: rate limited")}

	_, err := AwaitFeedback(context.Background(), w, oneRepoTask(t.TempDir()),
		time.Now(), 50*time.Millisecond)
	if !errors.Is(err, ErrFeedbackTimeout) {
		t.Errorf("err = %v, want a timeout rather than the forge error", err)
	}
}

// byBranchWaiter answers per branch, so a multi-repo task can have one pull request
// merged and another still open.
type byBranchWaiter struct {
	statuses map[string]*forge.Status
	calls    int
}

func (b *byBranchWaiter) StatusForBranch(_, branch string) (*forge.Status, error) {
	b.calls++
	return b.statuses[branch], nil
}

// twoRepoTask is a task spanning two repositories, each with its own branch.
func twoRepoTask(dir string) task.Task {
	return task.Task{Dir: dir, TaskID: "11-a", Repos: []task.Repo{
		{Name: "catalog", Dir: dir, Branch: "catalog-b"},
		{Name: "sidebar", Dir: dir, Branch: "sidebar-b"},
	}}
}

// One of two pull requests merged is not the task being finished, so the wait must
// keep waiting rather than telling the agent to stop.
//
// This was a real failure on two separate tasks. A task with a small catalog change and
// a large sidebar change had the small one merged first; the wait reported
// `merged laser-agentic-contract-migration#10` and exited zero — instantly, and forever
// after, because a merge never un-merges. The skill treats `merged` as "you are done —
// stop", so the agent that should have stayed to answer the reviewer on the still-open
// sidebar pull request had already left. Both agents that hit it noticed the verdict was
// wrong and substituted their own polling loop rather than obeying it.
func TestAwaitFeedbackKeepsWaitingWhileAnotherPullRequestIsOpen(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	w := &byBranchWaiter{statuses: map[string]*forge.Status{
		"catalog-b": {Number: 10, State: forge.StateMerged},
		"sidebar-b": {Number: 4953, State: forge.StateOpen, Decision: "REVIEW_REQUIRED"},
	}}

	_, err := AwaitFeedback(context.Background(), w, twoRepoTask(t.TempDir()), published, 50*time.Millisecond)
	if !errors.Is(err, ErrFeedbackTimeout) {
		t.Errorf("err = %v, want a timeout: one merged pull request is not a finished task", err)
	}
}

// Feedback on one pull request wakes the agent even while another is already merged.
//
// The rule is not "wait for everything": a reviewer's comment is something to action
// wherever it landed. Only the *terminal* verdicts — the ones that tell the agent to
// stop — wait for every repository to agree.
func TestAwaitFeedbackWakesOnFeedbackBesideAMergedPullRequest(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	commented := published.Add(time.Minute)
	w := &byBranchWaiter{statuses: map[string]*forge.Status{
		"catalog-b": {Number: 10, State: forge.StateMerged},
		"sidebar-b": {
			Number: 4953, State: forge.StateOpen,
			Comments: []forge.Comment{{Author: "reviewer", CreatedAt: commented}},
		},
	}}

	got, err := AwaitFeedback(context.Background(), w, twoRepoTask(t.TempDir()), published, time.Minute)
	if err != nil {
		t.Fatalf("AwaitFeedback: %v", err)
	}
	if got.Verdict != VerdictFeedback {
		t.Errorf("verdict = %q, want feedback", got.Verdict)
	}
	if got.Number != 4953 {
		t.Errorf("named pull request #%d, want the one with the comment (#4953)", got.Number)
	}
}

// Once every repository is finished the wait ends, and says which kind of finished.
func TestAwaitFeedbackEndsWhenEveryRepositoryIsFinished(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		statuses map[string]*forge.Status
		want     Verdict
		number   int
	}{
		"both merged": {
			statuses: map[string]*forge.Status{
				"catalog-b": {Number: 10, State: forge.StateMerged},
				"sidebar-b": {Number: 4953, State: forge.StateMerged},
			},
			want: VerdictMerged, number: 10,
		},
		"both approved": {
			statuses: map[string]*forge.Status{
				"catalog-b": {Number: 10, State: forge.StateOpen, Decision: forge.DecisionApproved},
				"sidebar-b": {Number: 4953, State: forge.StateOpen, Decision: forge.DecisionApproved},
			},
			want: VerdictApproved, number: 10,
		},
		// One merged and one approved is finished too. The approved one is named
		// because it is the pull request still open — the merged one can no longer be
		// pushed to, so it is the less useful of the two to point the agent at.
		"one merged, one approved": {
			statuses: map[string]*forge.Status{
				"catalog-b": {Number: 10, State: forge.StateMerged},
				"sidebar-b": {Number: 4953, State: forge.StateOpen, Decision: forge.DecisionApproved},
			},
			want: VerdictApproved, number: 4953,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := &byBranchWaiter{statuses: tc.statuses}
			got, err := AwaitFeedback(context.Background(), w, twoRepoTask(t.TempDir()), published, time.Minute)
			if err != nil {
				t.Fatalf("AwaitFeedback: %v", err)
			}
			if got.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tc.want)
			}
			if got.Number != tc.number {
				t.Errorf("named pull request #%d, want #%d", got.Number, tc.number)
			}
		})
	}
}

// A repository whose pull request cannot be read is not a finished one.
//
// Counting an unreadable pull request as finished would let a transient `gh` failure
// manufacture a "you are done" verdict — the one outcome an agent cannot come back
// from, since it exits on it.
func TestAwaitFeedbackDoesNotFinishOnAnUnreadablePullRequest(t *testing.T) {
	published := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	w := &byBranchWaiter{statuses: map[string]*forge.Status{
		"catalog-b": {Number: 10, State: forge.StateMerged},
		// sidebar-b is absent: the forge said it has no pull request.
	}}

	_, err := AwaitFeedback(context.Background(), w, twoRepoTask(t.TempDir()), published, 50*time.Millisecond)
	if !errors.Is(err, ErrFeedbackTimeout) {
		t.Errorf("err = %v, want a timeout: an unreadable pull request is not a finished one", err)
	}
}

// A cancelled context stops the wait, so stopping the agent stops its poller.
func TestAwaitFeedbackStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := AwaitFeedback(ctx, &fakeWaiter{}, oneRepoTask(t.TempDir()), time.Now(), 0)
	if err == nil || errors.Is(err, ErrFeedbackTimeout) {
		t.Errorf("err = %v, want the cancellation", err)
	}
}
