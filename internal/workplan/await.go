package workplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AwaitInterval is how often the wait checks for the resolution.
//
// A stat every second or two is free; what this command exists to avoid is an
// *agent* waking on a timer, because each of those wakeups reloads a whole
// conversation context. Moving the waiting into a blocked process is the point,
// not eliminating the polling.
const AwaitInterval = 2 * time.Second

// ErrAwaitTimeout means the resolution did not arrive in time.
var ErrAwaitTimeout = errors.New("no resolution arrived before the timeout")

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
