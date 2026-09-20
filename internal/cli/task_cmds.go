package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/tui"
	"github.com/spf13/cobra"
)

// completeTaskNames feeds shell tab-completion with the tasks that exist right
// now. Completion runs on every tab press, so it never reports errors.
func completeTaskNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	matches := task.Labels(cfg, toComplete)
	out := make([]string, 0, len(matches))
	for _, t := range matches {
		repos := make([]string, 0, len(t.Repos))
		for _, r := range t.Repos {
			repos = append(repos, r.Name)
		}
		out = append(out, fmt.Sprintf("%s\t%s", t.Label(), strings.Join(repos, ", ")))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func newTaskSelectCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "select [name]",
		GroupID:           groupTaskWork,
		Short:             "Change directory into a task",
		Long:              "Selection only. Without a name, opens the list with no other actions available.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			entry, err := pickTask(cfg, args, tui.Action{
				Key: "enter", Name: "select", Help: "cd here",
			})
			if err != nil || entry == nil {
				return err
			}
			return enterDir(cmd.OutOrStdout(), entry.View.Task.Dir)
		},
	}
}

func newTaskRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove [name]",
		Aliases: []string{"rm"},
		GroupID: groupTaskWork,
		Short:   "Delete a task and its worktrees",
		Long: "Asks first when there is something to lose: uncommitted work, unpushed\n" +
			"commits, files in artifacts/, or an agent still working. A live agent is\n" +
			"stopped before the directory goes.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			entry, err := pickTask(cfg, args, tui.Action{
				Key: "enter", Name: "delete", Help: "delete",
				Confirm: tui.ConfirmIfRisky, Verb: "Delete",
			})
			if err != nil || entry == nil {
				return err
			}
			return removeTask(cmd, cfg, *entry, yes, len(args) > 0)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation")
	return cmd
}

func newTaskPRCmd() *cobra.Command {
	var draft bool
	cmd := &cobra.Command{
		Use:     "pr [name]",
		GroupID: groupTaskWork,
		Short:   "Push every branch and open a pull request per repository",
		Long: "Commits anything outstanding (asking first), pushes each repository's\n" +
			"branch, and opens a pull request into the branch it was cut from.\n" +
			"Re-running is safe: an existing pull request is reused, not duplicated.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			entry, err := pickTask(cfg, args, tui.Action{
				Key: "enter", Name: "pr", Help: "open pull requests",
			})
			if err != nil || entry == nil {
				return err
			}
			_, err = publishTask(cmd, cfg, entry.View.Task, draft)
			return err
		},
	}
	cmd.Flags().BoolVar(&draft, "draft", false, "open the pull requests as drafts")
	return cmd
}

func newTaskCompleteCmd() *cobra.Command {
	var (
		draft bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:     "complete [name]",
		GroupID: groupTaskWork,
		Short:   "Open the pull requests, then delete the task",
		Long: "Runs pr and then remove. The task is kept if any repository fails to\n" +
			"reach the remote, so work is never deleted before it is safe.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTaskNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			entry, err := pickTask(cfg, args, tui.Action{
				Key: "enter", Name: "complete", Help: "pr, then delete",
			})
			if err != nil || entry == nil {
				return err
			}

			published, err := publishTask(cmd, cfg, entry.View.Task, draft)
			if err != nil {
				return err
			}
			if !published {
				return errors.New("not every repository reached the remote, so the task was kept")
			}

			// The state changed underneath us: re-read it so the deletion
			// decision sees the commits that were just pushed.
			fresh, err := reloadEntry(cfg, entry.View.Task.Dir)
			if err != nil {
				return err
			}
			return removeTask(cmd, cfg, fresh, yes, true)
		},
	}
	cmd.Flags().BoolVar(&draft, "draft", false, "open the pull requests as drafts")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation before deleting")
	return cmd
}

// pickTask resolves the named task, or opens the list restricted to a single
// action. A nil entry with a nil error means the user quit the list.
func pickTask(cfg config.Config, args []string, action tui.Action) (*taskEntry, error) {
	if len(args) == 1 {
		t, err := task.Resolve(cfg, args[0])
		if err != nil {
			return nil, err
		}
		entry, err := reloadEntry(cfg, t.Dir)
		if err != nil {
			return nil, err
		}
		return &entry, nil
	}

	if !tui.Interactive() {
		return nil, errors.New("name a task, or run this from a terminal to pick one from the list")
	}

	entries, err := surveyTasks(cfg)
	if err != nil {
		return nil, err
	}
	rows := make([]tui.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, taskRow(e))
	}

	outcome, err := tui.Run(tui.Config{
		Title:   fmt.Sprintf("%s · tasks in %s", action.Name, cfg.Tasks()),
		Rows:    rows,
		MaxRows: cfg.ListRows,
		Empty:   "no tasks yet — create one with: nm task new <repo>... -n <name>",
		Actions: []tui.Action{action},
	})
	if err != nil || outcome.Action == "" {
		return nil, err
	}
	entry, ok := outcome.Row.Data.(taskEntry)
	if !ok {
		return nil, fmt.Errorf("unexpected row payload %T", outcome.Row.Data)
	}
	return &entry, nil
}

// reloadEntry re-reads one task and the agent working on it.
func reloadEntry(cfg config.Config, dir string) (taskEntry, error) {
	entries, err := surveyTasks(cfg)
	if err != nil {
		return taskEntry{}, err
	}
	for _, e := range entries {
		if e.View.Task.Dir == dir {
			return e, nil
		}
	}
	return taskEntry{}, fmt.Errorf("task at %s is no longer there", dir)
}

// needsConfirmation reports whether deleting this task would lose something:
// uncommitted work, unpushed commits, artifacts, or an agent still going.
func needsConfirmation(e taskEntry) bool {
	if len(e.View.Hazards()) > 0 {
		return true
	}
	return e.Session != nil && e.Class() != agent.ClassDone
}

// removeTask deletes a task, asking first when there is something to lose.
// The list has already asked when it was the one that chose the task.
func removeTask(cmd *cobra.Command, cfg config.Config, e taskEntry, yes, named bool) error {
	out := cmd.OutOrStdout()

	if named && !yes && needsConfirmation(e) {
		if !tui.Interactive() {
			return fmt.Errorf("%s has work that would be lost (%s); re-run with -y to delete it anyway",
				e.View.Task.Label(), strings.Join(removalHazards(e), "; "))
		}
		choice, err := tui.Choose(tui.ChooseConfig{
			Title:    "Delete " + e.View.Task.Label() + "?",
			Subtitle: e.View.Task.Dir,
			Hazards:  removalHazards(e),
			Choices:  []string{"Cancel", "Delete anyway"},
			Default:  0,
		})
		if err != nil {
			return err
		}
		if choice != 1 {
			fmt.Fprintln(out, "left alone")
			return nil
		}
	}
	return deleteTask(out, cfg, e)
}

// removalHazards is what the confirmation lists, including the agent that
// would be stopped.
func removalHazards(e taskEntry) []string {
	hazards := e.View.Hazards()
	if e.Session != nil && e.Class() != agent.ClassDone {
		hazards = append(hazards, fmt.Sprintf("an agent is %s here and will be stopped", e.Session.Describe()))
	}
	return hazards
}

// publishTask pushes and opens pull requests, reporting whether every
// repository ended up with one.
func publishTask(cmd *cobra.Command, cfg config.Config, t task.Task, draft bool) (bool, error) {
	out := cmd.OutOrStdout()

	results, err := task.Publish(&t, task.PublishOptions{
		Draft: draft,
		GH:    forge.Client{Bin: cfg.GHCommand},
		Ask:   commitAsker(out),
	})
	if err != nil {
		switch {
		case errors.Is(err, forge.ErrNotAuthenticated):
			return false, fmt.Errorf("%w — run: gh auth login", err)
		case errors.Is(err, forge.ErrNotInstalled):
			return false, fmt.Errorf("%w — install it from https://cli.github.com", err)
		}
		return false, err
	}

	everything := true
	for _, r := range results {
		switch r.State {
		case task.PROpened:
			fmt.Fprintf(out, "  %s → %s\n", r.Repo, r.URL)
		case task.PRExisted:
			fmt.Fprintf(out, "  %s → %s (already open, pushed)\n", r.Repo, r.URL)
		case task.PRSkipped:
			fmt.Fprintf(out, "  %s — %s\n", r.Repo, r.Reason)
			if r.Reason != "no commits to review" {
				everything = false
			}
		case task.PRFailed:
			fmt.Fprintf(out, "  %s — failed: %s\n", r.Repo, r.Reason)
			everything = false
		}
	}
	return everything, nil
}

// commitAsker asks what to do about uncommitted work in one repository, and
// collects the commit message when the answer is to commit it.
func commitAsker(out io.Writer) task.Asker {
	return func(repo task.Repo, status gitx.Status) (task.CommitDecision, string, error) {
		if !tui.Interactive() {
			return task.CommitAbort, "", fmt.Errorf(
				"%s has uncommitted work (%s); commit it, or re-run from a terminal to do it here",
				repo.Name, strings.Join(status.Hazards(), ", "))
		}

		choice, err := tui.Choose(tui.ChooseConfig{
			Title:    repo.Name + " has uncommitted work",
			Subtitle: repo.Dir,
			Detail: []string{
				strings.Join(status.Hazards(), ", "),
				"Committing stages everything, untracked files included.",
			},
			Choices: []string{"Cancel", "Skip this repo", "Commit"},
			Default: 2,
		})
		if err != nil {
			return task.CommitAbort, "", err
		}
		switch choice {
		case 1:
			return task.CommitSkipRepo, "", nil
		case 2:
			result, err := tui.RunPrompt(tui.PromptConfig{
				Rows:  5,
				Title: "Commit message for " + repo.Name,
				Context: []string{
					repo.Dir,
					strings.Join(status.Hazards(), ", "),
				},
				Placeholder: "Summarize the change",
			})
			if err != nil {
				return task.CommitAbort, "", err
			}
			if !result.Submitted || strings.TrimSpace(result.Text) == "" {
				fmt.Fprintf(out, "  %s — no message given, left uncommitted\n", repo.Name)
				return task.CommitSkipRepo, "", nil
			}
			return task.CommitChanges, result.Text, nil
		default:
			return task.CommitAbort, "", nil
		}
	}
}
