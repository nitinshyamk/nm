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
		artifacts  string
		taskfile   string
		noAgent    bool
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
			"a background claude agent in the task directory.\n\n" +
			"--artifacts copies a file or directory into artifacts/, and --taskfile\n" +
			"copies a workplan task definition into input/. A task given a\n" +
			"definition also gets escalations/, and its prompt is generated rather\n" +
			"than asked for: it is work scoped somewhere else, so there is nothing\n" +
			"to type. With a definition, the repository list may be empty — a task\n" +
			"can be a clarification that entails no code change.\n\n" +
			"--no-agent builds the task without starting one, for inspecting what a\n" +
			"definition produces before handing it to an agent.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeRepoList,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return errors.New("a task needs a name: -n <name>")
			}
			// ArbitraryArgs replaces MinimumNArgs(1) so a task with a definition
			// can have no repositories. Without a definition the old rule still
			// holds, and saying so here keeps the error about the command line
			// rather than about a package invariant.
			if len(args) == 0 && taskfile == "" {
				return errors.New("name at least one repository, or pass --taskfile for work that changes no code")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			t, err := task.Create(cfg, task.Options{
				Name:      name,
				Repos:     args,
				Offline:   offline,
				Artifacts: artifacts,
				TaskFile:  taskfile,
			})
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
			if len(t.Repos) == 0 {
				fmt.Fprintln(out, "  no repositories — no code changes expected")
			}
			if t.TaskFile != "" {
				fmt.Fprintf(out, "  %s → %s\n", t.TaskFile, cfg.InputDir)
				fmt.Fprintf(out, "  %s/ for what needs a human\n", cfg.EscalationsDir)
			}

			switch {
			case t.TaskFile != "" && noAgent:
				// The task is built and its prompt is knowable, so say what would
				// have been started rather than leaving the caller to guess.
				fmt.Fprintf(out, "no agent started (--no-agent). To start one:\n  cd %s && claude -- %q\n",
					t.Dir, task.TaskFilePrompt(cfg, t))
			case t.TaskFile != "":
				// The prompt is generated rather than asked for: the work was
				// scoped in the workplan, so there is nothing for a human to type
				// here, and -p would open an empty editor for no reason.
				if err := startTaskFileAgent(out, cfg, &t); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "nm: %v\n", err)
				}
			case withPrompt:
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
	cmd.Flags().StringVarP(&artifacts, "artifacts", "a", "", "file or directory to copy into artifacts/")
	cmd.Flags().StringVar(&taskfile, "taskfile", "", "workplan task definition to copy into input/ and work from")
	cmd.Flags().BoolVar(&noAgent, "no-agent", false, "with --taskfile, build the task but do not start an agent")
	_ = cmd.RegisterFlagCompletionFunc("name", cobra.NoFileCompletions)
	// These two are the rare nm flags that genuinely take a path, so files are
	// the right completion rather than the usual refusal.
	for _, flag := range []string{"artifacts", "taskfile"} {
		_ = cmd.RegisterFlagCompletionFunc(flag, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveDefault
		})
	}
	return cmd
}

// startTaskFileAgent launches the agent for a task that came with a definition.
//
// The prompt is generated, so nothing is asked for and nothing is read from
// stdin: the work is already described in the file the agent is pointed at.
func startTaskFileAgent(out io.Writer, cfg config.Config, t *task.Task) error {
	client := agent.Client{Bin: cfg.ClaudeCommand}
	if !client.Available() {
		return fmt.Errorf("%s is not on PATH, so %s is yours to start: %s",
			cfg.ClaudeCommand, t.Label(), t.Dir)
	}

	prompt := task.TaskFilePrompt(cfg, *t)
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
