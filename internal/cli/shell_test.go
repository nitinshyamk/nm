package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteManagedBlockIsIdempotent(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".bashrc")
	original := "export PATH=\"$HOME/bin:$PATH\"\nalias ll='ls -la'\n"
	if err := os.WriteFile(rc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	replaced, err := writeManagedBlock(rc, "bash")
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if replaced {
		t.Error("the first write reported replacing a block that did not exist")
	}

	after := readFile(t, rc)
	if !strings.HasPrefix(after, original) {
		t.Error("writing the block disturbed the existing rc content")
	}

	for i := range 3 {
		replaced, err = writeManagedBlock(rc, "bash")
		if err != nil {
			t.Fatalf("rewrite %d: %v", i, err)
		}
		if !replaced {
			t.Errorf("rewrite %d did not report replacing the existing block", i)
		}
	}

	final := readFile(t, rc)
	if n := strings.Count(final, beginMarker); n != 1 {
		t.Errorf("rc file holds %d nm blocks after repeated setup, want 1:\n%s", n, final)
	}
	if !strings.HasPrefix(final, original) {
		t.Errorf("repeated setup disturbed the user's own lines:\n%s", final)
	}
	if strings.Count(final, "\n\n\n") > 0 {
		t.Errorf("repeated setup accumulated blank lines:\n%q", final)
	}
}

func TestWriteManagedBlockCreatesMissingFile(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "nested", ".zshrc")
	if _, err := writeManagedBlock(rc, "zsh"); err != nil {
		t.Fatalf("writeManagedBlock: %v", err)
	}
	if got := readFile(t, rc); !strings.Contains(got, "nm shell init zsh") {
		t.Errorf("rc file does not load the zsh integration:\n%s", got)
	}
}

func TestStripManagedBlockLeavesForeignContent(t *testing.T) {
	content := "before\n" + beginMarker + "\nold line\n" + endMarker + "\nafter\n"
	got, found := stripManagedBlock(content)
	if !found {
		t.Fatal("stripManagedBlock did not find the block")
	}
	if strings.Contains(got, "old line") {
		t.Error("the old block survived")
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding content was lost: %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
