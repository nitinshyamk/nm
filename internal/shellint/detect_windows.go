package shellint

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsHost = true

// maxAncestors bounds the walk up the process tree. nm is often started a few
// processes below the shell -- `mise run setup-shell` runs `go run .`, so the
// chain is shell -> mise -> go -> nm -- and the walk has to pass those to reach
// the shell. The bound keeps a pid-reuse cycle from spinning forever.
const maxAncestors = 12

// parentShell names the nearest ancestor process that is a shell nm knows.
//
// On Windows this is the only trustworthy signal: PowerShell, Git bash, and
// nushell are indistinguishable from the environment, and $SHELL actively lies
// inside PowerShell when Git has set it machine-wide.
func parentShell() (string, bool) {
	parents, names, err := processTable()
	if err != nil {
		return "", false
	}

	pid := uint32(os.Getpid())
	for range maxAncestors {
		parent, ok := parents[pid]
		if !ok || parent == 0 || parent == pid {
			return "", false
		}
		if shell := NormalizeShell(names[parent]); shell != "" {
			return shell, true
		}
		pid = parent
	}
	return "", false
}

// processTable snapshots every running process, returning each pid's parent and
// executable name. One snapshot answers the whole walk, so the tree cannot
// shift underneath it partway up.
func processTable() (parents map[uint32]uint32, names map[uint32]string, err error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	parents = map[uint32]uint32{}
	names = map[uint32]string{}

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
		names[entry.ProcessID] = windows.UTF16ToString(entry.ExeFile[:])
	}
	// Running out of entries is how the walk ends, not a failure.
	if err != nil && !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, nil, err
	}
	return parents, names, nil
}
