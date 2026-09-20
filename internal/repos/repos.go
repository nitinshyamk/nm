// Package repos discovers the source repositories sitting under the projects
// root, so commands that take a repository name can offer the real ones.
package repos

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
)

// Repo is one source checkout directly under the projects root.
type Repo struct {
	Name string // the directory name, which is what nm commands take
	Dir  string // absolute path
}

// List returns every repository directly under the projects root, by name.
//
// The worktrees and tasks roots are skipped: they usually live under the
// projects root and hold nm's own output, which is never something to branch
// from.
func List(cfg config.Config) ([]Repo, error) {
	root := cfg.Projects()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	skip := map[string]struct{}{
		filepath.Clean(cfg.Worktrees()): {},
		filepath.Clean(cfg.Tasks()):     {},
	}

	var found []Repo
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(root, name)
		if _, ok := skip[filepath.Clean(dir)]; ok {
			continue
		}
		// Stat rather than trusting the dirent, so a symlinked checkout is
		// still a repository.
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if !IsCheckout(dir) {
			continue
		}
		found = append(found, Repo{Name: name, Dir: dir})
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, nil
}

// IsCheckout reports whether dir looks like a git working tree. It only looks
// at the filesystem: completion runs on every tab press, so it must not pay
// for a git invocation per candidate.
func IsCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Names lists the repositories whose name starts with prefix. A projects root
// that cannot be read yields no suggestions rather than an error, because the
// only caller is shell completion and it has to stay silent.
func Names(cfg config.Config, prefix string) []string {
	list, err := List(cfg)
	if err != nil {
		return nil
	}
	needle := strings.ToLower(prefix)
	out := make([]string, 0, len(list))
	for _, r := range list {
		if strings.HasPrefix(strings.ToLower(r.Name), needle) {
			out = append(out, r.Name)
		}
	}
	return out
}
