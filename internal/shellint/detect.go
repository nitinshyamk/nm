package shellint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvNuVersion is set by nushell. It is exported into every descendant process,
// so it reports that a nushell is somewhere above nm rather than that nm's own
// shell is nu -- which is why it does not outrank the process walk.
const EnvNuVersion = "NU_VERSION"

// shellSources are the signals Detect reads. They are injected so the priority
// order between them is testable without depending on the process tree of
// whatever machine happens to be running the suite.
type shellSources struct {
	parent    func() (string, bool)
	nuVersion string
	shellEnv  string
	windows   bool
}

// Detect reports which shell nm was run from.
//
// Guessing wrong is worse than admitting ignorance: `nm shell setup` writes to
// an rc file, so a wrong answer configures a shell the user is not in and the
// directory jumps then silently do nothing in the shell they are in. Every
// branch is evidence-based and the fallback is an error, not a default.
func Detect() (string, error) {
	return detect(shellSources{
		parent:    parentShell,
		nuVersion: os.Getenv(EnvNuVersion),
		shellEnv:  os.Getenv("SHELL"),
		windows:   windowsHost,
	})
}

func detect(src shellSources) (string, error) {
	// The nearest shell ancestor is the only signal that survives nesting. Run
	// bash inside nushell and both NU_VERSION and $SHELL still describe the
	// outer world; the process walk describes where nm actually is.
	if src.parent != nil {
		if name, ok := src.parent(); ok {
			return name, nil
		}
	}
	if src.nuVersion != "" {
		return "nu", nil
	}

	// $SHELL names the login shell, which on Unix is usually the shell you are
	// in. On Windows it is commonly set machine-wide to Git's bash.exe and keeps
	// that value inside PowerShell and nushell alike, so it would confidently
	// name the wrong shell.
	if !src.windows {
		if name := NormalizeShell(src.shellEnv); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot tell which shell you are in; pass --shell (one of: %s)",
		strings.Join(Shells(), ", "))
}

// NormalizeShell maps an executable name or path to a shell nm knows about,
// returning "" for anything else. It accepts a bare name ("bash"), a path
// ("/bin/zsh"), and a Windows executable ("powershell.exe") alike.
func NormalizeShell(nameOrPath string) string {
	name := strings.TrimSpace(nameOrPath)
	if name == "" {
		return ""
	}
	// filepath.Base does not split on / when running on Windows, and $SHELL
	// holds a forward-slash path even there, so settle the separator first.
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.ToLower(strings.TrimSuffix(name, ".exe"))

	switch name {
	case "bash", "sh":
		// A bare sh may be dash, which cannot run the wrapper. bash is both the
		// closest match and the one nm can write an rc file for.
		return "bash"
	case "zsh":
		return "zsh"
	case "nu":
		return "nu"
	case "powershell", "pwsh":
		return "powershell"
	}
	return ""
}
