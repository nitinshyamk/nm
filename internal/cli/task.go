package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/tui"
	"github.com/spf13/cobra"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "task",
		GroupID: groupCommon,
		Short:   "Create, enter, and delete multi-repo tasks",
		Long: "A task is a directory holding one worktree per repository, the\n" +
			"input/, artifacts/, and scratch/ directories, and optionally a\n" +
			"background claude agent working on it.\n\n" +
			"With no arguments, opens the task list with every action available.\n" +
			"The subcommands below each do one thing to one task, and complete\n" +
			"task names as you type them.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTaskList(cmd)
		},
	}
	cmd.AddGroup(
		&cobra.Group{ID: groupTaskWork, Title: "Working on a task:"},
		&cobra.Group{ID: groupTaskMake, Title: "Creating a task:"},
	)
	cmd.AddCommand(
		newTaskListCmd(),
		newTaskSelectCmd(),
		newTaskPRCmd(),
		newTaskCompleteCmd(),
		newTaskRemoveCmd(),
		newTaskNewCmd(),
		newTaskRebaseCmd(),
	)
	return cmd
}

// newTaskListCmd is the bare `nm task` under a name, so the listing is
// something tab-completion can offer rather than a thing you have to know.
func newTaskListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		GroupID:           groupTaskWork,
		Short:             "List every task, grouped by what it needs from you",
		Long:              "The same list `nm task` opens with no arguments.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTaskList(cmd)
		},
	}
}

// Groups within `nm task`: the verbs you reach for daily, then creation.
const (
	groupTaskWork = "task-work"
	groupTaskMake = "task-make"
)

func newTaskNewCmd() *cobra.Command {
	var (
		name       string
		withPrompt bool
		offline    bool
	)
	cmd := &cobra.Command{
		Use:     "new <repo> [repo...] -n <name> [-p]",
		GroupID: groupTaskMake,
		Short:   "Create a task spanning one or more repositories",
		Long: "Creates <tasks_root>/<name>-<hash> containing a worktree per\n" +
			"repository, input/ for the prompt and any assets that come with it,\n" +
			"artifacts/ for output that is never committed, and scratch/ for\n" +
			"throwaway working notes.\n\n" +
			"-p takes no argument: it opens an editor for the prompt, then starts\n" +
			"a background claude agent in the task directory.",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeRepoList,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return errors.New("a task needs a name: -n <name>")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			t, err := task.Create(cfg, task.Options{Name: name, Repos: args, Offline: offline})
			if err != nil {
				if errors.Is(err, gitx.ErrRemoteUnreachable) {
					return fmt.Errorf("%w\n\nnm branches from the remote's current default branch so you never\n"+
						"start from a stale commit. To branch from local refs instead, re-run\n"+
						"with --offline", err)
				}
				return err
			}

			fmt.Fprintf(out, "created %s\n", t.Dir)
			for _, r := range t.Repos {
				fmt.Fprintf(out, "  %s → %s (from %s)\n", r.Name, r.Branch, r.BaseBranch)
			}

			if withPrompt {
				if err := startAgent(out, cfg, &t); err != nil {
					// The task itself is fine; only the agent failed to start.
					fmt.Fprintf(cmd.ErrOrStderr(), "nm: %v\n", err)
				}
			}
			return enterDir(out, t.Dir)
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "task name (required)")
	cmd.Flags().BoolVarP(&withPrompt, "prompt", "p", false, "write a prompt and start a background agent")
	cmd.Flags().BoolVar(&offline, "offline", false, "branch from local refs instead of fetching from origin")
	_ = cmd.RegisterFlagCompletionFunc("name", cobra.NoFileCompletions)
	return cmd
}

// startAgent collects a prompt and launches a background agent for the task.
func startAgent(out io.Writer, cfg config.Config, t *task.Task) error {
	client := agent.Client{Bin: cfg.ClaudeCommand}
	if !client.Available() {
		return fmt.Errorf("%s is not on PATH, so no agent was started", cfg.ClaudeCommand)
	}

	prompt, err := collectPrompt(cfg, *t)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		fmt.Fprintln(out, "no prompt given, so the task starts without an agent")
		return nil
	}

	id, raw, err := client.Launch(agent.LaunchOptions{
		Dir:     t.Dir,
		Name:    "nm-" + t.Label(),
		Prompt:  prompt,
		AddDirs: t.Dirs(),
	})
	if err != nil {
		return err
	}

	t.Prompt = prompt
	t.Agent = &task.Agent{ID: id, LaunchedAt: time.Now(), Raw: raw}
	if err := t.Save(); err != nil {
		return err
	}
	if err := t.SavePrompt(cfg); err != nil {
		return err
	}

	if id == "" {
		fmt.Fprintln(out, "started a background agent (its id could not be read; nm will find it by directory)")
	} else {
		fmt.Fprintf(out, "started background agent %s\n", id)
	}
	return nil
}

// collectPrompt opens the editor, or reads stdin when nm is being scripted.
func collectPrompt(cfg config.Config, t task.Task) (string, error) {
	if !tui.Interactive() {
		piped, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading the prompt from stdin: %w", err)
		}
		return strings.TrimSpace(string(piped)), nil
	}

	repos := make([]string, 0, len(t.Repos))
	for _, r := range t.Repos {
		repos = append(repos, r.Name)
	}
	result, err := tui.RunPrompt(tui.PromptConfig{
		Rows:  cfg.PromptRows,
		Title: "Prompt for " + t.Label(),
		Context: []string{
			t.Dir,
			"repos: " + strings.Join(repos, ", "),
			"the agent starts in the task directory and can reach every worktree",
		},
		Placeholder: "What should the agent do?",
	})
	if err != nil {
		return "", err
	}
	if !result.Submitted {
		return "", nil
	}
	return result.Text, nil
}
