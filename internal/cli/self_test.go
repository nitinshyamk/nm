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

	dest, err := installBinary(src, dir)
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

	dest, err := installBinary(src, dir)
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

	dest, err := installBinary(src, filepath.Join(root, "bin"))
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

	_, err := installBinary(missing, filepath.Join(root, "install"))
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
