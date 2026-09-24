package workplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// StaleLockAfter is how long a lock may sit before it is assumed abandoned.
//
// Generous on purpose. The cost of breaking a live lock is two orchestrators
// starting the same task twice; the cost of waiting out a dead one is a workplan
// that stops moving until someone looks. The first is worse, so the timeout is
// long enough that no real pass reaches it.
const StaleLockAfter = 30 * time.Minute

// ErrLocked means another orchestrator run holds this workplan.
var ErrLocked = errors.New("another execute is running")

// lockRecord is what sits in the lock file, so a held lock can say who holds it.
type lockRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Host      string    `json:"host,omitempty"`
}

// Lock takes the workplan's exclusive lock, returning a function that releases
// it.
//
// Every transition is a file move, so two runs acting at once would each read
// the same planned task and each start an agent for it. The lock is the whole
// defence, which is why it is taken before anything is read rather than before
// anything is written.
func (w Workplan) TakeLock(now func() time.Time) (release func(), err error) {
	if now == nil {
		now = time.Now
	}
	path := w.Lock()

	blob, err := json.Marshal(lockRecord{
		PID:       os.Getpid(),
		StartedAt: now(),
		Host:      hostname(),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the lock: %w", err)
	}

	for attempt := range 2 {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, writeErr := file.Write(append(blob, '\n'))
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("writing %s: %w", path, errors.Join(writeErr, closeErr))
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("taking the lock: %w", err)
		}

		// Held. Break it only if it is old enough to be certainly abandoned, and
		// only once — a second collision means someone else won the race to
		// replace it, and that someone is now live.
		if attempt > 0 {
			break
		}
		held, readErr := readLock(path)
		age := now().Sub(held.StartedAt)
		switch {
		case readErr != nil:
			// An unreadable lock cannot be reasoned about. Its mtime is the only
			// evidence left, so it is treated as stale on the same timeout.
			info, statErr := os.Stat(path)
			if statErr != nil || now().Sub(info.ModTime()) < StaleLockAfter {
				return nil, fmt.Errorf("%w (its lock file could not be read)", ErrLocked)
			}
		case age < StaleLockAfter:
			return nil, fmt.Errorf("%w (pid %d, started %s ago)", ErrLocked, held.PID, age.Round(time.Second))
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("%w (its lock could not be cleared: %w)", ErrLocked, err)
		}
	}
	return nil, ErrLocked
}

func readLock(path string) (lockRecord, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return lockRecord{}, err
	}
	var held lockRecord
	if err := json.Unmarshal(blob, &held); err != nil {
		return lockRecord{}, err
	}
	if held.StartedAt.IsZero() {
		return lockRecord{}, errors.New("the lock has no start time")
	}
	return held, nil
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
