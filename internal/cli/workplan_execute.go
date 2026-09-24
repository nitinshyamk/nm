package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/forge"
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

var _ workplan.Agents = agentRunner{}
