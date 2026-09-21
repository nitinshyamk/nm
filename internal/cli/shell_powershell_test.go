package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/shellint"
)

// TestPowerShellCompletionFiresForTheWrapperFunction is the guard for the thing
// that looked most likely to be broken about PowerShell support and turned out
// not to be: cobra registers its completer with
// `Register-ArgumentCompleter -CommandName 'nm'`, and nm is a *function* once the
// wrapper is loaded rather than the external command cobra had in mind. The
// completer fires anyway, and this pins that down -- if a cobra upgrade starts
// registering the completer as -Native, PowerShell completion would silently
// stop working and only this test would notice.
//
// TabExpansion2 is PowerShell's own completion entry point, so this exercises
// the real completion machinery rather than asserting on generated text.
func TestPowerShellCompletionFiresForTheWrapperFunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell only runs on Windows")
	}
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("powershell not available")
	}

	dir := t.TempDir()

	// A stand-in nm that answers the __complete protocol with real tab
	// characters, which is what cobra emits between value and description, and
	// the ":<directive>" line that is not a candidate.
	fake := "@echo off\r\necho list\tList every worktree\r\necho new\tCreate a worktree\r\necho :4\r\n"
	if err := os.WriteFile(filepath.Join(dir, "nm.bat"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	wrapper := filepath.Join(dir, "wrapper.ps1")
	script, err := shellint.InitScript("powershell")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	// Generate the completion the same way a user does, so the test cannot drift
	// from what `nm completion powershell` actually emits.
	completion := filepath.Join(dir, "completion.ps1")
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetArgs([]string{"completion", "powershell"})
	if err := root.Execute(); err != nil {
		t.Fatalf("generating the PowerShell completion: %v", err)
	}
	if err := os.WriteFile(completion, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	probe := filepath.Join(dir, "probe.ps1")
	body := "$env:PATH = '" + dir + "' + ';' + $env:PATH\n" +
		". '" + wrapper + "'\n" +
		". '" + completion + "'\n" +
		"Write-Output (Get-Command nm).CommandType\n" +
		"$line = 'nm worktree '\n" +
		"$r = TabExpansion2 -inputScript $line -cursorColumn $line.Length\n" +
		"Write-Output $r.CompletionMatches.Count\n" +
		"$r.CompletionMatches | ForEach-Object { Write-Output $_.CompletionText }\n"
	if err := os.WriteFile(probe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-File", probe).Output()
	if err != nil {
		t.Fatalf("running the completion probe: %v\n%s", err, out)
	}

	// nm's stderr ("Completion ended with directive: ...") can land in the same
	// stream, so match on the lines that matter rather than on an exact count.
	got := strings.Join(strings.Fields(string(out)), " ")
	if !strings.Contains(got, "Function") {
		t.Errorf("nm is not a Function in the probe, so this tested the wrong thing:\n%s", out)
	}
	for _, want := range []string{"list", "new"} {
		if !strings.Contains(got, want) {
			t.Errorf("completion did not offer %q; PowerShell completion is not firing:\n%s", want, out)
		}
	}
}

// The profile nm writes has to be the one PowerShell reads, or setup reports
// success and changes nothing -- the same silent no-op nu setup had.
func TestPowerShellProfileIsTheWindowsPowerShellOne(t *testing.T) {
	got, err := rcPath("powershell")
	if err != nil {
		t.Fatalf("rcPath: %v", err)
	}
	if filepath.Base(got) != "Microsoft.PowerShell_profile.ps1" {
		t.Errorf("rcPath = %q, want the PowerShell profile file", got)
	}
	// 5.1 reads Documents\WindowsPowerShell; 7 reads Documents\PowerShell.
	if !strings.Contains(got, "WindowsPowerShell") {
		t.Errorf("rcPath = %q, want the Windows PowerShell 5.1 profile directory", got)
	}
}

// Telling a PowerShell user to run `source` names a command their shell does not
// have, which is a poor last impression of a setup that just worked.
func TestReloadLineMatchesTheShell(t *testing.T) {
	if got := reloadLine("powershell", `C:\profile.ps1`); got != `. C:\profile.ps1` {
		t.Errorf("reloadLine(powershell) = %q, want a dot-source", got)
	}
	for _, shell := range []string{"bash", "zsh", "nu"} {
		if got := reloadLine(shell, "/home/x/.rc"); got != "source /home/x/.rc" {
			t.Errorf("reloadLine(%s) = %q, want source", shell, got)
		}
	}
}
