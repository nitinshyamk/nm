package forge

import (
	"testing"
	"time"
)

// realOutput is `gh pr view <n> --json number,url,state,isDraft,reviewDecision,
// mergeable,mergeStateStatus,reviews,comments` captured verbatim from a live pull
// request, trimmed to three reviews and two comments.
//
// Verbatim matters: the author fields are nested objects rather than the plain
// logins the rest of nm works with, and a hand-written fixture is exactly where
// that detail would get smoothed over and the parser would be wrong in a way no
// test noticed.
const realOutput = `{
  "number": 15365,
  "url": "https://github.com/Hadrian-MTV/hadrian/pull/15365",
  "state": "OPEN",
  "isDraft": false,
  "reviewDecision": "APPROVED",
  "mergeable": "MERGEABLE",
  "mergeStateStatus": "BLOCKED",
  "headRefName": "wow/setup-station-status",
  "reviews": [
    {"author": {"login": "coderabbitai"}, "state": "COMMENTED", "submittedAt": "2026-09-24T00:23:39Z"},
    {"author": {"login": "will-wow"}, "state": "COMMENTED", "submittedAt": "2026-09-24T00:54:39Z"},
    {"author": {"login": "tedfous-hadrian"}, "state": "APPROVED", "submittedAt": "2026-09-24T01:06:06Z"}
  ],
  "comments": [
    {"author": {"login": "coderabbitai"}, "createdAt": "2026-09-24T00:15:33Z", "url": "https://github.com/o/r/pull/15365#issuecomment-1"},
    {"author": {"login": "datadog-hadrian"}, "createdAt": "2026-09-24T00:44:44Z", "url": "https://github.com/o/r/pull/15365#issuecomment-2"}
  ]
}`

func at(s string) time.Time {
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestParseStatusReadsRealGHOutput(t *testing.T) {
	s, err := ParseStatus(realOutput)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if s == nil {
		t.Fatal("ParseStatus returned nil for real output")
	}

	if s.Number != 15365 {
		t.Errorf("Number = %d", s.Number)
	}
	if !s.Approved() {
		t.Errorf("Approved() = false, want true for reviewDecision %q", s.Decision)
	}
	if s.Merged() {
		t.Error("Merged() = true for an OPEN pull request")
	}
	if s.Mergeable != MergeableYes || s.MergeInfo != "BLOCKED" {
		t.Errorf("Mergeable/MergeInfo = %q/%q", s.Mergeable, s.MergeInfo)
	}

	// The nested author objects have to become plain logins.
	if len(s.Reviews) != 3 {
		t.Fatalf("Reviews = %d, want 3", len(s.Reviews))
	}
	if s.Reviews[0].Author != "coderabbitai" {
		t.Errorf("Reviews[0].Author = %q, want the login out of the nested object", s.Reviews[0].Author)
	}
	if !s.Reviews[2].SubmittedAt.Equal(at("2026-09-24T01:06:06Z")) {
		t.Errorf("Reviews[2].SubmittedAt = %v", s.Reviews[2].SubmittedAt)
	}
	if len(s.Comments) != 2 {
		t.Fatalf("Comments = %d, want 2", len(s.Comments))
	}
	if s.Comments[1].Author != "datadog-hadrian" {
		t.Errorf("Comments[1].Author = %q", s.Comments[1].Author)
	}
}

// A pull request with no reviews yet is the normal case for freshly published
// work, not an error.
func TestParseStatusWithNoReviewsOrComments(t *testing.T) {
	s, err := ParseStatus(`{"number":1,"url":"u","state":"OPEN","isDraft":false,
		"reviewDecision":"","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN",
		"reviews":[],"comments":[]}`)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if s.Approved() {
		t.Error("an empty reviewDecision must not read as approved")
	}
	if _, ok := s.FeedbackAfter(time.Time{}); ok {
		t.Error("FeedbackAfter found feedback on a pull request with none")
	}
	if got := s.Reviewers(); len(got) != 0 {
		t.Errorf("Reviewers = %v, want none", got)
	}
}

func TestParseStatusOnEmptyAndGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		s, err := ParseStatus(in)
		if err != nil {
			t.Errorf("ParseStatus(%q): %v", in, err)
		}
		if s != nil {
			t.Errorf("ParseStatus(%q) invented %+v", in, s)
		}
	}
	if _, err := ParseStatus("not json"); err == nil {
		t.Error("ParseStatus accepted output that is not JSON")
	}
}

// A field GitHub adds later must not break a workplan that is mid-flight.
func TestParseStatusIgnoresUnknownFields(t *testing.T) {
	s, err := ParseStatus(`{"number":7,"state":"OPEN","somethingNew":{"a":1},"reviews":[],"comments":[]}`)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if s.Number != 7 {
		t.Errorf("Number = %d, want 7", s.Number)
	}
}

// CHANGES_REQUESTED is not approval, and neither is an APPROVED review sitting in
// the list once reviewDecision has reset. This is the distinction that keeps a
// stale approval from moving a task forward.
func TestApprovedReadsTheDecisionNotTheReviewList(t *testing.T) {
	withStaleApproval := `{"number":1,"state":"OPEN","reviewDecision":"CHANGES_REQUESTED",
		"reviews":[{"author":{"login":"a"},"state":"APPROVED","submittedAt":"2026-01-01T00:00:00Z"}],
		"comments":[]}`
	s, err := ParseStatus(withStaleApproval)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved() {
		t.Error("Approved() trusted an APPROVED review after reviewDecision reset to CHANGES_REQUESTED")
	}

	reviewRequired := `{"number":1,"state":"OPEN","reviewDecision":"REVIEW_REQUIRED","reviews":[],"comments":[]}`
	s, err = ParseStatus(reviewRequired)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved() {
		t.Error("REVIEW_REQUIRED read as approved")
	}
}

func TestFeedbackAfter(t *testing.T) {
	s, err := ParseStatus(realOutput)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		since string
		want  bool
		at    string
	}{
		"before everything": {since: "2026-09-23T00:00:00Z", want: true, at: "2026-09-24T01:06:06Z"},
		"mid-stream":        {since: "2026-09-24T00:50:00Z", want: true, at: "2026-09-24T01:06:06Z"},
		"after everything":  {since: "2026-09-24T02:00:00Z", want: false},
		// Exactly the timestamp of the last review: After is strict, so work
		// published at the same instant as a review is not feedback on it.
		"exactly the last": {since: "2026-09-24T01:06:06Z", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := s.FeedbackAfter(at(tc.since))
			if ok != tc.want {
				t.Fatalf("FeedbackAfter(%s) ok = %v, want %v", tc.since, ok, tc.want)
			}
			if tc.want && !got.Equal(at(tc.at)) {
				t.Errorf("FeedbackAfter = %v, want %v", got, at(tc.at))
			}
		})
	}
}

// A comment newer than every review still counts: a reviewer may leave either,
// and reading only one would miss half of what they said.
func TestFeedbackAfterSeesACommentNewerThanEveryReview(t *testing.T) {
	s, err := ParseStatus(`{"number":1,"state":"OPEN","reviewDecision":"",
		"reviews":[{"author":{"login":"a"},"state":"COMMENTED","submittedAt":"2026-01-01T00:00:00Z"}],
		"comments":[{"author":{"login":"b"},"createdAt":"2026-06-01T00:00:00Z","url":"u"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.FeedbackAfter(at("2026-03-01T00:00:00Z"))
	if !ok {
		t.Fatal("FeedbackAfter missed a comment with no review after it")
	}
	if !got.Equal(at("2026-06-01T00:00:00Z")) {
		t.Errorf("FeedbackAfter = %v, want the comment's time", got)
	}
}

func TestReviewersMostRecentFirst(t *testing.T) {
	s, err := ParseStatus(realOutput)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Reviewers()
	want := []string{"tedfous-hadrian", "will-wow", "coderabbitai"}
	if len(got) != len(want) {
		t.Fatalf("Reviewers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Reviewers = %v, want %v", got, want)
		}
	}
}

// One reviewer who reviewed twice is one reviewer, listed at their latest review.
func TestReviewersDeduplicates(t *testing.T) {
	s, err := ParseStatus(`{"number":1,"state":"OPEN","reviews":[
		{"author":{"login":"a"},"state":"COMMENTED","submittedAt":"2026-01-01T00:00:00Z"},
		{"author":{"login":"a"},"state":"APPROVED","submittedAt":"2026-02-01T00:00:00Z"}],
		"comments":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Reviewers(); len(got) != 1 || got[0] != "a" {
		t.Errorf("Reviewers = %v, want just [a]", got)
	}
}

func TestMergedReadsTheState(t *testing.T) {
	s, err := ParseStatus(`{"number":1,"state":"MERGED","reviews":[],"comments":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Merged() {
		t.Error("Merged() = false for a MERGED pull request")
	}
}

// statusFields is what gh is asked for; the parser has to understand every one of
// them, or nm pays for a field it then throws away.
func TestStatusFieldsAreAllParsed(t *testing.T) {
	for _, field := range statusFields {
		switch field {
		case "number", "url", "state", "isDraft", "reviewDecision",
			"mergeable", "mergeStateStatus", "reviews", "comments":
		default:
			t.Errorf("statusFields asks gh for %q, which ParseStatus does not read", field)
		}
	}
}

// The real client must satisfy the interfaces its callers declare, or a fake
// passes the tests while the production path does not compile.
func TestClientSatisfiesItsInterfaces(t *testing.T) {
	var c any = Client{}
	if _, ok := c.(interface {
		CheckAuth() error
		StatusForBranch(dir, branch string) (*Status, error)
		Merge(dir string, number int, method string) error
	}); !ok {
		t.Error("Client does not satisfy the reviewing interface the orchestrator takes")
	}
}

// A merged pull request has to be readable, because merging is how a task's life
// normally ends.
//
// StatusForBranch used to go through Find, which filters to --state open, so a
// merged pull request came back as nil and the task it belonged to looked like work
// that was never published. It sat in review reporting "0 of 1 open" forever — the
// normal path was the broken one.
func TestParseStatusListReadsAMergedPullRequest(t *testing.T) {
	// `gh pr list --json ...` answers with an array, unlike `gh pr view`.
	out := `[{"number":8,"url":"https://github.com/o/r/pull/8","state":"MERGED","isDraft":false,
		"reviewDecision":"","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN",
		"reviews":[],"comments":[]}]`

	s, err := ParseStatusList(out)
	if err != nil {
		t.Fatalf("ParseStatusList: %v", err)
	}
	if s == nil {
		t.Fatal("a merged pull request read as nil")
	}
	if !s.Merged() {
		t.Errorf("Merged() = false for state %q", s.State)
	}
	if s.Number != 8 {
		t.Errorf("Number = %d, want 8", s.Number)
	}
	// Merged without a review is the case that matters here: the merge is the
	// stronger signal, and reviewDecision stays empty.
	if s.Approved() {
		t.Error("a merged pull request with no review reported itself as approved")
	}
}

func TestParseStatusListOnEmptyAndGarbage(t *testing.T) {
	for _, in := range []string{"", "  ", "[]"} {
		s, err := ParseStatusList(in)
		if err != nil {
			t.Errorf("ParseStatusList(%q): %v", in, err)
		}
		if s != nil {
			t.Errorf("ParseStatusList(%q) invented %+v", in, s)
		}
	}
	if _, err := ParseStatusList("not json"); err == nil {
		t.Error("ParseStatusList accepted output that is not JSON")
	}
}

// Both parsers must agree, since one reads `gh pr view` and the other
// `gh pr list` over the same fields.
func TestBothParsersAgree(t *testing.T) {
	fromView, err := ParseStatus(realOutput)
	if err != nil {
		t.Fatal(err)
	}
	fromList, err := ParseStatusList("[" + realOutput + "]")
	if err != nil {
		t.Fatal(err)
	}
	if fromView.Number != fromList.Number || fromView.Decision != fromList.Decision ||
		len(fromView.Reviews) != len(fromList.Reviews) ||
		len(fromView.Comments) != len(fromList.Comments) {
		t.Errorf("the two parsers disagree:\n view = %+v\n list = %+v", fromView, fromList)
	}
}
