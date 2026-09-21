package shellint

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	// The behavioural guard for this is TestEveryWrapperForwardsFlagsAndHelp,
	// which skips where nu is not installed -- so assert it cheaply here too.
	// Without --wrapped, `nm task new repo -n name -p` is a nushell parse error.
	if !strings.Contains(nu, "--wrapped") {
		t.Error("the nushell script must use --wrapped or nushell rejects nm's own flags")
	}
	posix, _ := InitScript("bash")
	if !strings.Contains(posix, "command nm") {
		t.Error("the posix script must call `command nm` or it will recurse into itself")
	}
}

// TestBashZshScriptRunsUnderItsShells replaces a `sh -n` check that only ever
// parsed the script. Parsing is not the question: pushd is an unknown *command*
// to dash, not a syntax error, so the old check passed under a /bin/sh that
// could never run what it had just approved. Running it, and asking for the
// builtins it depends on, is what actually holds.
func TestBashZshScriptRunsUnderItsShells(t *testing.T) {
	script, err := InitScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		// Sourcing defines the function; the type checks prove this shell has
		// the builtins the wrapper reaches for.
		probe := script + "\ntype nm >/dev/null && type pushd >/dev/null && type popd >/dev/null\n"
		cmd := exec.Command(path, "-c", probe)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s cannot run the integration script: %v\n%s", shell, err, out)
		}
	}
}

// The script must not be offered to a shell that cannot run it. dash is the
// /bin/sh on Debian and Ubuntu, and it has no pushd.
func TestBashZshScriptIsNotOfferedToDash(t *testing.T) {
	if _, err := InitScript("dash"); err == nil {
		t.Error("InitScript accepted dash, which has no pushd")
	}
	if _, err := InitScript("sh"); err == nil {
		t.Error("InitScript accepted sh, which may be dash")
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

// nuAvailable skips when nushell is not installed. nu is not a build dependency,
// so the suite must pass without it -- but where it exists these tests run the
// real shell rather than asserting on the script's text.
func nuAvailable(t *testing.T) string {
	t.Helper()
	nu, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("nu not available")
	}
	return nu
}

// fakeNM writes a stand-in nm into dir, which callers prepend to PATH. nushell
// resolves externals through PATH like any shell, so the stand-in has to be
// executable the way the platform means it: a .bat on Windows, a shebang script
// elsewhere.
func fakeNM(t *testing.T, dir, body string) {
	t.Helper()
	name, content := "nm", "#!/bin/sh\n"+body
	if runtime.GOOS == "windows" {
		name, content = "nm.bat", "@echo off\r\n"+body
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// cdRequestBody is a stand-in nm that does what the real one does: write the
// chosen directory to the file nm was handed.
func cdRequestBody(target string) string {
	if runtime.GOOS == "windows" {
		return "echo " + target + "> \"%NM_CD_FILE%\"\r\n"
	}
	return "printf '%s\n' \"" + target + "\" > \"$NM_CD_FILE\"\n"
}

// runNu runs a nushell script and returns its stdout lines. Paths are embedded
// in single-quoted nu strings by the callers, which are literal in nushell, so a
// Windows path's backslashes need no escaping.
func runNu(t *testing.T, nu, body string) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "probe.nu")
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nu, script)
	// std/dirs keeps its stack in DIRS_LIST, which is inherited like any other
	// environment variable. Running the suite from inside a nushell that has
	// pushed directories would otherwise start the probe with a non-empty stack,
	// and a test that asserts on depth would pass or fail by ambient luck.
	cmd.Env = envWithout(os.Environ(), "DIRS_LIST", "DIRS_POSITION")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the nu script: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// envWithout returns env with the named variables removed, matching the name
// case-insensitively because Windows environment variables are.
func envWithout(env []string, names ...string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, name := range names {
			if strings.EqualFold(key, name) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

// nuScriptAt writes the integration script somewhere a nu probe can source it.
func nuScriptAt(t *testing.T) string {
	t.Helper()
	script, err := InitScript("nu")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nm.nu")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNuWrapperPushesOntoTheDirectoryStack is the nushell half of the contract
// the bash script has: the jump happens AND it is recorded, so "dirs drop" --
// nushell's popd -- takes you back. The script used to test `which dirs` and
// fall back to a bare cd, and on a default nushell that fallback always won: the
// jump worked while nothing was ever pushed.
func TestNuWrapperPushesOntoTheDirectoryStack(t *testing.T) {
	nu := nuAvailable(t)
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNM(t, dir, cdRequestBody(destination))

	lines := runNu(t, nu, "$env.PATH = (['"+dir+"'] ++ $env.PATH)\n"+
		"source '"+nuScriptAt(t)+"'\n"+
		"cd '"+dir+"'\n"+
		// The baseline is measured rather than assumed: depth is only meaningful
		// as a delta, because std/dirs may start with entries already on it.
		"print (dirs | length)\n"+
		"nm task select x\n"+
		"print $env.PWD\n"+
		"print (dirs | length)\n"+
		// Jumping to where you already are is not worth a stack entry.
		"nm task select x\n"+
		"print (dirs | length)\n"+
		"dirs drop\n"+
		"print $env.PWD\n")

	if len(lines) != 5 {
		t.Fatalf("expected five lines, got %v", lines)
	}
	baseline, after, again := lines[0], lines[2], lines[3]

	if !samePathNu(t, lines[1], destination) {
		t.Errorf("the jump landed in %q, want %q", lines[1], destination)
	}
	if want := depthPlusOne(t, baseline); after != want {
		t.Errorf("stack depth went %s -> %s, want %s: the jump was not pushed", baseline, after, want)
	}
	if again != after {
		t.Errorf("a second jump to the same place took depth %s -> %s, want no change", after, again)
	}
	if !samePathNu(t, lines[4], dir) {
		t.Errorf("dirs drop left the shell in %q, want %q", lines[4], dir)
	}
}

// depthPlusOne is what the stack depth should read after exactly one push.
func depthPlusOne(t *testing.T, depth string) string {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(depth))
	if err != nil {
		t.Fatalf("stack depth %q is not a number: %v", depth, err)
	}
	return strconv.Itoa(n + 1)
}

// TestNuCompleterParsesTheCobraProtocol covers what cobra does not generate: the
// completion nushell gets is nm's own parse of the __complete protocol, so the
// parse is the thing worth testing.
func TestNuCompleterParsesTheCobraProtocol(t *testing.T) {
	nu := nuAvailable(t)
	dir := t.TempDir()
	// Real tab characters, which is what cobra emits between value and
	// description, and a ":<directive>" line that is not a candidate.
	if runtime.GOOS == "windows" {
		fakeNM(t, dir, "echo list\tList every worktree\r\necho new\tCreate a worktree\r\necho :4\r\n")
	} else {
		fakeNM(t, dir, "printf 'list\tList every worktree\nnew\tCreate a worktree\n:4\n'\n")
	}

	lines := runNu(t, nu, "$env.PATH = (['"+dir+"'] ++ $env.PATH)\n"+
		"source '"+nuScriptAt(t)+"'\n"+
		"let got = (nu-complete nm 'nm worktree ')\n"+
		"print ($got | length)\n"+
		"print ($got | get value | str join ',')\n"+
		"print ($got | get description | first)\n")

	if len(lines) != 3 {
		t.Fatalf("expected three lines, got %v", lines)
	}
	if lines[0] != "2" {
		t.Errorf("got %s candidates, want 2: the :4 directive line is not a candidate", lines[0])
	}
	if lines[1] != "list,new" {
		t.Errorf("candidates = %q, want list,new", lines[1])
	}
	if lines[2] != "List every worktree" {
		t.Errorf("first description = %q, want the text after the tab", lines[2])
	}
}

// samePathNu compares a path nushell printed against one Go built, for the
// reason shellPWD exists: t.TempDir() hands back the 8.3 short form on Windows
// while nushell reports the long one.
func samePathNu(t *testing.T, got, want string) bool {
	t.Helper()
	resolve := func(path string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(strings.TrimSpace(path))
		if err != nil {
			t.Fatalf("resolving %q: %v", path, err)
		}
		return resolved
	}
	return resolve(got) == resolve(want)
}

// powershellAvailable skips where Windows PowerShell cannot run. 5.1 is the
// edition that ships with Windows and the one nm writes a profile for.
func powershellAvailable(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell only runs on Windows")
	}
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("powershell not available")
	}
	return ps
}

// runPowerShell runs a script and returns its stdout lines. Callers embed paths
// in single-quoted PowerShell strings, which are literal, so the backslashes in
// a Windows path need no escaping.
func runPowerShell(t *testing.T, ps, body string) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "probe.ps1")
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-File", script).Output()
	if err != nil {
		t.Fatalf("running the PowerShell script: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func psScriptAt(t *testing.T) string {
	t.Helper()
	script, err := InitScript("powershell")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nm.ps1")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPowerShellWrapperPushesOntoTheDirectoryStack is the PowerShell half of the
// same contract bash and nushell hold: the jump happens, it is recorded, and
// Pop-Location takes you back.
func TestPowerShellWrapperPushesOntoTheDirectoryStack(t *testing.T) {
	ps := powershellAvailable(t)
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNM(t, dir, cdRequestBody(destination))

	lines := runPowerShell(t, ps, "$env:PATH = '"+dir+"' + ';' + $env:PATH\n"+
		". '"+psScriptAt(t)+"'\n"+
		"Set-Location '"+dir+"'\n"+
		"nm task select x\n"+
		"Write-Output (Get-Location).Path\n"+
		"Write-Output (Get-Location -Stack).Count\n"+
		// Jumping to where you already are is not worth a stack entry.
		"nm task select x\n"+
		"Write-Output (Get-Location -Stack).Count\n"+
		"Pop-Location\n"+
		"Write-Output (Get-Location).Path\n")

	if len(lines) != 4 {
		t.Fatalf("expected four lines, got %v", lines)
	}
	if !samePathNu(t, lines[0], destination) {
		t.Errorf("the jump landed in %q, want %q", lines[0], destination)
	}
	if strings.TrimSpace(lines[1]) != "1" {
		t.Errorf("stack depth after one jump = %s, want 1", lines[1])
	}
	// Resolve-Path and Convert-Path both keep an 8.3 short name while
	// Get-Location reports the long one, so this is the regression guard for
	// comparing the two as strings and pushing a duplicate every time.
	if strings.TrimSpace(lines[2]) != "1" {
		t.Errorf("stack depth after two jumps to the same place = %s, want 1", lines[2])
	}
	if !samePathNu(t, lines[3], dir) {
		t.Errorf("Pop-Location left the shell in %q, want %q", lines[3], dir)
	}
}

// The wrapper is a function named nm, so it has to reach the executable without
// calling itself -- PowerShell's equivalent of bash's `command nm`.
func TestPowerShellWrapperDoesNotRecurseIntoItself(t *testing.T) {
	script, err := InitScript("powershell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "-CommandType Application") {
		t.Error("the wrapper must resolve nm as an Application, or it calls itself forever")
	}
}

// A tab press must never move the shell, in every shell nm writes a wrapper for.
func TestPowerShellCompletionRequestsNeverChangeDirectory(t *testing.T) {
	ps := powershellAvailable(t)
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hostile stand-in: it writes a cd request whatever it is asked, so only
	// the wrapper's guard can keep the shell in place.
	fakeNM(t, dir, "if defined NM_CD_FILE echo "+elsewhere+"> \"%NM_CD_FILE%\"\r\necho ran: %*\r\n")

	for _, arg := range []string{"__complete", "__completeNoDesc", "completion"} {
		t.Run(arg, func(t *testing.T) {
			lines := runPowerShell(t, ps, "$env:PATH = '"+dir+"' + ';' + $env:PATH\n"+
				". '"+psScriptAt(t)+"'\n"+
				"Set-Location '"+dir+"'\n"+
				"nm "+arg+" task select ''\n"+
				"Write-Output (Get-Location).Path\n")

			ended := lines[len(lines)-1]
			if !samePathNu(t, ended, dir) {
				t.Errorf("a %q request moved the shell to %q; it must stay in %q", arg, ended, dir)
			}
			if !strings.Contains(strings.Join(lines, "\n"), "ran: "+arg) {
				t.Errorf("the request never reached the binary:\n%v", lines)
			}
		})
	}
}

// fakeNMForEveryShell writes a stand-in nm that reports exactly what it was
// handed, in both forms a shell might resolve on this platform.
//
// One form is not enough on Windows: Git Bash looks for `nm` or `nm.exe` and
// ignores `nm.bat`, while nushell and PowerShell need an extension in PATHEXT and
// so ignore the extensionless file. Writing both lets each shell find the one it
// can run -- and getting this wrong is not a visible failure, it just falls
// through PATH to the real nm.
func fakeNMForEveryShell(t *testing.T, dir string) {
	t.Helper()
	// For bash, including on Windows.
	if err := os.WriteFile(filepath.Join(dir, "nm"),
		[]byte("#!/bin/sh\necho \"GOT: $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	// For nushell and PowerShell.
	if err := os.WriteFile(filepath.Join(dir, "nm.bat"),
		[]byte("@echo off\r\necho GOT: %*\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestEveryWrapperForwardsFlagsAndHelp is the regression guard for a bug that
// shipped in the nushell wrapper.
//
// nushell's `def` validates what you typed against the signature, and a
// signature of nothing but a rest parameter declares no flags -- so
// `nm task new repo -n name -p` failed with "The nm command doesn't have flag
// -n" before the body ever ran, and `nm --help` printed nushell's generated help
// for the def instead of nm's. It is a parse error, so the wrapper could not even
// catch it. `def --wrapped` is the fix.
//
// The tests that missed this only ever passed positional arguments, so this one
// covers every shell rather than just the one that broke: the blind spot was the
// shape of the test, not the shell.
func TestEveryWrapperForwardsFlagsAndHelp(t *testing.T) {
	// A flag with a value, a bare switch, and the help flag that a wrapper is
	// most tempted to intercept.
	const invocation = "nm task new repo -n name -p"

	cases := []struct {
		shell string
		run   func(t *testing.T, dir, script, line string) string
	}{
		{"bash", func(t *testing.T, dir, script, line string) string {
			t.Helper()
			bash, err := exec.LookPath("bash")
			if err != nil {
				t.Skip("bash not available")
			}
			cmd := exec.Command(bash, "-c", "source '"+filepath.ToSlash(script)+"'\n"+line+"\n")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bash: %v\n%s", err, out)
			}
			return string(out)
		}},
		{"nu", func(t *testing.T, dir, script, line string) string {
			t.Helper()
			nu := nuAvailable(t)
			return strings.Join(runNu(t, nu, "$env.PATH = (['"+dir+"'] ++ $env.PATH)\n"+
				"source '"+script+"'\n"+line+"\n"), "\n")
		}},
		{"powershell", func(t *testing.T, dir, script, line string) string {
			t.Helper()
			ps := powershellAvailable(t)
			return strings.Join(runPowerShell(t, ps, "$env:PATH = '"+dir+"' + ';' + $env:PATH\n"+
				". '"+script+"'\n"+line+"\n"), "\n")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			dir := t.TempDir()
			fakeNMForEveryShell(t, dir)

			script, err := InitScript(tc.shell)
			if err != nil {
				t.Fatal(err)
			}
			ext := ".sh"
			switch tc.shell {
			case "nu":
				ext = ".nu"
			case "powershell":
				ext = ".ps1"
			}
			path := filepath.Join(dir, "nm"+ext)
			if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}

			got := tc.run(t, dir, path, invocation)
			// Every token has to arrive, in order, at the real binary.
			if !strings.Contains(got, "task new repo -n name -p") {
				t.Errorf("%s wrapper did not forward the flags verbatim:\n%s", tc.shell, got)
			}

			// --help must reach nm rather than printing the wrapper's own help.
			help := tc.run(t, dir, path, "nm --help")
			if !strings.Contains(help, "GOT: --help") {
				t.Errorf("%s wrapper swallowed --help instead of passing it to nm:\n%s", tc.shell, help)
			}
		})
	}
}
