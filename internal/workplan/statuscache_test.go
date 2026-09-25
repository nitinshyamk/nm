package workplan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nitinshyamk/nm/internal/forge"
)

func cached(t *testing.T, inner FeedbackWaiter, now func() time.Time) CachedReviewer {
	t.Helper()
	return CachedReviewer{Inner: inner, Dir: t.TempDir(), Now: now}
}

// A second read inside the TTL must not reach the forge.
//
// The duplication this removes is between processes — a blocked agent and an
// orchestrator pass both asking about the same pull request — so it is worth a real
// file rather than a map.
func TestCachedReviewerServesARepeatReadFromDisk(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	c := cached(t, inner, func() time.Time { return at })

	first, err := c.StatusForBranch("d", "b")
	if err != nil || first == nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := c.StatusForBranch("d", "b")
	if err != nil || second == nil {
		t.Fatalf("second read: %v", err)
	}

	if inner.calls != 1 {
		t.Errorf("the forge was asked %d times, want 1", inner.calls)
	}
	if second.Number != 12 {
		t.Errorf("the cached answer lost its number: %+v", second)
	}
}

// A different process reading the same cache directory gets the same benefit. This
// is the case an in-memory cache cannot serve, and the reason this one is on disk.
func TestCachedReviewerIsSharedBetweenReaders(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	clock := func() time.Time { return at }

	// Two values, as two processes would be, sharing only the directory.
	one := CachedReviewer{Inner: inner, Dir: dir, Now: clock}
	two := CachedReviewer{Inner: inner, Dir: dir, Now: clock}

	if _, err := one.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := two.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}

	if inner.calls != 1 {
		t.Errorf("two readers cost %d calls, want 1", inner.calls)
	}
}

// Past the TTL it asks again, or a waiting agent would be served its own stale
// answer and never see the comment it is waiting for.
func TestCachedReviewerExpires(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	now := at
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	c := cached(t, inner, func() time.Time { return now })

	if _, err := c.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}
	now = at.Add(StatusTTL)
	if _, err := c.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}

	if inner.calls != 2 {
		t.Errorf("the forge was asked %d times, want 2: the entry had expired", inner.calls)
	}
}

// The TTL has to stay under the tightest poll interval, or a single waiting agent
// polls and is handed its own cached answer — waiting twice as long as it should for
// news it already paid for.
func TestStatusTTLIsShorterThanTheTightestPoll(t *testing.T) {
	tightest := FeedbackBackoff[0].Every
	if StatusTTL >= tightest {
		t.Errorf("StatusTTL is %s but the tightest poll is every %s, so a lone waiting "+
			"agent would be served a stale answer", StatusTTL, tightest)
	}
}

// "No pull request yet" is cached too. Otherwise the state a task is in for as long
// as it takes to build is the one state that is never cached, and so the most
// expensive to poll.
func TestCachedReviewerCachesAMissingPullRequest(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	inner := &fakeWaiter{status: nil}
	c := cached(t, inner, func() time.Time { return at })

	for range 3 {
		status, err := c.StatusForBranch("d", "b")
		if err != nil {
			t.Fatal(err)
		}
		if status != nil {
			t.Errorf("status = %+v, want nil", status)
		}
	}
	if inner.calls != 1 {
		t.Errorf("a missing pull request cost %d calls, want 1", inner.calls)
	}
}

// An error is never cached. A gh hiccup is transient, and caching it would turn one
// bad call into a quarter-minute of pretending the pull request is unreadable.
func TestCachedReviewerDoesNotCacheErrors(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	inner := &fakeWaiter{err: errors.New("gh: rate limited")}
	c := cached(t, inner, func() time.Time { return at })

	for range 3 {
		if _, err := c.StatusForBranch("d", "b"); err == nil {
			t.Fatal("the error was swallowed")
		}
	}
	if inner.calls != 3 {
		t.Errorf("an error was cached (%d calls, want 3)", inner.calls)
	}
}

// Two branches, and the same branch in two repositories, are different pull
// requests. A key that collapsed either would serve one task's review state for
// another's.
func TestCachedReviewerKeysOnRepositoryAndBranch(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	c := cached(t, inner, func() time.Time { return at })

	for _, pair := range [][2]string{
		{"repo-one", "b"},
		{"repo-two", "b"}, // same branch name, different repository
		{"repo-one", "other"},
	} {
		if _, err := c.StatusForBranch(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 3 {
		t.Errorf("distinct pull requests shared a cache entry (%d calls, want 3)", inner.calls)
	}
}

// A corrupt entry is a miss, not a failure. The cache can only make a caller slower,
// never wrong about review state.
func TestCachedReviewerTreatsGarbageAsAMiss(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	c := CachedReviewer{Inner: inner, Dir: dir, Now: func() time.Time { return at }}

	if _, err := c.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}
	// Corrupt whatever it wrote.
	entries, err := os.ReadDir(filepath.Join(dir, StatusCacheDir))
	if err != nil || len(entries) == 0 {
		t.Fatalf("nothing was cached: %v", err)
	}
	path := filepath.Join(dir, StatusCacheDir, entries[0].Name())
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := c.StatusForBranch("d", "b")
	if err != nil {
		t.Fatalf("a corrupt entry became an error: %v", err)
	}
	if status == nil || status.Number != 12 {
		t.Errorf("status = %+v, want the forge's answer", status)
	}
	if inner.calls != 2 {
		t.Errorf("calls = %d, want 2: the corrupt entry should have been re-read", inner.calls)
	}
}

// An entry stamped in the future means the clock moved. Honouring it would make an
// entry that never expires, so it is treated as a miss.
func TestCachedReviewerRejectsAFutureEntry(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	now := at.Add(time.Hour)
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	c := cached(t, inner, func() time.Time { return now })

	if _, err := c.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}
	now = at // the clock went backwards, so the entry is now in the future
	if _, err := c.StatusForBranch("d", "b"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Errorf("a future-stamped entry was honoured (%d calls, want 2)", inner.calls)
	}
}

// An unwritable cache directory must not break the wait. The caller already has its
// answer; failing here would turn a read-only directory into a broken review round.
func TestCachedReviewerSurvivesAnUnwritableDirectory(t *testing.T) {
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	inner := &fakeWaiter{status: &forge.Status{Number: 12, State: forge.StateOpen}}
	// A file where the cache directory should be, so MkdirAll cannot succeed. This
	// works the same on Windows, where chmod does not.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StatusCacheDir), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CachedReviewer{Inner: inner, Dir: dir, Now: func() time.Time { return at }}

	status, err := c.StatusForBranch("d", "b")
	if err != nil {
		t.Fatalf("an unwritable cache broke the read: %v", err)
	}
	if status == nil || status.Number != 12 {
		t.Errorf("status = %+v, want the forge's answer", status)
	}
}
