package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Review is one submitted review on a pull request.
type Review struct {
	Author      string    `json:"-"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// Comment is one comment on a pull request: an issue comment, a review's summary
// body, or an inline comment on a line.
type Comment struct {
	Author    string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
}

// Status is everything the orchestrator needs to know about one pull request to
// decide what state its task is in.
type Status struct {
	Number    int
	URL       string
	State     string // OPEN, MERGED, CLOSED
	IsDraft   bool
	Decision  string // reviewDecision: APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	Mergeable string // MERGEABLE, CONFLICTING, UNKNOWN
	MergeInfo string // mergeStateStatus: CLEAN, BLOCKED, BEHIND, DIRTY, ...
	Reviews   []Review
	Comments  []Comment
}

// Review decisions and merge states worth naming, so a call site compares
// against a constant rather than retyping a GitHub enum.
const (
	DecisionApproved         = "APPROVED"
	DecisionChangesRequested = "CHANGES_REQUESTED"
	MergeableYes             = "MERGEABLE"
	StateMerged              = "MERGED"
	StateOpen                = "OPEN"
)

// Approved reports whether an authorized reviewer has approved the pull request
// as it currently stands.
//
// This reads GitHub's own reviewDecision rather than scanning for a review whose
// state is APPROVED, and the difference matters: reviewDecision resets when new
// commits land, so an approval of a revision that has since been rewritten does
// not count. Scanning the review list would report that stale approval as
// current.
func (s Status) Approved() bool { return s.Decision == DecisionApproved }

// Merged reports whether the pull request is already in the base branch.
func (s Status) Merged() bool { return s.State == StateMerged }

// FeedbackAfter reports the most recent review or comment newer than at, and
// whether there was one.
//
// This is what decides a refine round: work published at a known time, then any
// human activity after it is feedback to act on. Reviews and comments are read
// together because a reviewer may leave either, and treating one as
// authoritative would miss half of what they said.
func (s Status) FeedbackAfter(at time.Time) (time.Time, bool) {
	var latest time.Time
	for _, r := range s.Reviews {
		if r.SubmittedAt.After(at) && r.SubmittedAt.After(latest) {
			latest = r.SubmittedAt
		}
	}
	for _, c := range s.Comments {
		if c.CreatedAt.After(at) && c.CreatedAt.After(latest) {
			latest = c.CreatedAt
		}
	}
	return latest, !latest.IsZero()
}

// Reviewers lists who has reviewed, most recent first, for reporting which
// humans are already involved.
func (s Status) Reviewers() []string {
	seen := make(map[string]time.Time)
	for _, r := range s.Reviews {
		if r.Author == "" {
			continue
		}
		if at, ok := seen[r.Author]; !ok || r.SubmittedAt.After(at) {
			seen[r.Author] = r.SubmittedAt
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return seen[names[i]].After(seen[names[j]]) })
	return names
}

// statusFields is what gh is asked for. Keeping it in one place means the parser
// and the request can never disagree about which fields exist.
var statusFields = []string{
	"number", "url", "state", "isDraft",
	"reviewDecision", "mergeable", "mergeStateStatus",
	"reviews", "comments",
}

// Status reads everything about one pull request in a single gh invocation.
//
// One call rather than one per field: the orchestrator reads every pull request
// in a workplan on every pass, and a pass that shells out five times per task
// would spend its whole minute in process startup.
func (c Client) Status(dir string, number int) (*Status, error) {
	out, err := c.run(dir, "pr", "view", fmt.Sprint(number),
		"--json", strings.Join(statusFields, ","))
	if err != nil {
		return nil, err
	}
	return ParseStatus(out)
}

// StatusForBranch reads the most recent pull request for a branch, whatever state
// it is in, or nil when the branch has never had one.
//
// It deliberately does not use Find, which filters to open pull requests. A merged
// pull request is the normal end of a task's life, and a caller deciding what state
// that task is in needs to see it — where Find would answer "there is no pull
// request" and leave the task looking like work that was never published. That was
// a real bug: a merged task sat in review reporting "0 of 1 open" forever.
func (c Client) StatusForBranch(dir, branch string) (*Status, error) {
	out, err := c.run(dir, "pr", "list", "--head", branch, "--state", "all",
		"--json", strings.Join(statusFields, ","), "--limit", "1")
	if err != nil {
		return nil, err
	}
	return ParseStatusList(out)
}

// ParseStatusList decodes `gh pr list --json <status fields>` output, which is an
// array, and returns the first entry.
func ParseStatusList(out string) (*Status, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var raw []ghStatus
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("reading the pull request list: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return statusFrom(raw[0]), nil
}

// ghStatus mirrors `gh pr view --json`, whose author fields are nested objects
// rather than the plain logins the rest of nm works with.
type ghStatus struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
	Mergeable      string `json:"mergeable"`
	MergeState     string `json:"mergeStateStatus"`
	Reviews        []struct {
		Author      struct{ Login string } `json:"author"`
		State       string                 `json:"state"`
		SubmittedAt time.Time              `json:"submittedAt"`
	} `json:"reviews"`
	Comments []struct {
		Author    struct{ Login string } `json:"author"`
		CreatedAt time.Time              `json:"createdAt"`
		URL       string                 `json:"url"`
	} `json:"comments"`
}

// ParseStatus decodes `gh pr view --json` output.
//
// Unknown fields are ignored, as everywhere else nm reads gh: a new GitHub field
// must not break a workplan that is mid-flight.
func ParseStatus(out string) (*Status, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	var raw ghStatus
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("reading the pull request: %w", err)
	}
	return statusFrom(raw), nil
}

// statusFrom flattens gh's nested author objects into the plain logins the rest of
// nm works with.
func statusFrom(raw ghStatus) *Status {
	s := &Status{
		Number:    raw.Number,
		URL:       raw.URL,
		State:     raw.State,
		IsDraft:   raw.IsDraft,
		Decision:  raw.ReviewDecision,
		Mergeable: raw.Mergeable,
		MergeInfo: raw.MergeState,
	}
	for _, r := range raw.Reviews {
		s.Reviews = append(s.Reviews, Review{
			Author:      r.Author.Login,
			State:       r.State,
			SubmittedAt: r.SubmittedAt,
		})
	}
	for _, c := range raw.Comments {
		s.Comments = append(s.Comments, Comment{
			Author:    c.Author.Login,
			CreatedAt: c.CreatedAt,
			URL:       c.URL,
		})
	}
	return s
}

// ErrNotMergeable means GitHub will not merge the pull request as it stands.
var ErrNotMergeable = errors.New("the pull request is not mergeable")

// Merge merges a pull request into its base branch.
//
// It re-reads the pull request first and refuses one GitHub says is not
// mergeable, or that is already closed. A conflicted branch is something to
// report, not something to force — and because the caller is an orchestrator
// running unattended, that refusal belongs here rather than in whoever
// remembered to check first.
//
// The re-read is not redundant with the caller's own: between the pass that
// decided to merge and this call, someone may have pushed to the base.
func (c Client) Merge(dir string, number int, method string) error {
	status, err := c.Status(dir, number)
	if err != nil {
		return err
	}
	if status == nil {
		return fmt.Errorf("%w: it could not be read", ErrNotMergeable)
	}
	switch {
	case status.Merged():
		return nil // already there; merging again is not a failure
	case status.State != StateOpen:
		return fmt.Errorf("%w: it is %s", ErrNotMergeable, strings.ToLower(status.State))
	case status.IsDraft:
		return fmt.Errorf("%w: it is still a draft", ErrNotMergeable)
	case status.Mergeable != MergeableYes:
		return fmt.Errorf("%w: GitHub reports %s (%s)",
			ErrNotMergeable, strings.ToLower(status.Mergeable), strings.ToLower(status.MergeInfo))
	}

	if method == "" {
		method = "--squash"
	}
	// --delete-branch=false: the branch is a task's worktree branch, and deleting
	// it from under a worktree that still exists would strand it.
	_, err = c.run(dir, "pr", "merge", fmt.Sprint(number), method, "--delete-branch=false")
	return err
}
