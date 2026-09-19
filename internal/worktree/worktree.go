// Package worktree creates, lists, and deletes the standalone worktrees under
// the configured worktrees root.
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/nmhash"
)

// marker separates the repository from the worktree name in a directory name.
const marker = "-id-"

// DateLayout is the default name for a worktree created without one.
const DateLayout = "2006-01-02"

// Worktree is one directory under the worktrees root.
type Worktree struct {
	Repo    string // source repository name, e.g. "nm"
	Name    string // the name the user gave, or the date
	Hash    string // short deterministic hash
	Dir     string // absolute path on disk
	Branch  string // <name>-<hash>
	RepoDir string // absolute path of the source repository
	Status  gitx.Status
	Err     error // status collection failure, surfaced rather than hidden
}

// Label is how the worktree is identified in the UI: the branch name.
func (w Worktree) Label() string { return w.Branch }

// nameOK rejects names that git would refuse as a branch or that would make a
// confusing directory name.
var nameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// ValidateName reports whether name can be used for a worktree.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("name is empty")
	case !nameOK.MatchString(name):
		return fmt.Errorf("name %q must start with a letter or digit and contain only letters, digits, and . _ - /", name)
	case strings.Contains(name, ".."), strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, "/"):
		return fmt.Errorf("name %q is not a valid git branch name", name)
	case strings.Contains(name, marker):
		return fmt.Errorf("name %q may not contain %q, which separates the repository from the name", name, marker)
	}
	return nil
}

// DirName builds the on-disk directory name for a worktree.
func DirName(repo, name, hash string) string {
	return fmt.Sprintf("%s%s%s-%s", repo, marker, name, hash)
}

// ParseDirName splits a directory name back into its repository and the
// <name>-<hash> label. It reports false for directories nm did not create.
func ParseDirName(dir string) (repo, label string, ok bool) {
	idx := strings.Index(dir, marker)
	if idx <= 0 || idx+len(marker) >= len(dir) {
		return "", "", false
	}
	return dir[:idx], dir[idx+len(marker):], true
}

// Options controls worktree creation.
type Options struct {
	Repo    string
	Name    string // empty means use today's date
	Offline bool
	Now     func() time.Time
}

// Create adds a new worktree for a repository and returns it.
func Create(cfg config.Config, opts Options) (Worktree, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	repoDir := cfg.RepoPath(opts.Repo)
	if info, err := os.Stat(repoDir); err != nil || !info.IsDir() {
		return Worktree{}, fmt.Errorf("no repository %q in %s", opts.Repo, cfg.Projects())
	}
	if !gitx.IsRepo(repoDir) {
		return Worktree{}, fmt.Errorf("%s is not a git repository", repoDir)
	}

	named := opts.Name != ""
	name := opts.Name
	if !named {
		name = now().Format(DateLayout)
	}
	if err := ValidateName(name); err != nil {
		return Worktree{}, err
	}

	// A named worktree hashes only the repository and name, so asking for the
	// same thing twice always points at the same directory. An unnamed one
	// folds in the clock, because the date alone would collide with itself.
	hashParts := []string{opts.Repo, name}
	if !named {
		hashParts = append(hashParts, strconv.FormatInt(now().UnixNano(), 10))
	}

	hash := nmhash.Short(cfg.HashLength, hashParts...)
	dir := filepath.Join(cfg.Worktrees(), DirName(opts.Repo, name, hash))
	if _, err := os.Stat(dir); err == nil {
		if named {
			return Worktree{}, fmt.Errorf("worktree %s already exists at %s", DirName(opts.Repo, name, hash), dir)
		}
		// Unnamed and somehow taken: keep hashing until a free name appears.
		for i := 1; ; i++ {
			hash = nmhash.Short(cfg.HashLength, append(hashParts, strconv.Itoa(i))...)
			dir = filepath.Join(cfg.Worktrees(), DirName(opts.Repo, name, hash))
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				break
			}
			if i > 100 {
				return Worktree{}, fmt.Errorf("could not find a free worktree name under %s", cfg.Worktrees())
			}
		}
	}

	branch := name + "-" + hash
	if gitx.BranchExists(repoDir, branch) {
		return Worktree{}, fmt.Errorf("branch %s already exists in %s", branch, repoDir)
	}

	base, err := gitx.ResolveBase(repoDir, cfg.DefaultBaseBranch, opts.Offline)
	if err != nil {
		return Worktree{}, err
	}
	if err := os.MkdirAll(cfg.Worktrees(), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("creating %s: %w", cfg.Worktrees(), err)
	}
	if err := gitx.AddWorktree(repoDir, dir, branch, base.Commit); err != nil {
		return Worktree{}, err
	}

	return Worktree{
		Repo:    opts.Repo,
		Name:    name,
		Hash:    hash,
		Dir:     dir,
		Branch:  branch,
		RepoDir: repoDir,
	}, nil
}

// List returns every worktree under the worktrees root, newest activity first.
func List(cfg config.Config) ([]Worktree, error) {
	root := cfg.Worktrees()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var found []Worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !gitx.IsRepo(dir) {
			continue
		}
		repo, label, ok := ParseDirName(e.Name())
		if !ok {
			// Not created by nm, but it is a worktree living here, so show it
			// rather than pretending it does not exist.
			repo, label = "", e.Name()
		}
		w := Worktree{Repo: repo, Dir: dir}
		if main, err := gitx.MainWorktree(dir); err == nil {
			w.RepoDir = main
			if w.Repo == "" {
				w.Repo = filepath.Base(main)
			}
		}
		w.Branch = label
		if name, hash, ok := splitLabel(label); ok {
			w.Name, w.Hash = name, hash
		} else {
			w.Name = label
		}
		found = append(found, w)
	}

	// Status collection is one or more git invocations per worktree; run them
	// together so a directory full of worktrees still lists instantly.
	var wg sync.WaitGroup
	for i := range found {
		wg.Add(1)
		go func(w *Worktree) {
			defer wg.Done()
			st, err := gitx.GetStatus(w.Dir)
			w.Status, w.Err = st, err
			if st.Branch != "" {
				w.Branch = st.Branch
			}
		}(&found[i])
	}
	wg.Wait()

	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].Status.LastCommit, found[j].Status.LastCommit
		if a.Equal(b) {
			return found[i].Dir < found[j].Dir
		}
		return a.After(b)
	})
	return found, nil
}

func splitLabel(label string) (name, hash string, ok bool) {
	idx := strings.LastIndex(label, "-")
	if idx <= 0 || idx == len(label)-1 {
		return "", "", false
	}
	return label[:idx], label[idx+1:], true
}

// Delete removes a worktree and, when it is safe, its branch. It returns a
// note describing anything left behind.
func Delete(w Worktree, force bool) (note string, err error) {
	repoDir := w.RepoDir
	if repoDir == "" {
		if main, err := gitx.MainWorktree(w.Dir); err == nil {
			repoDir = main
		} else {
			return "", fmt.Errorf("cannot find the repository owning %s: %w", w.Dir, err)
		}
	}
	return Remove(repoDir, w.Dir, w.Branch, force)
}

// Remove deletes a worktree of repoDir and drops its branch when git agrees
// that is safe. Task directories reuse this for each of their worktrees.
func Remove(repoDir, dir, branch string, force bool) (note string, err error) {
	if err := gitx.RemoveWorktree(repoDir, dir, force); err != nil {
		return "", err
	}
	if err := gitx.PruneWorktrees(repoDir); err != nil {
		return "", err
	}
	if branch != "" {
		return deleteBranchIfSafe(repoDir, branch), nil
	}
	return "", nil
}

// deleteBranchIfSafe removes a branch, leaving it alone when git says it holds
// commits that are not merged anywhere. That refusal is a feature, not a
// failure, so it is reported as a note rather than an error.
func deleteBranchIfSafe(repoDir, branch string) (note string) {
	if err := gitx.DeleteBranch(repoDir, branch, false); err != nil {
		return fmt.Sprintf("kept branch %s in %s (it has commits that exist nowhere else)", branch, repoDir)
	}
	return ""
}
