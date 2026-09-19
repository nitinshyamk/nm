package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/tui"
	"github.com/spf13/cobra"
)

// taskEntry is a task plus the agent working on it.
type taskEntry struct {
	View    task.View
	Session *agent.Session
}

// Class buckets the task by what it needs from the user.
func (e taskEntry) Class() agent.Class {
	if e.Session == nil {
		return agent.ClassNone
	}
	return e.Session.Class()
}

// runTaskList opens the task picker, or prints a plain listing when there is
// no terminal to draw on.
func runTaskList(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if !tui.Interactive() {
		return printTasks(out, cfg)
	}

	for {
		entries, err := surveyTasks(cfg)
		if err != nil {
			return err
		}

		rows := make([]tui.Row, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, taskRow(e))
		}

		outcome, err := tui.Run(tui.Config{
			Title: fmt.Sprintf("tasks in %s", cfg.Tasks()),
			Rows:  rows,
			Empty: "no tasks yet — create one with: nm task new <repo>... -n <name>",
			Actions: []tui.Action{
				{Key: "enter", Name: "select", Help: "cd here"},
				{Key: "o", Name: "open", Help: "open editor + agent"},
				{Key: "d", Name: "delete", Help: "delete", Confirm: true, Verb: "Delete"},
			},
		})
		if err != nil {
			return err
		}

		entry, ok := outcome.Row.Data.(taskEntry)
		if outcome.Action != "" && !ok {
			return fmt.Errorf("unexpected row payload %T", outcome.Row.Data)
		}

		switch outcome.Action {
		case "":
			return nil
		case "select":
			return enterDir(out, entry.View.Task.Dir)
		case "open":
			return openTask(out, cfg, entry)
		case "delete":
			if err := deleteTask(out, cfg, entry); err != nil {
				return err
			}
		}
	}
}

// surveyTasks collects task state and matches each task with its agent.
func surveyTasks(cfg config.Config) ([]taskEntry, error) {
	views, err := task.Survey(cfg)
	if err != nil {
		return nil, err
	}

	client := agent.Client{Bin: cfg.ClaudeCommand}
	var sessions []agent.Session
	if client.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), agent.ListTimeout)
		defer cancel()
		// A claude that cannot be queried must not hide the tasks themselves.
		sessions, _ = client.List(ctx)
	}

	entries := make([]taskEntry, 0, len(views))
	for _, v := range views {
		entries = append(entries, taskEntry{View: v, Session: matchSession(v.Task, sessions)})
	}

	// Blocked work first, then finished, idle, working, and tasks with no
	// agent at all; ties broken by most recently created.
	sort.SliceStable(entries, func(i, j int) bool {
		if a, b := entries[i].Class(), entries[j].Class(); a != b {
			return a < b
		}
		return entries[i].View.Task.CreatedAt.After(entries[j].View.Task.CreatedAt)
	})
	return entries, nil
}

// matchSession finds the agent working on a task, by recorded id first and by
// working directory otherwise.
func matchSession(t task.Task, sessions []agent.Session) *agent.Session {
	if t.Agent != nil && t.Agent.ID != "" {
		for i := range sessions {
			if sessions[i].ID == t.Agent.ID || sessions[i].SessionID == t.Agent.ID {
				return &sessions[i]
			}
		}
	}
	for i := range sessions {
		if sameOrUnder(sessions[i].CWD, t.Dir) {
			return &sessions[i]
		}
	}
	return nil
}

func sameOrUnder(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func taskRow(e taskEntry) tui.Row {
	v := e.View
	row := tui.Row{
		ID:      v.Task.Dir,
		Group:   e.Class().Label(),
		Title:   v.Task.Label(),
		Hazards: v.Hazards(),
		Data:    e,
	}

	repos := make([]string, 0, len(v.Repos))
	for _, r := range v.Repos {
		repos = append(repos, r.Repo.Name)
	}
	row.Subtitle = fmt.Sprintf("%s  ·  %s", v.Task.Dir, strings.Join(repos, ", "))

	if e.Session != nil {
		row.Badges = append(row.Badges, tui.Badge{
			Text: "agent: " + e.Session.Describe(),
			Kind: classBadge(e.Class()),
		})
	} else {
		row.Badges = append(row.Badges, tui.Badge{Text: "no agent"})
	}
	row.Badges = append(row.Badges, statusBadges(v.Totals())...)
	if v.Artifacts > 0 {
		row.Badges = append(row.Badges, tui.Badge{
			Text: fmt.Sprintf("artifacts:%d", v.Artifacts),
			Kind: tui.BadgeInfo,
		})
	}
	if e.Session != nil && e.Class() != agent.ClassDone {
		row.Note = "The agent working here will be stopped."
	}
	return row
}

func classBadge(c agent.Class) tui.BadgeKind {
	switch c {
	case agent.ClassNeedsInput:
		return tui.BadgeDanger
	case agent.ClassDone:
		return tui.BadgeOK
	case agent.ClassWorking:
		return tui.BadgeInfo
	default:
		return tui.BadgeNeutral
	}
}

// openTask opens the task in the editor and attaches to its agent, then leaves
// the shell in the task directory.
func openTask(out io.Writer, cfg config.Config, e taskEntry) error {
	dir := e.View.Task.Dir

	if cfg.EditorCommand != "" {
		editor := exec.Command(cfg.EditorCommand, dir)
		editor.Dir = dir
		// The editor is a separate application: start it and move on rather
		// than holding the terminal until it exits.
		if err := editor.Start(); err != nil {
			fmt.Fprintf(out, "could not start %s: %v\n", cfg.EditorCommand, err)
		} else {
			go func() { _ = editor.Wait() }()
		}
	}

	client := agent.Client{Bin: cfg.ClaudeCommand}
	if client.Available() {
		var session *exec.Cmd
		switch {
		case e.Session != nil:
			session = client.AttachCommand(e.Session.AttachID())
		default:
			session = client.SessionCommand(dir, e.View.Task.Dirs())
		}
		session.Dir = dir
		session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := session.Run(); err != nil {
			fmt.Fprintf(out, "the claude session ended with: %v\n", err)
		}
	}
	return enterDir(out, dir)
}

func deleteTask(out io.Writer, cfg config.Config, e taskEntry) error {
	// Removing the directory under a live agent would leave it writing into
	// nothing, so stop it first.
	if e.Session != nil && e.Class() != agent.ClassDone {
		client := agent.Client{Bin: cfg.ClaudeCommand}
		if err := client.Stop(e.Session.AttachID()); err != nil {
			fmt.Fprintf(out, "%v\n", err)
		} else {
			fmt.Fprintf(out, "stopped agent %s\n", e.Session.AttachID())
		}
	}

	notes, err := task.Delete(e.View.Task, e.View.Dirty())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", e.View.Task.Dir)
	for _, note := range notes {
		fmt.Fprintf(out, "%s\n", note)
	}
	return nil
}

func printTasks(out io.Writer, cfg config.Config) error {
	entries, err := surveyTasks(cfg)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(out, "no tasks in %s\n", cfg.Tasks())
		return nil
	}
	for _, e := range entries {
		state := "clean"
		if h := e.View.Hazards(); len(h) > 0 {
			state = strings.Join(h, "; ")
		}
		status := "no agent"
		if e.Session != nil {
			status = e.Session.Describe()
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", e.View.Task.Label(), e.View.Task.Dir, status, state)
	}
	return nil
}
