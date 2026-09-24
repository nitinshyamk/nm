package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/workplan"
	"github.com/spf13/cobra"
)

// Groups within `nm workplan`: scoping the work, then running it.
const (
	groupPlanScope = "workplan-scope"
	groupPlanRun   = "workplan-run"
)

func newWorkplanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workplan",
		Aliases: []string{"wp"},
		GroupID: groupCommon,
		Short:   "Scope and run a body of work as a set of tasks",
		Long: "A workplan is a directory holding one file per task, in the directory\n" +
			"named for the state that task is in: planned, in-progress, review,\n" +
			"approved, completed. A task moves state by moving directory, so the\n" +
			"whole plan is legible with ls and nothing is kept in a database.\n\n" +
			"With no arguments, lists every workplan and what it is waiting on.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkplanList(cmd)
		},
	}
	cmd.AddGroup(
		&cobra.Group{ID: groupPlanScope, Title: "Scoping the work:"},
		&cobra.Group{ID: groupPlanRun, Title: "Running it:"},
	)
	cmd.AddCommand(
		newWorkplanDefineCmd(),
		newWorkplanAddCmd(),
		newWorkplanVerifyCmd(),
		newWorkplanListCmd(),
		newWorkplanExecuteCmd(),
		newWorkplanResolveCmd(),
		newWorkplanAwaitCmd(),
	)
	return cmd
}

func newWorkplanListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		GroupID:           groupPlanRun,
		Short:             "List every workplan and where its tasks are",
		Long:              "The same listing `nm workplan` prints with no arguments.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkplanList(cmd)
		},
	}
}

func newWorkplanDefineCmd() *cobra.Command {
	var (
		name      string
		artifacts string
	)
	cmd := &cobra.Command{
		Use:     "define -n <name> [-a <path>]",
		GroupID: groupPlanScope,
		Short:   "Create an empty workplan",
		Long: "Creates <workplans_root>/<name> holding one directory per state —\n" +
			"planned, in-progress, review, approved, completed — plus escalations/\n" +
			"for what an agent needs a human to decide, and artifacts/ for the\n" +
			"design the tasks were scoped against.\n\n" +
			"-a copies a file, or the contents of a directory, into artifacts/.\n" +
			"Re-running repairs a workplan whose directories are missing; it will\n" +
			"not replace an artifact that is already there.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return errors.New("a workplan needs a name: -n <name>")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w, err := workplan.Define(cfg, workplan.Options{Name: name, Artifacts: artifacts})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created %s\n", w.Dir)
			for _, s := range workplan.States() {
				fmt.Fprintf(out, "  %s/\n", s)
			}
			if artifacts != "" {
				fmt.Fprintf(out, "  artifacts/ ← %s\n", artifacts)
			}
			fmt.Fprintf(out, "\nadd tasks with: nm workplan add %s --content @<file>\n", w.Name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "workplan name (required)")
	cmd.Flags().StringVarP(&artifacts, "artifacts", "a", "", "file or directory to copy into artifacts/")
	_ = cmd.RegisterFlagCompletionFunc("name", cobra.NoFileCompletions)
	// The one nm flag that genuinely wants a path, so files are the right
	// completion here rather than the usual refusal.
	_ = cmd.RegisterFlagCompletionFunc("artifacts", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	})
	return cmd
}

func newWorkplanAddCmd() *cobra.Command {
	var content string
	cmd := &cobra.Command{
		Use:     "add <workplan> --content <json>",
		GroupID: groupPlanScope,
		Short:   "Add a task definition to a workplan",
		Long: "Validates a task definition and writes it to planned/<id>.json, then\n" +
			"creates escalations/<id>/ for it.\n\n" +
			"--content takes the JSON itself, @<file> to read a file, or - to read\n" +
			"stdin. A task description is prose with newlines in it, which a\n" +
			"command line mangles, so @<file> is usually what you want.\n\n" +
			"The definition needs id, description, and acceptance-criteria;\n" +
			"predecessors and repositories may be empty. An id is a number then\n" +
			"hyphen-joined lowercase words, as in 01-prepare-codebase.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkplanNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if content == "" {
				return errors.New("a task needs a definition: --content <json>, --content @<file>, or --content -")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w, err := workplan.Resolve(cfg, args[0])
			if err != nil {
				return err
			}

			blob, err := readContent(cmd.InOrStdin(), content)
			if err != nil {
				return err
			}
			task, err := workplan.ParseTask(blob)
			if err != nil {
				return err
			}
			if err := w.AddTask(task); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "added %s to %s\n", task.ID, w.Name)
			if len(task.Predecessors) > 0 {
				fmt.Fprintf(out, "  after   %s\n", strings.Join(task.Predecessors, ", "))
			}
			if len(task.Repositories) > 0 {
				fmt.Fprintf(out, "  repos   %s\n", strings.Join(task.Repositories, ", "))
			} else {
				fmt.Fprintf(out, "  repos   none — no code changes expected\n")
			}
			fmt.Fprintf(out, "  %d acceptance criteri%s\n", len(task.AcceptanceCriteria),
				map[bool]string{true: "on", false: "a"}[len(task.AcceptanceCriteria) == 1])
			return nil
		},
	}
	cmd.Flags().StringVar(&content, "content", "", "task definition as JSON, @<file>, or - for stdin")
	_ = cmd.RegisterFlagCompletionFunc("content", cobra.NoFileCompletions)
	return cmd
}

func newWorkplanVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "verify <workplan>",
		GroupID: groupPlanScope,
		Short:   "Check that a workplan is sound",
		Long: "Three checks, all reported together rather than failing on the first:\n\n" +
			"  schema      every task file parses, conforms, and is named for its id\n" +
			"  resolution  predecessors name tasks here, repositories exist\n" +
			"  acyclicity  the predecessors imply no cycle\n\n" +
			"Exits non-zero when anything is wrong, so it can gate a script.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkplanNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w, err := workplan.Resolve(cfg, args[0])
			if err != nil {
				return err
			}

			report := workplan.Verify(cfg, w)
			out := cmd.OutOrStdout()
			if report.OK() {
				fmt.Fprintf(out, "%s is sound: %s, no problems\n", w.Name, plural(report.Tasks, "task", "tasks"))
				return nil
			}
			fmt.Fprintf(out, "%s has %s:\n\n", w.Name, plural(len(report.Problems), "problem", "problems"))
			for _, p := range report.Problems {
				fmt.Fprintf(out, "  %s\n", p)
			}
			// The error is what makes the exit code non-zero; the detail is
			// already printed, so it must not be repeated in the message.
			return errors.New("the workplan is not sound")
		},
	}
	return cmd
}

// runWorkplanList prints every workplan with a count per state, so the shape of
// the work is visible without opening the directory.
func runWorkplanList(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	plans, err := workplan.List(cfg)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		fmt.Fprintf(out, "no workplans in %s\n", cfg.Workplans())
		fmt.Fprintln(out, "create one with: nm workplan define -n <name>")
		return nil
	}

	for _, w := range plans {
		placed, errs := w.Tasks()
		counts := make(map[workplan.State]int, len(workplan.States()))
		for _, p := range placed {
			counts[p.State]++
		}

		fmt.Fprintf(out, "%s  (%s)\n", w.Name, plural(len(placed), "task", "tasks"))
		for _, s := range workplan.States() {
			if counts[s] > 0 {
				fmt.Fprintf(out, "  %-12s %d\n", s, counts[s])
			}
		}
		if len(errs) > 0 {
			fmt.Fprintf(out, "  %s — run: nm workplan verify %s\n",
				plural(len(errs), "unreadable task", "unreadable tasks"), w.Name)
		}
	}
	return nil
}

// readContent resolves --content, which is the JSON itself, @<file>, or - for
// stdin.
//
// A task description is prose with newlines, and every shell mangles that
// differently on the command line, so reading from a file is the path that
// works everywhere.
func readContent(stdin io.Reader, value string) ([]byte, error) {
	switch {
	case value == "-":
		blob, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading the task definition from stdin: %w", err)
		}
		return blob, nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		blob, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading the task definition: %w", err)
		}
		return blob, nil
	default:
		return []byte(value), nil
	}
}

// plural renders a count with its noun, the way the task view already does.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
