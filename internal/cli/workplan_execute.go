package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/workplan"
	"github.com/spf13/cobra"
)

func newWorkplanExecuteCmd() *cobra.Command {
	var (
		merge  bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:     "execute <workplan>",
		GroupID: groupPlanRun,
		Short:   "Move every task as far as its rules allow",
		Long: "One pass over the workplan. Collects escalations, moves tasks whose\n" +
			"conditions are met, starts the work that is unblocked, and prints what\n" +
			"changed. Nothing changed prints nothing, so this is safe to run on a\n" +
			"timer.\n\n" +
			"Each pass reads the filesystem and GitHub from scratch, so a pass that\n" +
			"was interrupted is repaired by running again rather than cleaned up\n" +
			"after.\n\n" +
			"--merge allows the last transition, approved into completed. Without it\n" +
			"an approved task is reported as ready to merge and left alone, so a\n" +
			"poller left running never puts anything into production on its own.",
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

			gh := forge.Client{Bin: cfg.GHCommand}
			if err := gh.CheckAuth(); err != nil {
				switch {
				case errors.Is(err, forge.ErrNotAuthenticated):
					return fmt.Errorf("%w — run: gh auth login", err)
				case errors.Is(err, forge.ErrNotInstalled):
					return fmt.Errorf("%w — install it from https://cli.github.com", err)
				}
				return err
			}

			result, err := workplan.Execute(w, workplan.ExecuteOptions{
				Config: cfg,
				Review: gh,
				Agents: agentRunner{cfg: cfg},
				Merge:  merge,
			})
			if err != nil {
				if errors.Is(err, workplan.ErrLocked) {
					// Not a failure: the other run is doing this work. Exiting zero
					// keeps a one-minute timer from filling a log with errors when
					// one pass runs long.
					fmt.Fprintf(cmd.OutOrStdout(), "%v\n", err)
					return nil
				}
				return err
			}

			if asJSON {
				return writeJSON(cmd, result)
			}
			printResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&merge, "merge", false, "allow approved tasks to be merged and completed")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the pass as JSON")
	return cmd
}

func newWorkplanResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resolve <workplan>",
		GroupID: groupPlanRun,
		Short:   "Deliver escalation answers to the tasks waiting on them",
		Long: "Copies every escalations/<id>/<timestamp>-resolution.md into the task\n" +
			"directory it belongs to, where the agent blocked on it will see it.\n\n" +
			"`nm workplan execute` does this too, so this command is for delivering\n" +
			"an answer without waiting for the next pass.",
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

			delivered, problems, err := workplan.DeliverAll(cfg, w)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, d := range delivered {
				fmt.Fprintf(out, "%s → %s\n", d.Name, d.TaskID)
			}
			for _, p := range problems {
				fmt.Fprintf(cmd.ErrOrStderr(), "nm: %s\n", p)
			}
			if len(delivered) == 0 {
				fmt.Fprintln(out, "nothing to deliver")
			}
			return nil
		},
	}
	return cmd
}

// printResult writes the two sections the design calls for: what moved, and what
// needs a human. Waiting comes last and only when nothing else happened, so a
// pass that did something is not buried under a list of what it did not do.
func printResult(cmd *cobra.Command, result workplan.Result) {
	out := cmd.OutOrStdout()

	if len(result.Transitions) > 0 {
		fmt.Fprintln(out, "transitions:")
		for _, t := range result.Transitions {
			fmt.Fprintf(out, "  %s\n", t)
		}
	}

	if len(result.Escalations) > 0 {
		fmt.Fprintln(out, "escalations:")
		for _, e := range result.Escalations {
			fmt.Fprintf(out, "  %s  %s\n", e.TaskID, e.Stamp())
			// The body is indented rather than summarized: whoever is reading
			// this has to decide something, and a summary would make them open
			// the file anyway.
			for _, line := range strings.Split(strings.TrimRight(e.Body, "\n"), "\n") {
				fmt.Fprintf(out, "    %s\n", line)
			}
		}
	}

	if len(result.Refines) > 0 {
		fmt.Fprintf(out, "refining: %s\n", strings.Join(result.Refines, ", "))
	}

	for _, p := range result.Problems {
		fmt.Fprintf(cmd.ErrOrStderr(), "nm: %s\n", p)
	}

	if result.Quiet() && len(result.Waiting) > 0 {
		fmt.Fprintln(out, "waiting:")
		for _, wait := range result.Waiting {
			fmt.Fprintf(out, "  %s\n", wait)
		}
	}
}

// writeJSON emits the pass for a poller to read. The shape is stable and
// explicit rather than whatever the internal types happen to marshal to.
func writeJSON(cmd *cobra.Command, result workplan.Result) error {
	type transition struct {
		Task string `json:"task"`
		From string `json:"from"`
		To   string `json:"to"`
		Note string `json:"note,omitempty"`
	}
	type escalation struct {
		Task  string `json:"task"`
		Stamp string `json:"stamp"`
		Body  string `json:"body"`
	}
	payload := struct {
		Transitions []transition `json:"transitions"`
		Escalations []escalation `json:"escalations"`
		Refining    []string     `json:"refining"`
		Waiting     []string     `json:"waiting"`
		Problems    []string     `json:"problems"`
		Quiet       bool         `json:"quiet"`
	}{
		Transitions: []transition{},
		Escalations: []escalation{},
		Refining:    result.Refines,
		Waiting:     result.Waiting,
		Problems:    result.Problems,
		Quiet:       result.Quiet(),
	}
	for _, t := range result.Transitions {
		payload.Transitions = append(payload.Transitions, transition{
			Task: t.TaskID, From: string(t.From), To: string(t.To), Note: t.Note,
		})
	}
	for _, e := range result.Escalations {
		payload.Escalations = append(payload.Escalations, escalation{
			Task: e.TaskID, Stamp: e.Stamp(), Body: e.Body,
		})
	}
	// Empty rather than null, so a reader can iterate without checking first.
	if payload.Refining == nil {
		payload.Refining = []string{}
	}
	if payload.Waiting == nil {
		payload.Waiting = []string{}
	}
	if payload.Problems == nil {
		payload.Problems = []string{}
	}

	blob, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the pass: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(blob))
	return nil
}

// agentRunner adapts the claude CLI to what the orchestrator needs.
type agentRunner struct {
	cfg config.Config
}

func (a agentRunner) Launch(dir, name, prompt string, addDirs []string) (string, error) {
	client := agent.Client{Bin: a.cfg.ClaudeCommand}
	if !client.Available() {
		return "", fmt.Errorf("%s is not on PATH", a.cfg.ClaudeCommand)
	}
	id, _, err := client.Launch(agent.LaunchOptions{
		Dir: dir, Name: name, Prompt: prompt, AddDirs: addDirs,
	})
	return id, err
}

// Alive reports whether an agent is still working.
//
// A session nm cannot ask about is reported as alive, which is the safe answer:
// the refine latch uses this, and a false "gone" would start a second agent in a
// worktree that already has one.
func (a agentRunner) Alive(id, dir string) bool {
	client := agent.Client{Bin: a.cfg.ClaudeCommand}
	if !client.Available() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), agent.ListTimeout)
	defer cancel()

	sessions, err := client.List(ctx)
	if err != nil {
		return true
	}
	for _, s := range sessions {
		matches := (id != "" && (s.ID == id || s.SessionID == id)) || sameOrUnder(s.CWD, dir)
		if !matches {
			continue
		}
		return s.Class() != agent.ClassDone
	}
	// Known-good claude, a readable list, and no session: it is genuinely gone.
	return false
}

// Stalled reports that an agent exists but is going nowhere: blocked waiting for
// input, or stopped.
//
// Unlike Alive, the cautious answer here is false. This drives a message telling
// the user to go and look, and crying wolf every minute on a claude that cannot be
// queried would train them to ignore it.
func (a agentRunner) Stalled(id, dir string) bool {
	client := agent.Client{Bin: a.cfg.ClaudeCommand}
	if !client.Available() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), agent.ListTimeout)
	defer cancel()

	sessions, err := client.List(ctx)
	if err != nil {
		return false
	}
	for _, s := range sessions {
		matches := (id != "" && (s.ID == id || s.SessionID == id)) || sameOrUnder(s.CWD, dir)
		if !matches {
			continue
		}
		// Needing input is the blocked case a background agent cannot recover
		// from; done is an agent that exited without writing a ready marker.
		// Either way the task is not progressing and nobody has been told.
		switch s.Class() {
		case agent.ClassNeedsInput, agent.ClassDone:
			return true
		default:
			return false
		}
	}
	return false
}

var _ workplan.Agents = agentRunner{}

func newWorkplanAwaitCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "await-resolution <escalation-file>",
		GroupID: groupPlanRun,
		Short:   "Block until an escalation is answered",
		Long: "Waits for <timestamp>-resolution.md to appear beside the escalation,\n" +
			"then exits zero and prints its path. Exits non-zero on timeout.\n\n" +
			"This exists so a blocked agent waits on one process instead of waking on\n" +
			"a timer. An agent's wakeup reloads its whole conversation context, so an\n" +
			"unanswered escalation polled every two minutes costs hundreds of reloads\n" +
			"overnight; a blocked process costs nothing.\n\n" +
			"Returns immediately when the answer is already there, so an agent that\n" +
			"restarted after one landed does not wait for nothing.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := workplan.Await(cmd.Context(), args[0], timeout)
			if err != nil {
				if errors.Is(err, workplan.ErrAwaitTimeout) {
					// A timeout is an outcome the skill handles, not a crash: the
					// escalation stays open and the answer can still arrive.
					return fmt.Errorf("%w after %s; the escalation is still open", err, timeout)
				}
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour,
		"how long to wait before giving up (0 waits forever)")
	_ = cmd.RegisterFlagCompletionFunc("timeout", cobra.NoFileCompletions)
	return cmd
}

func newWorkplanAwaitFeedbackCmd() *cobra.Command {
	var (
		timeout time.Duration
		since   string
	)
	cmd := &cobra.Command{
		Use:     "await-feedback",
		GroupID: groupPlanRun,
		Short:   "Block until a reviewer acts on this task's pull request",
		Long: "Run from a task directory after publishing. Waits for a reviewer to\n" +
			"comment, approve, or merge, then prints the verdict and exits zero:\n\n" +
			"  feedback nm#12 at 2026-05-01T09:12:44Z   action it, then wait again\n" +
			"  approved nm#12                           the task is done\n" +
			"  merged   nm#12                           already in the base branch\n\n" +
			"This is what lets one agent own a task end to end rather than exiting\n" +
			"after publishing and being replaced for each review round. A replacement\n" +
			"loses everything the first agent knew and can overlap with it in the same\n" +
			"worktree.\n\n" +
			"Unlike await-resolution, which stats a local file, every check here spends\n" +
			"a GitHub call — so the interval widens as the wait goes on: every 30s for\n" +
			"5m, then every 2m for 20m, then every 10m for 2h, then hourly. A reviewer\n" +
			"who just looked is likely to say more within minutes; one who has been\n" +
			"quiet for hours is on another day. Statuses are cached briefly on disk, so\n" +
			"a waiting agent and an orchestrator pass do not both pay for the same read.\n\n" +
			"Times out after 12h by default, which is a resumable outcome and not a\n" +
			"failure: the pull request is still open and still in review, so run this\n" +
			"again to keep waiting rather than letting the agent be replaced.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			found, err := task.Current(cfg)
			if err != nil {
				return err
			}

			// Default to the published timestamp, so "newer than the work" means the
			// same thing here as it does to the orchestrator. Anything older would
			// replay comments the agent has already dealt with.
			from, err := feedbackSince(cfg, found, since)
			if err != nil {
				return err
			}

			// Cached, so the waiting agent and the orchestrator's own pass do not
			// each spend a call on the same pull request. The cache lives beside the
			// task rather than in the workplan, because a task directory is what this
			// command can always find.
			reviewer := workplan.CachedReviewer{
				Inner: forge.Client{Bin: cfg.GHCommand},
				Dir:   found.Dir,
			}

			verdict, err := workplan.AwaitFeedback(cmd.Context(), reviewer, found, from, timeout)
			if err != nil {
				if errors.Is(err, workplan.ErrFeedbackTimeout) {
					return fmt.Errorf("%w after %s; the pull request is still open and still "+
						"in review, so run this again to keep waiting", err, timeout)
				}
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), verdict)
			return nil
		},
	}
	// Twelve hours, not forever. A held session is the cost of one agent owning the
	// task, and an unbounded wait makes that cost unbounded too — a pull request
	// nobody ever reviews would pin an agent until the machine restarted. Half a day
	// covers an overnight review without leaving anything pinned indefinitely, and
	// the timeout is a resumable outcome rather than a failure: the skill is told to
	// run this again, so waiting continues without the agent being replaced.
	cmd.Flags().DurationVar(&timeout, "timeout", 12*time.Hour,
		"how long to wait before giving up (0 waits forever)")
	cmd.Flags().StringVar(&since, "since", "",
		"only count feedback newer than this RFC 3339 time (default: the ready-to-review timestamp)")
	_ = cmd.RegisterFlagCompletionFunc("timeout", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("since", cobra.NoFileCompletions)
	return cmd
}

// feedbackSince resolves what "new" feedback means for this wait.
func feedbackSince(cfg config.Config, t task.Task, flag string) (time.Time, error) {
	if flag != "" {
		at, err := time.Parse(time.RFC3339, flag)
		if err != nil {
			return time.Time{}, fmt.Errorf("reading --since: %w", err)
		}
		return at, nil
	}
	at, err := workplan.ReadStamp(t.Ready(cfg))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w; publish and write ready-to-review.md before waiting, "+
			"or pass --since", err)
	}
	return at, nil
}
