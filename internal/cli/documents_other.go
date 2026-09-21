//go:build !windows

package cli

import "path/filepath"

// documentsDir has no redirection to account for away from Windows. It exists so
// rcPath compiles everywhere; Windows PowerShell is the only shell that uses it,
// and it does not run here.
func documentsDir(home string) string { return filepath.Join(home, "Documents") }
