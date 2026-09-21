package shellint

import (
	"strings"
	"testing"
)

func TestNormalizeShellAcceptsEveryFormAShellIsNamedIn(t *testing.T) {
	for input, want := range map[string]string{
		"bash":                                  "bash",
		"/bin/bash":                             "bash",
		"C:\\Program Files\\Git\\bin\\bash.exe": "bash",
		"/usr/bin/zsh":                          "zsh",
		"ZSH":                                   "zsh",
		"nu":                                    "nu",
		"nu.exe":                                "nu",
		"powershell.exe":                        "powershell",
		"pwsh":                                  "powershell",
		// $SHELL holds a forward-slash path even on Windows.
		"C:/Program Files/Git/bin/bash.exe": "bash",
		// sh may be dash, which cannot run the wrapper, but bash is the closest
		// shell nm can actually write an rc file for.
		"sh": "bash",
	} {
		if got := NormalizeShell(input); got != want {
			t.Errorf("NormalizeShell(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "   ", "fish", "csh", "cmd.exe", "go.exe", "mise.exe"} {
		if got := NormalizeShell(input); got != "" {
			t.Errorf("NormalizeShell(%q) = %q, want \"\": nm cannot write for it", input, got)
		}
	}
}

func found(name string) func() (string, bool) {
	return func() (string, bool) { return name, true }
}

func notFound() (string, bool) { return "", false }

// The process walk outranks both environment signals, because it is the only
// one that survives nesting. Running bash inside nushell leaves NU_VERSION set
// and $SHELL pointing at the login shell, so trusting either would name the
// shell nm is *not* in -- which is exactly the bug that wrote an nm block to a
// nushell config while bash was the shell in front of the user.
func TestDetectPrefersTheNearestShellAncestor(t *testing.T) {
	got, err := detect(shellSources{
		parent:    found("bash"),
		nuVersion: "0.109.1",
		shellEnv:  "/usr/bin/zsh",
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got != "bash" {
		t.Errorf("detect = %q, want bash: the process walk outranks NU_VERSION and $SHELL", got)
	}
}

// With no shell ancestor to be found, NU_VERSION is better evidence than
// $SHELL: it at least proves a nushell is involved.
func TestDetectFallsBackToNuVersionBeforeShellEnv(t *testing.T) {
	got, err := detect(shellSources{
		parent:    notFound,
		nuVersion: "0.109.1",
		shellEnv:  "/bin/bash",
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got != "nu" {
		t.Errorf("detect = %q, want nu", got)
	}
}

func TestDetectReadsShellEnvOnlyAwayFromWindows(t *testing.T) {
	got, err := detect(shellSources{parent: notFound, shellEnv: "/usr/bin/zsh"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got != "zsh" {
		t.Errorf("detect = %q, want zsh", got)
	}

	// On Windows $SHELL is commonly Git's bash.exe no matter which shell is
	// running, so it is not evidence of anything.
	if _, err := detect(shellSources{
		parent:   notFound,
		shellEnv: "C:/Program Files/Git/bin/bash.exe",
		windows:  true,
	}); err == nil {
		t.Error("detect trusted $SHELL on Windows, where it names the wrong shell")
	}
}

// Guessing is the failure mode that matters: `nm shell setup` writes to an rc
// file, so naming the wrong shell configures one the user is not in and the
// jumps then silently do nothing where they are.
func TestDetectRefusesToGuess(t *testing.T) {
	_, err := detect(shellSources{parent: notFound, shellEnv: "/usr/bin/fish"})
	if err == nil {
		t.Fatal("detect guessed a shell with no evidence, want an error")
	}
	if !strings.Contains(err.Error(), "--shell") {
		t.Errorf("error %q does not tell the user how to say which shell they are in", err)
	}
	for _, shell := range Shells() {
		if !strings.Contains(err.Error(), shell) {
			t.Errorf("error %q does not list %q as a choice", err, shell)
		}
	}
}
