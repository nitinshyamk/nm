package workplan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nitinshyamk/nm/internal/config"
)

// cfgAt points the workplans root at a temporary directory, so no test can
// reach the user's real ~/projects/workplans.
func cfgAt(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.WorkplansRoot = filepath.Join(t.TempDir(), "workplans")
	return cfg
}

func fixedNow() func() time.Time {
	at := time.Date(2026, 5, 1, 8, 34, 2, 0, time.UTC)
	return func() time.Time { return at }
}

func TestDefineCreatesEveryDirectory(t *testing.T) {
	cfg := cfgAt(t)

	w, err := Define(cfg, Options{Name: "build-the-thing", Now: fixedNow()})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}

	// Every state directory, named for the state itself.
	for _, s := range States() {
		if info, err := os.Stat(w.StateDir(s)); err != nil || !info.IsDir() {
			t.Errorf("state directory %s is missing: %v", s, err)
		}
	}
	for _, dir := range []string{w.Escalations(), w.Artifacts()} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s is missing: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.Dir, MetaFile)); err != nil {
		t.Errorf("%s is missing: %v", MetaFile, err)
	}

	// Seven directories, no more: a stray one would mean a call site invented a
	// name rather than using a constant.
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if want := len(States()) + 2; dirs != want {
		t.Errorf("Define created %d directories, want %d", dirs, want)
	}
}

func TestDefineRoundTripsTheRecord(t *testing.T) {
	cfg := cfgAt(t)

	w, err := Define(cfg, Options{Name: "roundtrip", Now: fixedNow()})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}

	got, err := Load(w.Dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "roundtrip" {
		t.Errorf("Name = %q, want roundtrip", got.Name)
	}
	if !got.CreatedAt.Equal(fixedNow()()) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, fixedNow()())
	}
	if got.Version != SchemaVersion {
		t.Errorf("Version = %d, want %d", got.Version, SchemaVersion)
	}
	if got.Dir != w.Dir {
		t.Errorf("Dir = %q, want %q", got.Dir, w.Dir)
	}
}

// Re-running Define must repair a workplan rather than reset it: the creation
// time is when the work was scoped, not when a directory was last fixed.
func TestDefineIsIdempotentAndKeepsTheCreationTime(t *testing.T) {
	cfg := cfgAt(t)

	first, err := Define(cfg, Options{Name: "again", Now: fixedNow()})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	// Lose a directory the way an interrupted run or an older nm would.
	if err := os.RemoveAll(first.StateDir(Review)); err != nil {
		t.Fatal(err)
	}

	later := func() time.Time { return fixedNow()().Add(72 * time.Hour) }
	second, err := Define(cfg, Options{Name: "again", Now: later})
	if err != nil {
		t.Fatalf("second Define: %v", err)
	}
	if info, err := os.Stat(second.StateDir(Review)); err != nil || !info.IsDir() {
		t.Errorf("the second Define did not restore review/: %v", err)
	}
	if !second.CreatedAt.Equal(fixedNow()()) {
		t.Errorf("CreatedAt = %v, want the original %v", second.CreatedAt, fixedNow()())
	}
}

func TestDefineRejectsBadNames(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"leading dash":    "-plan",
		"leading dot":     ".plan",
		"a slash":         "team/plan",
		"a space":         "my plan",
		"dot dot":         "a..b",
		"a backslash":     `team\plan`,
		"a colon":         "c:plan",
		"a shell glob":    "plan*",
		"quotes":          `"plan"`,
		"a leading tilde": "~plan",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(value); err == nil {
				t.Errorf("ValidateName(%q) = nil, want an error", value)
			}
			cfg := cfgAt(t)
			if _, err := Define(cfg, Options{Name: value}); err == nil {
				t.Errorf("Define(%q) = nil, want an error", value)
			}
		})
	}
}

func TestValidateNameAcceptsReasonableNames(t *testing.T) {
	for _, name := range []string{"plan", "build-the-thing", "v1.2", "a_b", "01-first", "X"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

func TestDefineCopiesAnArtifactFile(t *testing.T) {
	cfg := cfgAt(t)
	src := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(src, []byte("the design\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := Define(cfg, Options{Name: "with-file", Artifacts: src})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(w.Artifacts(), "design.md"))
	if err != nil {
		t.Fatalf("the artifact was not copied: %v", err)
	}
	if string(got) != "the design\n" {
		t.Errorf("artifact content = %q, want %q", got, "the design\n")
	}
}

// A directory's *contents* land in artifacts/, not the directory itself, so
// -a ./design and -a ./design/plan.md are comparably shaped.
func TestDefineCopiesDirectoryContentsNotTheDirectory(t *testing.T) {
	cfg := cfgAt(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.md"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := Define(cfg, Options{Name: "with-dir", Artifacts: src})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}

	if _, err := os.Stat(filepath.Join(w.Artifacts(), "a.md")); err != nil {
		t.Errorf("a.md was not copied to the top of artifacts/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Artifacts(), "sub", "b.md")); err != nil {
		t.Errorf("sub/b.md was not copied: %v", err)
	}
	// The source directory's own name must not appear as a level.
	if _, err := os.Stat(filepath.Join(w.Artifacts(), filepath.Base(src))); err == nil {
		t.Errorf("artifacts/ has an extra level named after the source directory")
	}
}

// Artifacts are the context tasks were scoped against, so a second copy that
// would replace one fails rather than silently changing the meaning of work
// already in flight.
func TestCopyArtifactsRefusesToOverwrite(t *testing.T) {
	cfg := cfgAt(t)
	src := filepath.Join(t.TempDir(), "design.md")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := Define(cfg, Options{Name: "twice", Artifacts: src})
	if err != nil {
		t.Fatalf("Define: %v", err)
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.CopyArtifacts(src); err == nil {
		t.Fatal("a second CopyArtifacts overwrote an existing artifact")
	}
	got, err := os.ReadFile(filepath.Join(w.Artifacts(), "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Errorf("artifact content = %q, want the original v1", got)
	}
}

// A workplan whose artifacts could not be copied would have its tasks scoped
// against context that is not there, so nothing is left behind.
func TestDefineUnwindsWhenArtifactsAreMissing(t *testing.T) {
	cfg := cfgAt(t)

	_, err := Define(cfg, Options{Name: "doomed", Artifacts: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("Define with unreadable artifacts returned nil")
	}
	if _, statErr := os.Stat(DirFor(cfg, "doomed")); statErr == nil {
		t.Error("a failed Define left the workplan directory behind")
	}
}

// Repairing an existing workplan must not delete it when the artifacts argument
// is bad: the unwind only covers what this call created.
func TestDefineKeepsAnExistingWorkplanWhenArtifactsFail(t *testing.T) {
	cfg := cfgAt(t)
	if _, err := Define(cfg, Options{Name: "survivor"}); err != nil {
		t.Fatalf("Define: %v", err)
	}

	_, err := Define(cfg, Options{Name: "survivor", Artifacts: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("Define with unreadable artifacts returned nil")
	}
	if _, statErr := os.Stat(DirFor(cfg, "survivor")); statErr != nil {
		t.Error("a failed repair deleted a workplan that already existed")
	}
}

func TestListReturnsWorkplansNewestFirst(t *testing.T) {
	cfg := cfgAt(t)

	base := fixedNow()()
	for i, name := range []string{"oldest", "middle", "newest"} {
		at := base.Add(time.Duration(i) * time.Hour)
		if _, err := Define(cfg, Options{Name: name, Now: func() time.Time { return at }}); err != nil {
			t.Fatalf("Define(%s): %v", name, err)
		}
	}
	// A directory that is not a workplan must be ignored, not break the listing.
	if err := os.MkdirAll(filepath.Join(cfg.Workplans(), "not-a-workplan"), 0o755); err != nil {
		t.Fatal(err)
	}

	plans, err := List(cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, p := range plans {
		got = append(got, p.Name)
	}
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("List returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List returned %v, want %v", got, want)
		}
	}
}

func TestListIsEmptyWithoutAWorkplansRoot(t *testing.T) {
	cfg := cfgAt(t) // the root is a path under TempDir that was never created

	plans, err := List(cfg)
	if err != nil {
		t.Fatalf("List on a missing root: %v", err)
	}
	if len(plans) != 0 {
		t.Errorf("List returned %d workplans, want none", len(plans))
	}
}

func TestPathHelpersAgreeWithTheStateNames(t *testing.T) {
	w := Workplan{Name: "p", Dir: filepath.Join("root", "p")}

	if got, want := w.TaskFile(Planned, "01-foo"), filepath.Join("root", "p", "planned", "01-foo.json"); got != want {
		t.Errorf("TaskFile = %q, want %q", got, want)
	}
	if got, want := w.TaskEscalations("01-foo"), filepath.Join("root", "p", "escalations", "01-foo"); got != want {
		t.Errorf("TaskEscalations = %q, want %q", got, want)
	}
	if got, want := w.Lock(), filepath.Join("root", "p", ".lock"); got != want {
		t.Errorf("Lock = %q, want %q", got, want)
	}
	// in-progress is hyphenated on disk; the constant is the only spelling.
	if got, want := w.StateDir(InProgress), filepath.Join("root", "p", "in-progress"); got != want {
		t.Errorf("StateDir(InProgress) = %q, want %q", got, want)
	}
}
