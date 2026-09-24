// Package agent talks to the claude CLI's background sessions: launching one
// for a task, reporting which ones are blocked, and attaching to them.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ListTimeout bounds the status query the task view runs on every refresh.
const ListTimeout = 20 * time.Second

// Class buckets a session by what it needs from the user. The order is the
// order the task view presents them in: blocked work first, idle work last.
type Class int

// Session classes, most urgent first.
const (
	ClassNeedsInput Class = iota
	ClassDone
	ClassIdle
	ClassWorking
	ClassNone
)

// Label names a class for display.
func (c Class) Label() string {
	switch c {
	case ClassNeedsInput:
		return "Needs input"
	case ClassDone:
		return "Finished"
	case ClassIdle:
		return "Idle"
	case ClassWorking:
		return "Working"
	default:
		return "No agent"
	}
}

// Session is one entry of `claude agents --json`.
type Session struct {
	ID        string `json:"id"` // short id that `claude attach` takes
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`
	StartedAt int64  `json:"startedAt"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	State     string `json:"state"`
}

// AttachID is the identifier `claude attach` accepts.
func (s Session) AttachID() string {
	if s.ID != "" {
		return s.ID
	}
	return s.SessionID
}

// Started reports when the session was launched.
func (s Session) Started() time.Time {
	if s.StartedAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(s.StartedAt)
}

// Class buckets the session by whether it is waiting on the user.
//
// state is the precise signal — it distinguishes an agent that stopped to ask
// a question from one that is still thinking — so it decides when present,
// and status answers for sessions that do not report one.
func (s Session) Class() Class {
	switch strings.ToLower(s.State) {
	case "needs_approval", "needs_reply", "blocked":
		return ClassNeedsInput
	case "done", "failed", "stopped":
		return ClassDone
	case "working":
		return ClassWorking
	}

	switch strings.ToLower(s.Status) {
	case "needs_input":
		return ClassNeedsInput
	case "completed", "exited", "error":
		return ClassDone
	case "busy", "running":
		return ClassWorking
	case "waiting":
		return ClassNeedsInput
	}

	// An unrecognized value is reported as idle rather than hidden, so a new
	// claude release cannot make a task silently disappear from the view.
	return ClassIdle
}

// Describe is the short status text shown beside a task.
func (s Session) Describe() string {
	if s.State != "" {
		return strings.ReplaceAll(strings.ToLower(s.State), "_", " ")
	}
	if s.Status == "" {
		return strings.ToLower(s.Class().Label())
	}
	return strings.ReplaceAll(strings.ToLower(s.Status), "_", " ")
}

// Client runs the claude CLI.
type Client struct {
	Bin string // defaults to "claude"
}

func (c Client) bin() string {
	if c.Bin == "" {
		return "claude"
	}
	return c.Bin
}

// Available reports whether the claude CLI can be found.
func (c Client) Available() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// List returns every known session, including finished background ones.
func (c Client) List(ctx context.Context) ([]Session, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "agents", "--json", "--all")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude agents --json: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return ParseSessions([]byte(stdout.String()))
}

// ParseSessions decodes `claude agents --json` output. Unknown fields are
// ignored so a new claude release does not break the listing.
func ParseSessions(data []byte) ([]Session, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	var sessions []Session
	if err := json.Unmarshal([]byte(trimmed), &sessions); err != nil {
		return nil, fmt.Errorf("parsing the claude session list: %w", err)
	}
	return sessions, nil
}

// UnattendedMode is the permission mode a background agent is started in.
//
// A background agent has no terminal. Without this it stops on the first tool
// call the harness wants confirmed — `cd x && git status` is enough — and waits
// on stdin nobody is attached to. That state is unrecoverable by anything nm
// can do: the agent is no longer reading files, so no amount of writing to its
// escalations directory reaches it, and only a human running `claude attach` can
// clear it. An unattended agent that can be stopped by a dialog is an unattended
// agent that silently deadlocks.
//
// "auto" and not "bypassPermissions": auto still runs its classifier, which
// refuses genuinely destructive commands (verified against `rm -rf /`), where
// bypass has no check at all. `--dangerously-skip-permissions` is never used.
const UnattendedMode = "auto"

// LaunchOptions describes a background agent to start.
type LaunchOptions struct {
	Dir     string   // working directory for the agent
	Name    string   // display name, shown by `claude agents`
	Prompt  string   // the task prompt
	AddDirs []string // extra directories the agent may touch

	// Attended leaves the permission mode alone, for an agent a human is
	// watching. The default is unattended, because that is what every caller in
	// nm actually starts.
	Attended bool
}

// LaunchArgs builds the claude arguments for a background agent.
func LaunchArgs(opts LaunchOptions) []string {
	args := []string{"--bg"}
	if opts.Name != "" {
		args = append(args, "-n", opts.Name)
	}
	if !opts.Attended {
		args = append(args, "--permission-mode", UnattendedMode)
	}
	for _, dir := range opts.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	// --add-dir takes a variable number of values, so without this separator
	// it swallows the prompt and the agent starts with nothing to do.
	return append(args, "--", opts.Prompt)
}

// Launch starts a background agent and returns its short id.
func (c Client) Launch(opts LaunchOptions) (id string, output string, err error) {
	cmd := exec.Command(c.bin(), LaunchArgs(opts)...)
	cmd.Dir = opts.Dir
	cmd.Env = os.Environ()
	raw, err := cmd.CombinedOutput()
	output = strings.TrimSpace(string(raw))
	if err != nil {
		return "", output, fmt.Errorf("starting the background agent: %s: %w", output, err)
	}
	return ParseLaunchID(output), output, nil
}

var (
	// "  claude attach 93dbd9a3    open in this terminal"
	attachLine = regexp.MustCompile(`claude attach ([0-9a-fA-F]{6,})`)
	// "backgrounded · 93dbd9a3 · nm-probe"
	backgroundedLine = regexp.MustCompile(`backgrounded\s*[·:]?\s*([0-9a-fA-F]{6,})`)
)

// ParseLaunchID digs the session id out of `claude --bg` output. It returns
// "" when the format is not recognized, which callers recover from by looking
// the session up by working directory.
func ParseLaunchID(out string) string {
	if m := attachLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	if m := backgroundedLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// Stop ends a background session without discarding its conversation.
func (c Client) Stop(id string) error {
	out, err := exec.Command(c.bin(), "stop", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stopping agent %s: %s: %w", id, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// AttachCommand builds the command that opens a background session in this
// terminal. The caller runs it with the terminal attached.
func (c Client) AttachCommand(id string) *exec.Cmd {
	return exec.Command(c.bin(), "attach", id)
}

// SessionCommand builds the command that starts a fresh interactive session
// in dir, used for tasks that have no agent yet.
func (c Client) SessionCommand(dir string, addDirs []string) *exec.Cmd {
	args := make([]string, 0, 2*len(addDirs))
	for _, d := range addDirs {
		args = append(args, "--add-dir", d)
	}
	cmd := exec.Command(c.bin(), args...)
	cmd.Dir = dir
	return cmd
}
