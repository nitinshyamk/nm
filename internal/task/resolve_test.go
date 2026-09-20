package task

import (
	"strings"
	"testing"
)

func tasksNamed(specs ...string) []Task {
	out := make([]Task, 0, len(specs))
	for _, spec := range specs {
		name, hash, _ := strings.Cut(spec, "/")
		out = append(out, Task{Name: name, Hash: hash})
	}
	return out
}

func TestResolveExactAndPrefix(t *testing.T) {
	tasks := tasksNamed("auth/9c31a0", "auth-retry/4b1e77", "docs/77ac10")

	cases := []struct {
		query, want string
	}{
		{"auth-9c31a0", "auth-9c31a0"},      // full label
		{"auth", "auth-9c31a0"},             // bare name, exact
		{"AUTH", "auth-9c31a0"},             // case does not matter
		{"docs", "docs-77ac10"},             // bare name
		{"do", "docs-77ac10"},               // unique prefix
		{"auth-r", "auth-retry-4b1e77"},     // prefix of a longer name
		{"auth-retry", "auth-retry-4b1e77"}, // exact name that is not a prefix winner
		{"auth-9", "auth-9c31a0"},           // prefix of the label
	}
	for _, tc := range cases {
		got, err := resolveAmong(tasks, tc.query, "/tasks")
		if err != nil {
			t.Errorf("resolve(%q): %v", tc.query, err)
			continue
		}
		if got.Label() != tc.want {
			t.Errorf("resolve(%q) = %s, want %s", tc.query, got.Label(), tc.want)
		}
	}
}

func TestResolveExactNameBeatsPrefix(t *testing.T) {
	// "auth" is both an exact name and a prefix of "auth-retry"; typing it in
	// full must not be called ambiguous.
	tasks := tasksNamed("auth/9c31a0", "auth-retry/4b1e77")
	got, err := resolveAmong(tasks, "auth", "/tasks")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Label() != "auth-9c31a0" {
		t.Errorf("resolve(auth) = %s, want the exact match", got.Label())
	}
}

func TestResolveAmbiguous(t *testing.T) {
	tasks := tasksNamed("auth/9c31a0", "auth-retry/4b1e77")
	_, err := resolveAmong(tasks, "au", "/tasks")
	if err == nil {
		t.Fatal("an ambiguous prefix resolved to something")
	}
	for _, want := range []string{"auth-9c31a0", "auth-retry-4b1e77"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list %q: %v", want, err)
		}
	}
}

func TestResolveDuplicateNamesAcrossHashes(t *testing.T) {
	// The same name with different repo sets produces different hashes.
	tasks := tasksNamed("auth/9c31a0", "auth/4b1e77")
	if _, err := resolveAmong(tasks, "auth", "/tasks"); err == nil {
		t.Error("two tasks share the name and it still resolved")
	}
	got, err := resolveAmong(tasks, "auth-4b1e77", "/tasks")
	if err != nil {
		t.Fatalf("the full label should disambiguate: %v", err)
	}
	if got.Hash != "4b1e77" {
		t.Errorf("resolved %s, want the labelled one", got.Label())
	}
}

func TestResolveMisses(t *testing.T) {
	tasks := tasksNamed("auth/9c31a0")
	if _, err := resolveAmong(tasks, "nope", "/tasks"); err == nil {
		t.Error("a query matching nothing resolved")
	} else if !strings.Contains(err.Error(), "/tasks") {
		t.Errorf("the error should name where nm looked: %v", err)
	}
	if _, err := resolveAmong(tasks, "  ", "/tasks"); err == nil {
		t.Error("an empty query resolved")
	}
	if _, err := resolveAmong(nil, "auth", "/tasks"); err == nil {
		t.Error("resolving against no tasks succeeded")
	}
}
