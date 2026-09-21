package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBinary stands in for what `mise run build` produces.
func fakeBinary(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.Base(builtBinary()))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallBinaryCreatesTheDirectory(t *testing.T) {
	root := t.TempDir()
	src := fakeBinary(t, root, "first build")
	// Nested and absent, the way ~/.local/bin is on a fresh machine.
	dir := filepath.Join(root, "install", "bin")

	dest, _, err := installBinary(src, dir)
	if err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	if got := filepath.Dir(dest); got != dir {
		t.Errorf("installed into %q, want %q", got, dir)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "first build" {
		t.Errorf("installed contents = %q (err %v), want the source", got, err)
	}

	// The staging copy must not be left lying around next to the real one.
	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Errorf("the staged copy %s.new survived the install", dest)
	}
}

func TestInstallBinaryReplacesAnOlderInstall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	src := fakeBinary(t, root, "second build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(stale, []byte("first build"), 0o755); err != nil {
		t.Fatal(err)
	}

	dest, _, err := installBinary(src, dir)
	if err != nil {
		t.Fatalf("installBinary over an existing install: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "second build" {
		t.Errorf("installed contents = %q (err %v), want the newer build", got, err)
	}
}

func TestInstallBinaryIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows decides by extension, which builtBinary already covers.
		t.Skip("no Unix permission bits on Windows")
	}
	root := t.TempDir()
	src := fakeBinary(t, root, "build")

	dest, _, err := installBinary(src, filepath.Join(root, "bin"))
	if err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed with mode %v, which is not executable", info.Mode().Perm())
	}
}

// The old bash task failed obscurely when bin/ was empty; the message should
// say what to run instead.
func TestInstallBinaryWithoutABuildSaysWhatToRun(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "bin", filepath.Base(builtBinary()))

	_, _, err := installBinary(missing, filepath.Join(root, "install"))
	if err == nil {
		t.Fatal("installBinary succeeded with no binary to install")
	}
	if !strings.Contains(err.Error(), "mise run build") {
		t.Errorf("error %q does not mention how to produce the binary", err)
	}
}

func TestBuiltBinaryCarriesTheWindowsExtension(t *testing.T) {
	name := filepath.Base(builtBinary())
	want := "nm"
	if runtime.GOOS == "windows" {
		want = "nm.exe"
	}
	if name != want {
		t.Errorf("builtBinary = %q, want %q: an extensionless file is not executable on Windows", name, want)
	}
}

// The staged copy must never survive a successful install, however the swap went.
func TestInstallBinaryLeavesNoStagedCopy(t *testing.T) {
	root := t.TempDir()
	src := fakeBinary(t, root, "build")

	dest, _, err := installBinary(src, filepath.Join(root, "bin"))
	if err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Errorf("the staged copy %s.new survived the install", dest)
	}
}

// TestInstallBinaryWorksWhenARotationSlotIsOccupied covers the failure a fixed
// ".old" name caused: the first install rotates the running binary to nm.exe.old,
// and a second install can neither delete that file nor rename over it, so every
// install after the first failed with "Access is denied" until the running nm
// exited. The fix is to pick the next free name instead.
//
// A non-empty directory standing where the rotation would go is an occupied slot
// that can be neither removed nor renamed over -- the same constraint a held file
// imposes, without needing OS-level locks, so this runs on every platform.
func TestInstallBinaryWorksWhenARotationSlotIsOccupied(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, filepath.Base(builtBinary()))
	if err := os.WriteFile(dest, []byte("build 0"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Block the first two rotation names, so the install has to reach the third.
	blocked := []string{dest + ".old", dest + ".old.1"}
	for _, path := range blocked {
		if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	src := fakeBinary(t, root, "build 1")
	got, _, err := installBinary(src, dir)
	if err != nil {
		t.Fatalf("installBinary with rotation slots occupied: %v", err)
	}
	if contents, err := os.ReadFile(got); err != nil || string(contents) != "build 1" {
		t.Errorf("installed contents = %q (err %v), want build 1", contents, err)
	}
	// The occupied slots are not nm's to clear, and must survive untouched.
	for _, path := range blocked {
		if _, err := os.Stat(filepath.Join(path, "inside")); err != nil {
			t.Errorf("the install disturbed the occupied slot %s: %v", path, err)
		}
	}
}

// Too many occupied slots is a real dead end, and the error has to say what to do
// about it rather than reporting a bare rename failure.
func TestInstallBinaryGivesUpClearlyWhenEverySlotIsOccupied(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, filepath.Base(builtBinary()))
	if err := os.WriteFile(dest, []byte("build 0"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range rotationNames(dest) {
		if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	src := fakeBinary(t, root, "build 1")
	_, _, err := installBinary(src, dir)
	if err == nil {
		t.Fatal("installBinary succeeded with every rotation slot occupied")
	}
	if !strings.Contains(err.Error(), "running nm") {
		t.Errorf("error %q does not say what to do about it", err)
	}
	// And it must not have left a staged copy behind on the way out.
	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Errorf("the staged copy %s.new survived the failure", dest)
	}
}
