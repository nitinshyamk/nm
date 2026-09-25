package workplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/task"
)

// AwaitInterval is how often the wait checks for the resolution.
//
// A stat every second or two is free; what this command exists to avoid is an
// *agent* waking on a timer, because each of those wakeups reloads a whole
// conversation context. Moving the waiting into a blocked process is the point,
// not eliminating the polling.
const AwaitInterval = 2 * time.Second

// FeedbackBackoff is how often a wait for review feedback asks the forge, widening
// as the wait goes on.
//
// This is the number that decides whether a persistent agent is affordable, because
// unlike Await — which stats a local file for free — every tick here spends a GitHub
// API call. A flat 30s is 120 calls an hour per waiting task, and a workplan with
// eight tasks in review is eight agents doing that: ~23k calls a day against a 5000/h
// secondary limit shared with everything else using `gh`.
//
// The schedule is shaped to how review actually arrives. A reviewer who just looked
// is likely to say something else within minutes, so the first window is tight; a
// pull request nobody has touched for two hours is waiting on someone's next working
// day, and asking every 30 seconds until then buys nothing. Same total responsiveness
// where it matters, ~40 calls in the first day instead of ~2900.
var FeedbackBackoff = []struct {
	Every time.Duration
	For   time.Duration // 0 means "from here on"
}{
	{Every: 30 * time.Second, For: 5 * time.Minute},
	{Every: 2 * time.Minute, For: 20 * time.Minute},
	{Every: 10 * time.Minute, For: 2 * time.Hour},
	{Every: time.Hour},
}

// feedbackInterval is how long to wait before the next check, given how long the
// wait has already been going.
func feedbackInterval(elapsed time.Duration) time.Duration {
	var through time.Duration
	for _, step := range FeedbackBackoff {
		if step.For == 0 {
			return step.Every
		}
		through += step.For
		if elapsed < through {
			return step.Every
		}
	}
	// Unreachable while the table ends in an open-ended step, but a caller editing
	// the table should get the widest interval rather than a zero one, which would
	// spin.
	return FeedbackBackoff[len(FeedbackBackoff)-1].Every
}

// ErrAwaitTimeout means the resolution did not arrive in time.
var ErrAwaitTimeout = errors.New("no resolution arrived before the timeout")

// ErrFeedbackTimeout means no reviewer acted before the timeout.
var ErrFeedbackTimeout = errors.New("no review feedback arrived before the timeout")

// ResolutionFor returns the path a resolution to this escalation would have.
func ResolutionFor(escalation string) string {
	dir, name := filepath.Split(escalation)
	return filepath.Join(dir, strings.TrimSuffix(name, ".md")+ResolutionSuffix)
}

// Await blocks until the resolution to an escalation exists, and returns its
// path.
//
// It returns immediately when the resolution is already there, so an agent that
// restarted after one landed does not wait for nothing.
func Await(ctx context.Context, escalation string, timeout time.Duration) (string, error) {
	if IsResolution(filepath.Base(escalation)) {
		return "", fmt.Errorf("%s is already a resolution", filepath.Base(escalation))
	}
	// The escalation itself has to exist, or this would block forever on a
	// mistyped filename — which looks exactly like waiting for a slow human.
	if _, err := os.Stat(escalation); err != nil {
		return "", fmt.Errorf("reading %s: %w", escalation, err)
	}

	target := ResolutionFor(escalation)
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	ticker := time.NewTicker(AwaitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", ErrAwaitTimeout
			}
			return "", ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(target); err == nil {
				return target, nil
			}
		}
	}
}

// Verdict is what a wait for review feedback ended on.
type Verdict string

// The three ways a review round can end. They are distinguished because the agent
// does something different for each, and collapsing them would make it guess: a
// task that was approved needs nothing, and one that was merged has no pull request
// left to push to.
const (
	VerdictFeedback Verdict = "feedback" // a reviewer said something to action
	VerdictApproved Verdict = "approved" // approved as it stands; the work is done
	VerdictMerged   Verdict = "merged"   // already in the base branch
)

// FeedbackWaiter is the slice of the forge a feedback wait needs. An interface so
// the wait is testable without a network or a GitHub login.
type FeedbackWaiter interface {
	StatusForBranch(dir, branch string) (*forge.Status, error)
}

// Feedback is what a wait ended on, and where.
type Feedback struct {
	Verdict Verdict
	Repo    string    // which repository's pull request moved
	Number  int       // its pull request number
	At      time.Time // when the feedback was left, for VerdictFeedback
}

// String renders a verdict the way await-feedback prints it.
func (f Feedback) String() string {
	line := fmt.Sprintf("%s %s#%d", f.Verdict, f.Repo, f.Number)
	if !f.At.IsZero() {
		line += " at " + f.At.Format(time.RFC3339)
	}
	return line
}

// AwaitFeedback blocks until a reviewer acts on a task's pull requests.
//
// This is what lets one agent own a task end to end. Without it the agent has to
// exit after publishing and the orchestrator has to start a second one for each
// review round — which loses everything the first agent knew, and means two
// processes can end up in one worktree.
//
// It is deliberately *not* modelled on Await. That one stats a local file every two
// seconds because the cost is nothing; this one spends a GitHub call per tick, so
// the interval is long and the caller is expected to pass a generous timeout rather
// than a tight one.
//
// Approval and merge end the wait as well as comments. An agent blocked only on
// comments would sit forever on a pull request that was approved and merged without
// anyone typing anything, which is the normal way a good task ends.
func AwaitFeedback(ctx context.Context, w FeedbackWaiter, t task.Task, since time.Time, timeout time.Duration) (Feedback, error) {
	return awaitFeedback(ctx, w, t, since, timeout, time.Now)
}

// awaitFeedback is AwaitFeedback with the clock injected, so a test can drive the
// backoff schedule without waiting out its real intervals.
func awaitFeedback(ctx context.Context, w FeedbackWaiter, t task.Task, since time.Time, timeout time.Duration, clock func() time.Time) (Feedback, error) {
	if len(t.Repos) == 0 {
		// A task that changes no code has no pull request, so there is nothing here
		// to wait for and waiting would never end. Its review is an escalation.
		return Feedback{}, errors.New("this task has no repositories, so it has no pull request to watch; " +
			"wait on its escalation instead")
	}

	// Checked before the first tick, so an agent that restarted after a reviewer
	// had already spoken does not wait out a whole interval for news it could have
	// had immediately.
	if found, ok := pollFeedback(w, t, since); ok {
		return found, nil
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// A timer rather than a ticker, because the interval widens: each tick arms the
	// next one from the schedule instead of repeating a fixed period.
	started := clock()
	timer := time.NewTimer(feedbackInterval(0))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Feedback{}, ErrFeedbackTimeout
			}
			return Feedback{}, ctx.Err()
		case <-timer.C:
			if found, ok := pollFeedback(w, t, since); ok {
				return found, nil
			}
			timer.Reset(feedbackInterval(clock().Sub(started)))
		}
	}
}

// pollFeedback reads every repository's pull request once and reports the first
// thing worth waking for.
//
// A read that errors is treated as "nothing yet" rather than fatal. A blocked agent
// is the wrong place to fail on a transient `gh` hiccup: exiting would end the round
// and lose the session, where waiting another interval costs one more call.
func pollFeedback(w FeedbackWaiter, t task.Task, since time.Time) (Feedback, bool) {
	for _, repo := range t.Repos {
		status, err := w.StatusForBranch(repo.Dir, repo.Branch)
		if err != nil || status == nil {
			continue
		}
		// Merged first, then approved: both are terminal, and a merged pull request
		// that was also approved is more usefully reported as merged, because there
		// is no longer anything to push to.
		switch {
		case status.Merged():
			return Feedback{Verdict: VerdictMerged, Repo: repo.Name, Number: status.Number}, true
		case status.Approved():
			return Feedback{Verdict: VerdictApproved, Repo: repo.Name, Number: status.Number}, true
		}
		if at, ok := status.FeedbackAfter(since); ok {
			return Feedback{Verdict: VerdictFeedback, Repo: repo.Name, Number: status.Number, At: at}, true
		}
	}
	return Feedback{}, false
}
