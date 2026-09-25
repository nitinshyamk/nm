package workplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nitinshyamk/nm/internal/forge"
)

// StatusTTL is how long a cached pull request status is treated as current.
//
// Slightly under the tightest poll interval in FeedbackBackoff, so a single waiting
// agent is never served its own stale answer — the cache exists to stop *different*
// readers from each spending a call on the same pull request, not to slow one reader
// down.
const StatusTTL = 25 * time.Second

// StatusCacheDir is where cached statuses live, under the workplan.
const StatusCacheDir = ".status-cache"

// CachedReviewer wraps a forge with a short-lived on-disk cache.
//
// The duplication it removes is between processes, which is why the cache is a file
// rather than a map. A workplan with eight tasks in review has eight blocked agents
// plus a one-minute orchestrator pass, and every one of them asks GitHub about the
// same pull requests. In-memory caching cannot help that: each is its own process.
//
// It is deliberately a cache and not a store. A miss, an unreadable entry, and an
// expired entry are all just "ask the forge", so nothing here can make a caller
// wrong about review state — only slower.
type CachedReviewer struct {
	Inner FeedbackWaiter
	Dir   string           // where to keep the cache
	Now   func() time.Time // nil means time.Now
	TTL   time.Duration    // 0 means StatusTTL
}

// entry is one cached answer.
//
// Missing records a pull request the forge said does not exist. Without it that
// answer is the one case that is never cached, so a task whose branch has no pull
// request yet — the state a just-started task is in for as long as it takes to
// build — would be the most expensive one to poll.
type entry struct {
	At      time.Time     `json:"at"`
	Missing bool          `json:"missing,omitempty"`
	Status  *forge.Status `json:"status,omitempty"`
}

func (c CachedReviewer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c CachedReviewer) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return StatusTTL
}

// StatusForBranch answers from the cache when the answer is fresh, and otherwise
// asks the forge and records what it said.
func (c CachedReviewer) StatusForBranch(dir, branch string) (*forge.Status, error) {
	path := c.path(dir, branch)

	if found, ok := c.read(path); ok {
		if found.Missing {
			return nil, nil
		}
		return found.Status, nil
	}

	status, err := c.Inner.StatusForBranch(dir, branch)
	if err != nil {
		// Errors are not cached. A `gh` failure is usually transient — a rate limit,
		// a dropped connection — and caching it would turn one bad call into a
		// quarter-minute of pretending the pull request is unreadable.
		return nil, err
	}
	c.write(path, entry{At: c.now(), Missing: status == nil, Status: status})
	return status, nil
}

// path is where one branch's cached status lives.
//
// The filename is a hash because a branch name contains slashes and a directory
// path is not a filename at all. Both go into the key: the same branch name in two
// repositories is two different pull requests.
func (c CachedReviewer) path(dir, branch string) string {
	sum := sha256.Sum256([]byte(dir + "\x00" + branch))
	return filepath.Join(c.Dir, StatusCacheDir, hex.EncodeToString(sum[:8])+".json")
}

// read returns a cached entry when there is a fresh one.
func (c CachedReviewer) read(path string) (entry, bool) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return entry{}, false
	}
	var found entry
	if err := json.Unmarshal(blob, &found); err != nil {
		return entry{}, false
	}
	if c.now().Sub(found.At) >= c.ttl() {
		return entry{}, false
	}
	// A future timestamp means a clock changed under us. Treating it as a miss is
	// the safe reading: the alternative is honouring an entry that never expires.
	if found.At.After(c.now()) {
		return entry{}, false
	}
	return found, true
}

// write records an answer, and stays silent if it cannot.
//
// A cache that cannot be written is a performance problem, not a correctness one,
// and the caller already has the answer it needs. Failing here would turn a
// read-only directory into a broken wait.
func (c CachedReviewer) write(path string, e entry) {
	blob, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Written to a temporary name and renamed, so a reader never sees half a file.
	// Two processes racing here is fine: they are writing the same answer, and the
	// rename makes whichever lands last the whole of it.
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

var _ FeedbackWaiter = CachedReviewer{}
