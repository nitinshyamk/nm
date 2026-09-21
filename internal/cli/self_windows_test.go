package cli

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestInstallBinarySucceedsWhileTheDestinationIsOpen is the regression guard for
// `mise run install` failing with "Access is denied" whenever any nm was still
// running -- which, on a machine that uses nm, is most of the time.
//
// Windows will not let a file be replaced while it is open, so the install swaps
// by renaming rather than overwriting: the old binary is rotated aside and the new
// one takes its place.
//
// The handle here uses the sharing mode that permits rename, which is what makes
// the swap possible at all. os.Open would be the wrong instrument -- its sharing
// mode blocks rename too, so it is a stricter lock than any real process and the
// install would fail for a reason no user will ever hit.
//
// What this cannot reproduce: a *mapped* executable also blocks deletion of its
// image, through the section object rather than the sharing mode. A plain handle
// with FILE_SHARE_DELETE allows deletion, so rotations never survive here and the
// leftover path cannot be provoked. The portable
// TestInstallBinaryWorksWhenARotationSlotIsOccupied covers the logic that depends
// on it, and the real behaviour was checked by hand against a live nm process.
func TestInstallBinarySucceedsWhileTheDestinationIsOpen(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	src := fakeBinary(t, root, "second build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(dest, []byte("first build"), 0o755); err != nil {
		t.Fatal(err)
	}

	name, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("opening %s the way a running process holds its image: %v", dest, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	got, _, err := installBinary(src, dir)
	if err != nil {
		t.Fatalf("installBinary while the destination is in use: %v", err)
	}
	if contents, err := os.ReadFile(got); err != nil || string(contents) != "second build" {
		t.Errorf("installed contents = %q (err %v), want the newer build", contents, err)
	}
	if _, err := os.Stat(dest + ".new"); !os.IsNotExist(err) {
		t.Errorf("the staged copy %s.new survived", dest)
	}
}
