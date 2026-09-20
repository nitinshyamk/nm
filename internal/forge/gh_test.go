package forge

import (
	"errors"
	"testing"
)

func TestParsePRList(t *testing.T) {
	// Verbatim shape of `gh pr list --json number,url,state`.
	pr, err := ParsePRList(`[{"number":42,"url":"https://github.com/o/r/pull/42","state":"OPEN"}]`)
	if err != nil {
		t.Fatalf("ParsePRList: %v", err)
	}
	if pr == nil {
		t.Fatal("a listed pull request came back nil")
	}
	if pr.Number != 42 || pr.URL != "https://github.com/o/r/pull/42" || pr.State != "OPEN" {
		t.Errorf("parsed %+v", pr)
	}
}

func TestParsePRListWhenThereIsNone(t *testing.T) {
	// No pull request is the normal case on a fresh branch, not an error.
	for _, in := range []string{"[]", "", "   ", "\n"} {
		pr, err := ParsePRList(in)
		if err != nil {
			t.Errorf("ParsePRList(%q): %v", in, err)
		}
		if pr != nil {
			t.Errorf("ParsePRList(%q) invented %+v", in, pr)
		}
	}
}

func TestParsePRListRejectsGarbage(t *testing.T) {
	if _, err := ParsePRList("not json"); err == nil {
		t.Error("ParsePRList accepted output that is not JSON")
	}
}

func TestParsePRListTakesTheFirst(t *testing.T) {
	pr, err := ParsePRList(`[{"number":1,"url":"a","state":"OPEN"},{"number":2,"url":"b","state":"OPEN"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 1 {
		t.Errorf("took pull request %d, want the first", pr.Number)
	}
}

func TestCheckAuthReportsAMissingBinary(t *testing.T) {
	c := Client{Bin: "gh-that-does-not-exist"}
	if err := c.CheckAuth(); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("CheckAuth = %v, want ErrNotInstalled", err)
	}
}
