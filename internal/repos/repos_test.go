package repos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
)

// projectsRoot builds a projects directory holding the named repositories,
// plus the nm output directories that must never be offered as one.
func projectsRoot(t *testing.T, names ...string) config.Config {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults()
	cfg.ProjectsRoot = root
	cfg.WorktreesRoot = filepath.Join(root, "worktrees")
	cfg.TasksRoot = filepath.Join(root, "tasks")
	return cfg
}

func TestListFindsCheckoutsOnly(t *testing.T) {
	cfg := projectsRoot(t, "nm", "site")

	// Things that live alongside the repositories but are not repositories.
	root := cfg.Projects()
	for _, dir := range []string{"notes", ".cache"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "nm" || got[1].Name != "site" {
		t.Fatalf("List = %v, want nm and site in order", got)
	}
	if got[0].Dir != filepath.Join(root, "nm") {
		t.Errorf("Dir = %q, want the absolute path", got[0].Dir)
	}
}

func TestListSkipsTheNmRoots(t *testing.T) {
	cfg := projectsRoot(t, "nm")
	// nm's own output directories are git worktrees, so without the skip they
	// would look exactly like repositories.
	for _, dir := range []string{cfg.Worktrees(), cfg.Tasks()} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "nm" {
		t.Errorf("List = %v, want only nm", got)
	}
}

func TestListToleratesAMissingRoot(t *testing.T) {
	cfg := config.Defaults()
	cfg.ProjectsRoot = filepath.Join(t.TempDir(), "nowhere")

	got, err := List(cfg)
	if err != nil {
		t.Fatalf("a missing projects root should not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want nothing", got)
	}
}

func TestNamesFiltersByPrefix(t *testing.T) {
	cfg := projectsRoot(t, "nm", "nitinshyamk.github.io", "site")

	cases := map[string][]string{
		"":   {"nitinshyamk.github.io", "nm", "site"},
		"n":  {"nitinshyamk.github.io", "nm"},
		"NM": {"nm"},
		"s":  {"site"},
		"zz": {},
	}
	for prefix, want := range cases {
		got := Names(cfg, prefix)
		if len(got) != len(want) {
			t.Errorf("Names(%q) = %v, want %v", prefix, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Names(%q) = %v, want %v", prefix, got, want)
				break
			}
		}
	}
}
