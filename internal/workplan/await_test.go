package workplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
