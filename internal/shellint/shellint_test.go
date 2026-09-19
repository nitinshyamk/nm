package shellint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestCDWritesTheTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cd")
	t.Setenv(EnvCDFile, target)

	if !Enabled() {
		t.Fatal("Enabled() is false even though NM_CD_FILE is set")
	}
	delivered, err := RequestCD("/some/dir")
	if err != nil {
		t.Fatalf("RequestCD: %v", err)
	}
	if !delivered {
		t.Fatal("RequestCD reported the request was not delivered")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "/some/dir" {
		t.Errorf("wrote %q, want /some/dir", got)
	}
}

func TestRequestCDWithoutWrapper(t *testing.T) {
	t.Setenv(EnvCDFile, "")
	if err := os.Unsetenv(EnvCDFile); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("Enabled() is true with no NM_CD_FILE")
	}
	delivered, err := RequestCD("/some/dir")
	if err != nil {
		t.Fatalf("RequestCD should not fail without a wrapper: %v", err)
	}
	if delivered {
		t.Error("RequestCD claimed delivery with no wrapper loaded")
	}
}

func TestInitScripts(t *testing.T) {
	for _, shell := range Shells() {
		script, err := InitScript(shell)
		if err != nil {
			t.Fatalf("InitScript(%s): %v", shell, err)
		}
		if !strings.Contains(script, EnvCDFile) {
			t.Errorf("%s script never sets %s", shell, EnvCDFile)
		}
		if !strings.Contains(script, "cd ") {
			t.Errorf("%s script never runs cd", shell)
		}
	}

	if _, err := InitScript("fish"); err == nil {
		t.Error("InitScript accepted an unsupported shell")
	}

	nu, _ := InitScript("nu")
	if !strings.Contains(nu, "def --env") {
		t.Error("the nushell script must use def --env or the cd will not escape the function")
	}
	posix, _ := InitScript("bash")
	if !strings.Contains(posix, "command nm") {
		t.Error("the posix script must call `command nm` or it will recurse into itself")
	}
}

func TestPosixScriptIsValidShell(t *testing.T) {
	script, err := InitScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh", "sh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s rejected the integration script: %v\n%s", shell, err, out)
		}
	}
}

// TestPosixWrapperActuallyChangesDirectory runs the real wrapper against a
// stand-in binary, proving the protocol works end to end in a live shell.
func TestPosixWrapperActuallyChangesDirectory(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script, err := InitScript("bash")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake nm that does what the real one does: write the chosen directory.
	fake := filepath.Join(dir, "nm")
	fakeSrc := "#!/bin/sh\nprintf '%s\\n' \"" + destination + "\" > \"$NM_CD_FILE\"\n"
	if err := os.WriteFile(fake, []byte(fakeSrc), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bash, "-c", script+"\nnm worktree\npwd\n")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the wrapper: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if gotResolved, err := filepath.EvalSymlinks(got); err != nil || gotResolved != want {
		t.Errorf("shell ended in %q, want %q", got, want)
	}
}
