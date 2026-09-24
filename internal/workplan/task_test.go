package workplan

import (
	"strings"
	"testing"
)

const validTask = `{
  "id": "01-modify-contract-to-include-gdt",
  "predecessors": ["00-prepare-codebase"],
  "repositories": ["nm"],
  "description": "Add the GDT field to the contract.",
  "acceptance-criteria": ["The field exists.", "The gate passes."]
}`

func TestParseTaskRoundTripsTheHyphenatedKey(t *testing.T) {
	got, err := ParseTask([]byte(validTask))
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if got.ID != "01-modify-contract-to-include-gdt" {
		t.Errorf("ID = %q", got.ID)
	}
	if len(got.AcceptanceCriteria) != 2 {
		t.Fatalf("AcceptanceCriteria = %v, want 2 entries", got.AcceptanceCriteria)
	}
	if got.AcceptanceCriteria[1] != "The gate passes." {
		t.Errorf("AcceptanceCriteria[1] = %q", got.AcceptanceCriteria[1])
	}

	// Encode then parse again: the on-disk spelling has to be one this reader
	// accepts, or nm writes files it cannot read.
	blob, err := got.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(blob), `"acceptance-criteria"`) {
		t.Errorf("Encode did not write the hyphenated key:\n%s", blob)
	}
	again, err := ParseTask(blob)
	if err != nil {
		t.Fatalf("re-parsing encoded output: %v", err)
	}
	if again.ID != got.ID || len(again.AcceptanceCriteria) != len(got.AcceptanceCriteria) {
		t.Errorf("round trip changed the task: %+v vs %+v", again, got)
	}
}

// An empty predecessors list is written explicitly, because in a file read by
// humans an absent key reads as an oversight and [] reads as a decision.
func TestEncodeWritesEmptyListsExplicitly(t *testing.T) {
	blob, err := Task{
		ID:                 "01-alone",
		Description:        "Work with no predecessors.",
		AcceptanceCriteria: []string{"Done."},
	}.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, want := range []string{`"predecessors": []`, `"repositories": []`} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("Encode output is missing %s:\n%s", want, blob)
		}
	}
}

func TestParseTaskRejectsBadDefinitions(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"a bad id": {
			body: `{"id":"modify-contract","description":"d","acceptance-criteria":["c"]}`,
			want: "must be a number then hyphen-joined lowercase words",
		},
		"an uppercase id": {
			body: `{"id":"01-Modify","description":"d","acceptance-criteria":["c"]}`,
			want: "hyphen-joined lowercase",
		},
		"an id with a space": {
			body: `{"id":"01 modify","description":"d","acceptance-criteria":["c"]}`,
			want: "hyphen-joined lowercase",
		},
		"an id with no words": {
			body: `{"id":"01","description":"d","acceptance-criteria":["c"]}`,
			want: "hyphen-joined lowercase",
		},
		"no leading number": {
			body: `{"id":"a-01-thing","description":"d","acceptance-criteria":["c"]}`,
			want: "hyphen-joined lowercase",
		},
		"empty acceptance criteria": {
			body: `{"id":"01-a","description":"d","acceptance-criteria":[]}`,
			want: "no definition of done",
		},
		"missing acceptance criteria": {
			body: `{"id":"01-a","description":"d"}`,
			want: "no definition of done",
		},
		"a blank criterion": {
			body: `{"id":"01-a","description":"d","acceptance-criteria":["  "]}`,
			want: "acceptance-criteria[0] is empty",
		},
		"an empty description": {
			body: `{"id":"01-a","description":"   ","acceptance-criteria":["c"]}`,
			want: "description is empty",
		},
		"an unknown field": {
			body: `{"id":"01-a","description":"d","acceptance-criteria":["c"],"acceptance_criteria":["typo"]}`,
			want: "unknown field",
		},
		"a self predecessor": {
			body: `{"id":"01-a","predecessors":["01-a"],"description":"d","acceptance-criteria":["c"]}`,
			want: "lists itself",
		},
		"a malformed predecessor": {
			body: `{"id":"01-a","predecessors":["Nope"],"description":"d","acceptance-criteria":["c"]}`,
			want: "is not a task id",
		},
		"a duplicate predecessor": {
			body: `{"id":"02-b","predecessors":["01-a","01-a"],"description":"d","acceptance-criteria":["c"]}`,
			want: `predecessor "01-a" is listed twice`,
		},
		"a duplicate repository": {
			body: `{"id":"01-a","repositories":["nm","nm"],"description":"d","acceptance-criteria":["c"]}`,
			want: `repository "nm" is listed twice`,
		},
		"two JSON values": {
			body: `{"id":"01-a","description":"d","acceptance-criteria":["c"]} {"id":"02-b"}`,
			want: "more than one JSON value",
		},
		"not JSON at all": {
			body: `nope`,
			want: "reading the task definition",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTask([]byte(tc.body))
			if err == nil {
				t.Fatalf("ParseTask accepted %s", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A task with no repositories is valid: the schema allows clarification work
// that entails no code change.
func TestParseTaskAcceptsZeroRepositories(t *testing.T) {
	got, err := ParseTask([]byte(
		`{"id":"01-ask-a-question","predecessors":[],"repositories":[],"description":"Clarify the contract.","acceptance-criteria":["An answer is recorded."]}`))
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if len(got.Repositories) != 0 {
		t.Errorf("Repositories = %v, want none", got.Repositories)
	}
}

// Validate reports every problem at once, so correcting a hand-written task is
// one round trip rather than four.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	err := Task{ID: "Bad Id", Description: "  "}.Validate()
	if err == nil {
		t.Fatal("Validate accepted a task with three problems")
	}
	for _, want := range []string{"id", "description is empty", "no definition of done"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestIDsThatAreAccepted(t *testing.T) {
	for _, id := range []string{
		"01-a",
		"01-prepare-codebase",
		"0-x",
		"100-a-b-c-d",
		"01-add-gdt2",
		"02-v2-migration",
	} {
		if err := (Task{ID: id, Description: "d", AcceptanceCriteria: []string{"c"}}).Validate(); err != nil {
			t.Errorf("id %q was rejected: %v", id, err)
		}
	}
}
