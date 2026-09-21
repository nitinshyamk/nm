package cli

import (
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// documentsDir resolves the user's Documents folder.
//
// It is not reliably %USERPROFILE%\Documents: OneDrive redirection is common on
// Windows and moves it, and $PROFILE follows the redirection. Hardcoding the
// home-relative path would write a profile PowerShell never loads -- the same
// class of silent no-op as writing nushell's config to the XDG path. The shell
// folder registry value is what Explorer and PowerShell both read.
func documentsDir(home string) string {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`,
		registry.QUERY_VALUE)
	if err != nil {
		return filepath.Join(home, "Documents")
	}
	defer func() { _ = key.Close() }()

	value, valueType, err := key.GetStringValue("Personal")
	if err != nil || value == "" {
		return filepath.Join(home, "Documents")
	}
	// The stored value is usually REG_EXPAND_SZ holding %USERPROFILE%\Documents,
	// so it has to be expanded rather than used literally.
	if valueType == registry.EXPAND_SZ {
		expanded, err := registry.ExpandString(value)
		if err != nil {
			return filepath.Join(home, "Documents")
		}
		value = expanded
	}
	return value
}
