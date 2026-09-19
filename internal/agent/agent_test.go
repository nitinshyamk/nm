package agent

import (
	"testing"
	"time"
)

// realListing is verbatim output from `claude agents --json --all`, including
// an interactive session, a finished background one, and the id/state fields
// that only background sessions carry.
const realListing = `[
  {
    "pid": 6439,
    "cwd": "/home/nitin/projects/nm",
    "kind": "interactive",
    "startedAt": 1789852512799,
    "sessionId": "91b501be-b969-45e9-848e-37ac6d998e1a",
    "name": "nm-cli",
    "status": "busy"
  },
  {
    "pid": 48452,
    "id": "93dbd9a3",
    "cwd": "/home/nitin/projects/tasks/auth-9c31a0",
    "kind": "background",
    "startedAt": 1789857553134,
    "sessionId": "93dbd9a3-ad6c-4da3-b7c6-de1075baffee",
    "name": "nm-auth",
    "status": "idle",
    "state": "done"
  }
]`

func TestParseSessions(t *testing.T) {
	sessions, err := ParseSessions([]byte(realListing))
	if err != nil {
		t.Fatalf("ParseSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("parsed %d sessions, want 2", len(sessions))
	}

	interactive, background := sessions[0], sessions[1]
	if interactive.AttachID() != interactive.SessionID {
		t.Errorf("an interactive session should fall back to its sessionId, got %q", interactive.AttachID())
	}
	if background.AttachID() != "93dbd9a3" {
		t.Errorf("AttachID = %q, want the short id", background.AttachID())
	}
	if got, want := background.Started(), time.UnixMilli(1789857553134); !got.Equal(want) {
		t.Errorf("Started = %v, want %v", got, want)
	}
	if background.Class() != ClassDone {
		t.Errorf("an idle background session in state done is %v, want ClassDone", background.Class())
	}
	if background.Describe() != "done" {
		t.Errorf("Describe = %q, want done", background.Describe())
	}
	if interactive.Class() != ClassWorking {
		t.Errorf("a busy session is %v, want ClassWorking", interactive.Class())
	}
}

func TestParseSessionsEdgeCases(t *testing.T) {
	if s, err := ParseSessions([]byte("")); err != nil || s != nil {
		t.Errorf("empty output gave %v, %v; want no sessions and no error", s, err)
	}
	if s, err := ParseSessions([]byte("[]")); err != nil || len(s) != 0 {
		t.Errorf("an empty array gave %v, %v", s, err)
	}
	if _, err := ParseSessions([]byte("not json")); err == nil {
		t.Error("ParseSessions accepted output that is not JSON")
	}
	// Unknown fields must not break decoding: claude may add more over time.
	extra := `[{"pid":1,"cwd":"/x","status":"busy","somethingNew":{"a":1}}]`
	if _, err := ParseSessions([]byte(extra)); err != nil {
		t.Errorf("an unfamiliar field broke parsing: %v", err)
	}
}

func TestClassification(t *testing.T) {
	cases := []struct {
		status, state string
		want          Class
	}{
		// state is the precise signal, and it wins when both are present.
		// These are the values claude actually emits.
		{"idle", "needs_approval", ClassNeedsInput},
		{"idle", "needs_reply", ClassNeedsInput},
		{"idle", "blocked", ClassNeedsInput},
		{"idle", "working", ClassWorking},
		{"idle", "done", ClassDone},
		{"idle", "failed", ClassDone},
		// A session stopped by `nm task` deletion, or by hand.
		{"", "stopped", ClassDone},
		{"busy", "blocked", ClassNeedsInput},

		// Sessions without a state fall back to status.
		{"needs_input", "", ClassNeedsInput},
		{"waiting", "", ClassNeedsInput},
		{"busy", "", ClassWorking},
		{"running", "", ClassWorking},
		{"idle", "", ClassIdle},
		{"completed", "", ClassDone},
		{"exited", "", ClassDone},
		{"error", "", ClassDone},
		{"", "", ClassIdle},

		// Values from a future claude release still land somewhere visible.
		{"thinking_very_hard", "", ClassIdle},
		{"idle", "contemplating", ClassIdle},
	}
	for _, tc := range cases {
		got := Session{Status: tc.status, State: tc.state}.Class()
		if got != tc.want {
			t.Errorf("status=%q state=%q classified as %v, want %v", tc.status, tc.state, got, tc.want)
		}
	}
}

func TestClassOrderPutsBlockedWorkFirst(t *testing.T) {
	ordered := []Class{ClassNeedsInput, ClassDone, ClassIdle, ClassWorking, ClassNone}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1] >= ordered[i] {
			t.Errorf("%v does not sort before %v; the order must run from most to least in need of attention",
				ordered[i-1].Label(), ordered[i].Label())
		}
	}
}

func TestParseLaunchID(t *testing.T) {
	// Verbatim `claude --bg` output.
	real := "Starting background service…\n" +
		"backgrounded · 93dbd9a3 · nm-probe\n" +
		"  claude agents             list sessions\n" +
		"  claude attach 93dbd9a3    open in this terminal\n" +
		"  claude logs 93dbd9a3      show recent output\n" +
		"  claude stop 93dbd9a3      stop this session"
	if got := ParseLaunchID(real); got != "93dbd9a3" {
		t.Errorf("ParseLaunchID = %q, want 93dbd9a3", got)
	}

	cases := map[string]string{
		"backgrounded · abc123de · name": "abc123de",
		"backgrounded: abc123de":         "abc123de",
		"claude attach 0123456789abcdef": "0123456789abcdef",
		"something entirely different":   "",
		"":                               "",
	}
	for in, want := range cases {
		if got := ParseLaunchID(in); got != want {
			t.Errorf("ParseLaunchID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLaunchArgsSeparateThePrompt(t *testing.T) {
	args := LaunchArgs(LaunchOptions{
		Name:    "nm-auth",
		Prompt:  "refactor auth",
		AddDirs: []string{"/tasks/auth/nm", "/tasks/auth/site"},
	})

	// --add-dir accepts a variable number of values, so the prompt must be
	// separated from it or claude starts an agent with nothing to do.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
		}
	}
	if sep == -1 {
		t.Fatalf("no -- separator in %q", args)
	}
	if got := args[len(args)-1]; got != "refactor auth" {
		t.Errorf("last argument is %q, want the prompt", got)
	}
	if sep != len(args)-2 {
		t.Errorf("the separator must sit immediately before the prompt: %q", args)
	}
	for _, a := range args[:sep] {
		if a == "refactor auth" {
			t.Error("the prompt also appears before the separator")
		}
	}
	if args[0] != "--bg" {
		t.Errorf("args start with %q, want --bg", args[0])
	}
}

func TestDescribePrefersState(t *testing.T) {
	cases := []struct {
		session Session
		want    string
	}{
		{Session{Status: "idle", State: "needs_approval"}, "needs approval"},
		{Session{Status: "idle", State: "needs_reply"}, "needs reply"},
		{Session{Status: "idle", State: "done"}, "done"},
		{Session{Status: "busy"}, "busy"},
		{Session{Status: "needs_input"}, "needs input"},
		{Session{}, "idle"},
	}
	for _, tc := range cases {
		if got := tc.session.Describe(); got != tc.want {
			t.Errorf("Describe(%+v) = %q, want %q", tc.session, got, tc.want)
		}
	}
}
