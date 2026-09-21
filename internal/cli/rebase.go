package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/spf13/cobra"
)

func newTaskRebaseCmd() *cobra.Command {
	var (
		base    string
		remote  string
		noAgent bool
		noPush  bool
		verify  bool
	)
	cmd := &cobra.Command{
		Use:     "rebase <repo> <branch>",
		GroupID: groupTaskMake,
		Short:   "Replay a branch that is already on the remote onto its base",
		Long: "Checks <branch> out into a task of its own, replays it onto the base\n" +
			"branch with git pull --rebase, and force-pushes the result.\n\n" +
			"The branch must already exist on the remote: this replays work that is\n" +
			"out there, it does not start any. The worktree keeps the branch's own\n" +
			"name rather than inventing one, because the branch is what gets pushed\n" +
			"back; only the task directory carries a hash.\n\n" +
			"A clean replay is pushed straight away with --force-with-lease, which\n" +
			"refuses if anyone else moved the branch. Conflicts are handed to a\n" +
			"background agent that resolves them, runs the repository's own checks,\n" +
			"and pushes only if they pass — escalating to you rather than guessing\n" +
			"when a conflict is really a decision.\n\n" +
			"--verify holds a clean replay back as well, and has the agent run the\n" +
			"checks before pushing: git merging the text without complaining is not\n" +
			"the same as the result still working.\n\n" +
			"Your shell is left in the worktree.",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeRebaseArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			rb, err := task.StartRebase(cfg, task.RebaseOptions{
				Repo:   args[0],
				Branch: args[1],
				Base:   base,
				Remote: remote,
			})
			if err != nil {
				return remoteHint(err)
			}
			fmt.Fprintf(out, "created %s\n", rb.Task.Dir)
			fmt.Fprintf(out, "  %s → %s, rebasing onto %s (%s)\n",
				rb.RepoName(), rb.Branch(), rb.Base.Branch, rb.Base.Source)

			report, err := rb.Replay()
			if err != nil {
				return fmt.Errorf("%w\n\nthe worktree is kept at %s; remove it with: nm task remove %s",
					err, rb.Worktree(), rb.Task.Label())
			}

			if err := reportRebase(cmd, cfg, rb, report, rebaseFollowUp{
				noAgent: noAgent,
				noPush:  noPush,
				verify:  verify,
			}); err != nil {
				return err
			}
			return enterDir(out, rb.Worktree())
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "branch to rebase onto (default: the remote's default branch)")
	cmd.Flags().StringVar(&remote, "remote", "origin", "remote holding the branch")
	cmd.Flags().BoolVar(&noAgent, "no-agent", false, "leave conflicts for you instead of starting an agent")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "do not push, even when the replay is clean")
	cmd.Flags().BoolVar(&verify, "verify", false, "have the agent run the repository's checks before pushing a clean replay")
	_ = cmd.RegisterFlagCompletionFunc("base", completeBaseBranch)
	_ = cmd.RegisterFlagCompletionFunc("remote", completeRemotes)
	return cmd
}

// rebaseFollowUp is what the flags asked for once the replay is done.
type rebaseFollowUp struct {
	noAgent bool
	noPush  bool
	verify  bool
}

// reportRebase says what the replay did and does whatever follows from it:
// push a clean rewrite, hand it to an agent to check first, or hand the
// conflicts to an agent to resolve.
func reportRebase(cmd *cobra.Command, cfg config.Config, rb task.Rebase, report task.RebaseReport, follow rebaseFollowUp) error {
	out := cmd.OutOrStdout()
	noAgent, noPush := follow.noAgent, follow.noPush

	switch report.Outcome {
	case task.RebaseUpToDate:
		fmt.Fprintf(out, "%s is already on top of %s — nothing to push\n", rb.Branch(), rb.Base.Branch)
		return nil

	case task.RebaseReplayed:
		fmt.Fprintf(out, "replayed %s onto %s cleanly\n", rb.Branch(), rb.Base.Branch)
		if noPush {
			fmt.Fprintf(out, "not pushed (--no-push). When you are ready:\n  git -C %s push --force-with-lease %s %s\n",
				rb.Worktree(), rb.Remote, rb.Branch())
			return nil
		}
		if follow.verify && !noAgent {
			// A replay git did not complain about can still be broken, so
			// --verify buys the same gate the conflict path always runs.
			fmt.Fprintln(out, "not pushed yet (--verify): the checks run first")
			return startRebaseAgent(out, cmd.ErrOrStderr(), cfg, rb, report)
		}
		if err := rb.Push(); err != nil {
			return fmt.Errorf("the rebase is done but the push was refused: %w\n\n"+
				"--force-with-lease refuses when the branch moved on the remote while nm was\n"+
				"working. Fetch and look before pushing again: the worktree is at %s", err, rb.Worktree())
		}
		fmt.Fprintf(out, "force-pushed %s (with lease)\n", rb.Branch())
		return nil

	case task.RebaseConflicted:
		fmt.Fprintf(out, "the rebase stopped with conflicts")
		if len(report.Conflicts) == 0 {
			fmt.Fprintln(out)
		} else {
			fmt.Fprintf(out, " in %d file(s):\n", len(report.Conflicts))
			for _, path := range report.Conflicts {
				fmt.Fprintf(out, "  %s\n", path)
			}
		}
		if noAgent {
			fmt.Fprintf(out, "\nresolve them in %s, then:\n"+
				"  git add <files> && git rebase --continue\n"+
				"  git push --force-with-lease %s %s\n", rb.Worktree(), rb.Remote, rb.Branch())
			return nil
		}
		return startRebaseAgent(out, cmd.ErrOrStderr(), cfg, rb, report)
	}
	return nil
}

// startRebaseAgent hands the rebase to a background agent. A failure to start
// one is not a failure of the rebase: the worktree is sitting there either
// way, so nm says so and leaves it to the user.
func startRebaseAgent(out, errOut io.Writer, cfg config.Config, rb task.Rebase, report task.RebaseReport) error {
	client := agent.Client{Bin: cfg.ClaudeCommand}
	if !client.Available() {
		fmt.Fprintf(errOut, "nm: %s is not on PATH, so %s is yours to finish: %s\n",
			cfg.ClaudeCommand, rb.Branch(), rb.Worktree())
		return nil
	}

	prompt := task.RebasePrompt(cfg, rb, report)
	id, raw, err := client.Launch(agent.LaunchOptions{
		// The agent works in the worktree, where the rebase is, and can still
		// reach the task directory to leave an escalation note in artifacts/.
		Dir:     rb.Worktree(),
		Name:    "nm-rebase-" + rb.Task.Label(),
		Prompt:  prompt,
		AddDirs: []string{rb.Task.Dir},
	})
	if err != nil {
		fmt.Fprintf(errOut, "nm: %v\n", err)
		return nil
	}

	t := rb.Task
	t.Prompt = prompt
	t.Agent = &task.Agent{ID: id, LaunchedAt: time.Now(), Raw: raw}
	if err := t.Save(); err != nil {
		return err
	}
	if err := t.SavePrompt(cfg); err != nil {
		return err
	}

	job := "on the conflicts"
	if report.Outcome != task.RebaseConflicted {
		job = "on checking the replay"
	}
	if id == "" {
		fmt.Fprintf(out, "started an agent %s (its id could not be read; nm will find it by directory)\n", job)
	} else {
		fmt.Fprintf(out, "started agent %s %s\n", id, job)
	}
	fmt.Fprintf(out, "it pushes with --force-with-lease once the checks pass, or writes\n"+
		"%s/%s and stops if the call is yours to make\n", cfg.ArtifactsDir, task.EscalationFile)
	return nil
}

// remoteHint explains an unreachable origin. Unlike worktree creation there
// is no --offline answer here: a rebase onto the remote's branch has to reach
// the remote.
func remoteHint(err error) error {
	if errors.Is(err, gitx.ErrRemoteUnreachable) {
		return fmt.Errorf("%w\n\nrebase replays a remote branch onto the remote's default branch, so it\n"+
			"has no offline mode — there is nothing to fetch from", err)
	}
	return err
}
