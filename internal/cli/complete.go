package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/repos"
	"github.com/nitinshyamk/nm/internal/shellint"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/workplan"
	"github.com/spf13/cobra"
)

// Completion runs on every tab press, in the user's live shell, while they
// are still typing. Everything in this file obeys three rules because of
// that: it never reports an error (an empty list is the honest answer when
// something is wrong), it never touches the network, and it never changes
// anything on disk.
//
// cobra completes command names, flag names, and --flag=value on its own. The
// functions here fill in the parts only nm knows: which repositories exist,
// which tasks exist, which branches a repository has, and which keys the
// configuration file takes.

// noFiles is the directive for an argument that is a name rather than a path:
// without it the shell falls back to listing the current directory, which is
// never what any nm argument wants.
const noFiles = cobra.ShellCompDirectiveNoFileComp

// completeTaskNames feeds tab-completion with the tasks that exist right now,
// each labelled with the repositories it spans.
func completeTaskNames(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFiles
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, noFiles
	}
	matches := task.Labels(cfg, toComplete)
	out := make([]string, 0, len(matches))
	for _, t := range matches {
		names := make([]string, 0, len(t.Repos))
		for _, r := range t.Repos {
			names = append(names, r.Name)
		}
		out = append(out, fmt.Sprintf("%s\t%s", t.Label(), strings.Join(names, ", ")))
	}
	return out, noFiles
}

// completeWorkplanNames feeds tab-completion with the workplans that exist right
// now.
//
// Unlike a task, a workplan is offered bare, with no description after the tab:
// the useful thing to say about one is how many tasks sit in each state, and
// counting those means reading every file in five directories. Completion runs on
// every tab press, so it stays with the cheap answer.
func completeWorkplanNames(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFiles
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, noFiles
	}
	return workplan.Names(cfg, toComplete), noFiles
}

// completeOneRepo completes a single repository argument.
func completeOneRepo(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFiles
	}
	return repoNames(toComplete, nil), noFiles
}

// completeRepoThenName completes `<repo> [name]`: the repository from the
// projects root, and then nothing, because the name is the user's to invent.
func completeRepoThenName(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeOneRepo(cmd, args, toComplete)
}

// completeRepoList completes a repeatable repository argument, dropping the
// ones already on the command line so a multi-repo task never offers the same
// repository twice.
func completeRepoList(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return repoNames(toComplete, args), noFiles
}

// completeRebaseArgs completes `<repo> <branch>`: a repository, and then the
// branches that repository already knows about on its remote.
func completeRebaseArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return repoNames(toComplete, nil), noFiles
	case 1:
		return remoteBranches(cmd, args[0], toComplete), noFiles
	default:
		return nil, noFiles
	}
}

// completeBaseBranch completes --base against the repository already typed.
func completeBaseBranch(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, noFiles
	}
	return remoteBranches(cmd, args[0], toComplete), noFiles
}

// completeRemotes completes --remote against the repository already typed.
func completeRemotes(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, noFiles
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, noFiles
	}
	out, err := gitx.Run(cfg.RepoPath(args[0]), "remote")
	if err != nil {
		return nil, noFiles
	}
	return withPrefix(strings.Fields(out), toComplete), noFiles
}

// completeShells completes the shells nm can generate an integration for.
func completeShells(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFiles
	}
	return withPrefix(shellint.Shells(), toComplete), noFiles
}

// completeConfigKeys completes the settings ~/.nm.json holds, each shown with
// its current value so tab-completion doubles as a way to read the file.
func completeConfigKeys(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, noFiles
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, noFiles
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return nil, noFiles
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		return nil, noFiles
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	out := make([]string, 0, len(keys))
	for _, key := range sorted(keys) {
		if !strings.HasPrefix(key, toComplete) {
			continue
		}
		value, err := settingValue(cfg, key)
		if err != nil || value == "" {
			out = append(out, key)
			continue
		}
		out = append(out, key+"\t"+value)
	}
	return out, noFiles
}

// repoNames lists the repositories under the projects root that start with
// prefix, minus the ones already given.
func repoNames(prefix string, exclude []string) []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	taken := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		taken[name] = struct{}{}
	}

	names := repos.Names(cfg, prefix)
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := taken[name]; ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

// remoteBranches lists the branches a repository knows its remote has.
//
// It reads refs that are already on disk rather than asking the remote: a tab
// press must not wait on the network, and a branch that has never been
// fetched is not one this shell has heard of yet.
func remoteBranches(cmd *cobra.Command, repo, prefix string) []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	remote := "origin"
	if cmd != nil {
		if flag := cmd.Flags().Lookup("remote"); flag != nil && flag.Value.String() != "" {
			remote = flag.Value.String()
		}
	}
	return withPrefix(gitx.RemoteBranches(cfg.RepoPath(repo), remote), prefix)
}

// withPrefix keeps the values the user could still be typing.
func withPrefix(values []string, prefix string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}
