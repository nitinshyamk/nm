package nmhash

import "testing"

func TestShortIsDeterministic(t *testing.T) {
	a := Short(6, "nm", "fix-tui")
	b := Short(6, "nm", "fix-tui")
	if a != b {
		t.Fatalf("same inputs produced %q and %q", a, b)
	}
}

func TestShortSeparatesParts(t *testing.T) {
	// Without a separator, ("ab","c") and ("a","bc") would collide.
	if Short(8, "ab", "c") == Short(8, "a", "bc") {
		t.Fatal("parts are not separated before hashing")
	}
}

func TestShortLength(t *testing.T) {
	for _, n := range []int{1, 6, 12, 52} {
		if got := len(Short(n, "nm")); got != n {
			t.Errorf("Short(%d) returned %d characters", n, got)
		}
	}
	if got := len(Short(0, "nm")); got != DefaultLength {
		t.Errorf("Short(0) returned %d characters, want the default %d", got, DefaultLength)
	}
	if got := len(Short(1000, "nm")); got == 0 || got > 64 {
		t.Errorf("Short(1000) returned %d characters, want a truncated hash", got)
	}
}

func TestShortIsLowercaseAlphanumeric(t *testing.T) {
	h := Short(20, "nm", "some/branch name")
	for _, r := range h {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit {
			t.Fatalf("hash %q contains %q, which is not path- or branch-safe", h, r)
		}
	}
}
