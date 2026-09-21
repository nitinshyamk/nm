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
	if want := shellPWD(t, bash, destination); got != want {
		t.Errorf("shell ended in %q, want %q", got, want)
	}
}

// shellPWD asks the shell what it calls dir. A path cannot be compared across
// the two spellings of it: Go builds C:\Users\...\Temp\x, while bash reports
// the same directory as /tmp/x under Git Bash. Letting one shell spell both
// sides keeps the comparison meaningful on every platform, and needs no
// runtime.GOOS branch.
func shellPWD(t *testing.T, bash, dir string) string {
	t.Helper()
	// Forward slashes: a Windows path inside a double-quoted bash string would
	// otherwise carry backslashes that bash may read as escapes.
	out, err := exec.Command(bash, "-c", `cd "`+filepath.ToSlash(dir)+`" && pwd`).Output()
	if err != nil {
		t.Fatalf("asking the shell how it spells %q: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestCompletionRequestsNeverChangeDirectory is the guard for the sharpest
// edge in the shell integration: completion evaluates "nm __complete ..." in
// the user's live shell, so if the wrapper took its normal cd path there, a
// tab press could move them.
func TestCompletionRequestsNeverChangeDirectory(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script, err := InitScript("bash")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hostile stand-in: it writes a cd request no matter what it is asked,
	// so only the wrapper's guard can keep the shell in place.
	fake := filepath.Join(dir, "nm")
	fakeSrc := "#!/bin/sh\n" +
		"[ -n \"$NM_CD_FILE\" ] && printf '%s\\n' \"" + elsewhere + "\" > \"$NM_CD_FILE\"\n" +
		"echo \"ran: $*\"\n"
	if err := os.WriteFile(fake, []byte(fakeSrc), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, arg := range []string{"__complete", "__completeNoDesc", "completion"} {
		t.Run(arg, func(t *testing.T) {
			cmd := exec.Command(bash, "-c", script+"\nnm "+arg+" task select ''\npwd\n")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("running the wrapper: %v", err)
			}

			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			ended := strings.TrimSpace(lines[len(lines)-1])
			if want := shellPWD(t, bash, dir); ended != want {
				t.Errorf("a %q request moved the shell to %q; it must stay in %q", arg, ended, want)
			}
			if !strings.Contains(string(out), "ran: "+arg) {
				t.Errorf("the request never reached the binary:\n%s", out)
			}
		})
	}
}

func TestSelectionStillChangesDirectory(t *testing.T) {
	// The guard must not swallow ordinary commands.
	script, err := InitScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "NM_CD_FILE=\"$nm_cd_file\" command nm \"$@\"") {
		t.Error("the wrapper no longer runs nm with a cd file for normal commands")
	}
}

// TestPosixWrapperPushesOntoTheDirectoryStack covers the part of the contract
// that is not just "you end up there": every jump nm makes is pushed, so popd
// takes you back to where you were before it moved you.
func TestPosixWrapperPushesOntoTheDirectoryStack(t *testing.T) {
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
	fake := filepath.Join(dir, "nm")
	fakeSrc := "#!/bin/sh\nprintf '%s\\n' \"" + destination + "\" > \"$NM_CD_FILE\"\n"
	if err := os.WriteFile(fake, []byte(fakeSrc), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(body string) []string {
		t.Helper()
		cmd := exec.Command(bash, "-c", script+"\n"+body)
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("running the wrapper: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	lines := run("nm task select x >/dev/null\npwd\npopd >/dev/null\npwd\n")
	if len(lines) != 2 {
		t.Fatalf("expected two paths, got %v", lines)
	}
	if want := shellPWD(t, bash, destination); strings.TrimSpace(lines[0]) != want {
		t.Errorf("the jump landed in %q, want %q", lines[0], want)
	}
	if want := shellPWD(t, bash, dir); strings.TrimSpace(lines[1]) != want {
		t.Errorf("popd left the shell in %q, want the directory it started in, %q", lines[1], want)
	}

	// Jumping to where you already are is not worth a stack entry, or popd
	// would take two presses to undo one move.
	depth := run("nm task select x >/dev/null\nnm task select x >/dev/null\ndirs -p | wc -l\n")
	if got := strings.TrimSpace(depth[len(depth)-1]); got != "2" {
		t.Errorf("the stack is %s deep after two jumps to the same place, want 2", got)
	}
}
