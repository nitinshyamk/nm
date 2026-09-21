//go:build !windows

package shellint

const windowsHost = false

// parentShell has no work to do away from Windows: $SHELL names the shell the
// user is in, which Detect reads directly.
func parentShell() (string, bool) { return "", false }
