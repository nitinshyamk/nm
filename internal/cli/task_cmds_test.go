package cli

import (
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
)

func entryWith(repos []task.RepoStatus, artifacts int, session *agent.Session) taskEntry {
	return taskEntry{
		View: task.View{
			Task:      task.Task{Name: "auth", Hash: "9c31a0", Dir: "/tasks/auth-9c31a0"},
			Repos:     repos,
			Artifacts: artifacts,
		},
		Session: session,
	}
}

func cleanRepo() task.RepoStatus {
	return task.RepoStatus{Repo: task.Repo{Name: "nm"}, Status: gitx.Status{}}
}

func TestNeedsConfirmation(t *testing.T) {
	cases := []struct {
		name  string
		entry taskEntry
		want  bool
	}{
		{
			name:  "nothing to lose",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 0, nil),
			want:  false,
		},
		{
			name: "uncommitted work",
			entry: entryWith([]task.RepoStatus{
				{Repo: task.Repo{Name: "nm"}, Status: gitx.Status{Unstaged: 1}},
			}, 0, nil),
			want: true,
		},
		{
			name: "untracked files",
			entry: entryWith([]task.RepoStatus{
				{Repo: task.Repo{Name: "nm"}, Status: gitx.Status{Untracked: 2}},
			}, 0, nil),
			want: true,
		},
		{
			name: "commits that never left",
			entry: entryWith([]task.RepoStatus{
				{Repo: task.Repo{Name: "nm"}, Status: gitx.Status{Unpushed: 1}},
			}, 0, nil),
			want: true,
		},
		{
			name:  "artifacts",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 3, nil),
			want:  true,
		},
		{
			name:  "an agent still working",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 0, &agent.Session{Status: "busy", State: "working"}),
			want:  true,
		},
		{
			name:  "an agent waiting on a reply",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 0, &agent.Session{Status: "waiting", State: "needs_reply"}),
			want:  true,
		},
		{
			name:  "an agent that finished",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 0, &agent.Session{Status: "idle", State: "done"}),
			want:  false,
		},
		{
			name:  "an agent that was stopped",
			entry: entryWith([]task.RepoStatus{cleanRepo()}, 0, &agent.Session{State: "stopped"}),
			want:  false,
		},
		{
			name: "clean repos but one dirty one",
			entry: entryWith([]task.RepoStatus{
				cleanRepo(),
				{Repo: task.Repo{Name: "site"}, Status: gitx.Status{Staged: 1}},
			}, 0, nil),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsConfirmation(tc.entry); got != tc.want {
				t.Errorf("needsConfirmation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRemovalHazardsNameTheAgent(t *testing.T) {
	e := entryWith([]task.RepoStatus{cleanRepo()}, 2, &agent.Session{Status: "waiting", State: "needs_reply"})
	hazards := strings.Join(removalHazards(e), " | ")

	if !strings.Contains(hazards, "artifacts/ holds 2 files") {
		t.Errorf("hazards do not mention the artifacts: %q", hazards)
	}
	if !strings.Contains(hazards, "needs reply") || !strings.Contains(hazards, "stopped") {
		t.Errorf("hazards do not explain the agent will be stopped: %q", hazards)
	}

	// A finished agent is not a reason to hesitate.
	done := entryWith([]task.RepoStatus{cleanRepo()}, 0, &agent.Session{State: "done"})
	if got := removalHazards(done); len(got) != 0 {
		t.Errorf("a finished agent produced hazards: %v", got)
	}
}
