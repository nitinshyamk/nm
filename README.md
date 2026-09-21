# nm

A personal CLI for the two things that surround actual coding: spinning up git
worktrees, and running tasks that span several repositories.

```
nm worktree new nm fix-tui     create a worktree and cd into it
nm worktree                    list, enter, or delete worktrees
nm task new nm site -n auth -p create a task across two repos, with an agent
nm task                        see which tasks need you, and jump into one
nm task rebase nm feature/auth replay a pushed branch onto main, conflicts and all
```

## Install

```bash
mise install          # go, golangci-lint, gofumpt
mise run install      # build and install to ~/.local/bin/nm
mise run setup-shell  # add the shell integration (see below)
mise run hooks        # install the pre-commit hook (contributors only)
```

## Shell integration — required for `cd` to work

A process cannot change its parent shell's working directory. When nm picks a
worktree, the `cd` has to happen *in your shell*, so nm ships a shell function
that wraps the binary: nm writes the directory it selected to `$NM_CD_FILE`,
and the function reads it and cds after nm exits. This is the same mechanism
zoxide and direnv use.

`mise run setup-shell` (or `nm shell setup`) adds a managed block to your rc
file; re-running refreshes that block rather than adding a second one. To do it
by hand, add two lines:

```bash
# ~/.bashrc  or  ~/.zshrc
eval "$(nm shell init bash)"
source <(nm completion bash)   # tab-completion for task names
```

```nu
# nushell: def --env is what lets the cd escape the function
nm shell init nu | save --force ($nu.default-config-dir | path join nm.nu)
source ($nu.default-config-dir | path join nm.nu)
```

cobra has no nushell completion generator, so nushell gets the directory
wrapper without tab-completion.

The wrapper deliberately passes completion requests straight through to the
binary. Completion evaluates `nm __complete …` in your live shell, so without
that guard a tab press could move you.

### Moving is a push, not a jump

The wrapper moves you with `pushd`, so wherever you were stays on the
directory stack and `popd` puts you back:

```console
~/notes $ nm task select auth
~/projects/tasks/auth-9c31a0 $ popd
~/notes $
```

A jump to the directory you are already in is skipped, so repeated selections
do not pile up entries you have to pop one by one. nushell has no `pushd`
builtin; the script uses `dirs add` from `std/dirs` when that module is loaded
and plain `cd` when it is not.

### What completes

Tab-completion covers the whole tree, not just command names:

| Where | What you get |
|---|---|
| `nm <tab>` | the command groups: `worktree`, `task`, `shell`, `config` |
| `nm task <tab>` | `list`, `new`, `select`, `pr`, `rebase`, `complete`, `remove` |
| `nm task select <tab>` | the tasks that exist, labelled with the repos they span |
| `nm task new <tab>` | repositories in `projects_root`, minus the ones already typed |
| `nm worktree new <tab>` | the same repositories |
| `nm task rebase nm <tab>` | that repository's branches on origin |
| `nm config get <tab>` | every setting, with its current value |
| `nm shell init <tab>` | `bash`, `zsh`, `nu` |

Completion never touches the network and never reports an error: branch
suggestions come from refs already on disk, and anything it cannot work out
comes back empty rather than as a listing of your current directory.

Without it everything still works — nm prints the directory instead of moving
you there, and says once how to install the wrapper. Scripts that call `nm`
get the plain binary and no surprise `cd`.

## Worktrees

`nm worktree new <repo> [name]` creates
`~/projects/worktrees/<repo>-id-<name>-<hash>` on a new branch `<name>-<hash>`.

- The hash comes from the repo and the name, so the same request always names
  the same directory, and a second attempt is refused rather than duplicated.
- Without a name, today's date is used: `nm-id-2026-09-19-a3f9c2`. The clock
  goes into that hash, so several worktrees a day never collide.
- The branch is cut from **the remote's current default branch**. nm asks
  origin for its default with `ls-remote --symref` and fetches it, rather than
  trusting a local `origin/HEAD` that goes stale when the remote's default
  changes, or an `origin/main` that goes stale as soon as anyone pushes. If
  origin cannot be reached, nm stops and tells you to pass `--offline` rather
  than quietly branching from an old commit.

`nm worktree` lists them with what is uncommitted and what has never left this
machine. `enter` cds, `d` deletes. Deletion names exactly what would be lost
and opens with Cancel focused; a branch still holding commits that exist
nowhere else is kept, and nm says so.

## Tasks

A task is a unit of work spanning one or more repositories:

```
~/projects/tasks/auth-9c31a0/
├── nm-9c31a0/               worktree, branch auth-9c31a0
├── home-management-system-9c31a0/
├── input/                   what the task was given
│   └── prompt.md            the prompt the agent started from
├── artifacts/               plans and output that never get committed
├── scratch/                 throwaway working notes, mostly the agent's
└── .nm-task.json            repos, branches, base commits, the agent
```

`input/` is an organizing spot: nm writes `prompt.md` there when an agent
starts, and anything else the task should be worked from — a spec, a screenshot,
a log — can be dropped in beside it. `scratch/` is the opposite end: debugging
output and half-finished work that nothing reads back, so it never has to be
tidy and deleting the task does not warn about it. Only `artifacts/` counts as
work worth flagging before a delete.

```bash
nm task new nm home-management-system -n auth      # just the worktrees
nm task new nm home-management-system -n auth -p   # ...and an agent
```

`-p` takes no argument. It opens a full-screen prompt editor with emacs
keybindings, then starts a background claude agent in the task directory that
can reach every worktree. Cancelling leaves the task in place without an agent.
Piping stdin replaces the editor, so this is still scriptable:

```bash
echo "refactor auth across both repos" | nm task new nm site -n auth -p
```

### Prompt editor keys

| Motion | Edit | Control |
|---|---|---|
| `C-f`/`C-b` character | `C-k` kill to end of line | `C-s` submit |
| `M-f`/`M-b` word | `C-u` kill to start of line | `M-⏎` submit |
| `C-n`/`C-p` line | `C-w` kill word back | `C-g` / `esc` cancel |
| `C-a`/`C-e` line start/end | `M-d` kill word forward | `⏎` newline |
| `M-<`/`M->` buffer start/end | `C-y` yank the last kill | |

### Working on one task

Each of these takes a task name and completes it as you type; with no name,
they open the list restricted to that one action.

```bash
nm task select auth      # cd into it
nm task pr auth          # push every branch and open a PR per repo
nm task complete auth    # pr, then remove
nm task remove auth      # delete it
```

`nm task list` prints the same list `nm task` opens.

A name can be the full label (`auth-9c31a0`), the bare name (`auth`), or any
unambiguous prefix. `nm task pr`:

- refuses to start unless `gh` is logged in, so a multi-repo run never stops
  halfway through;
- offers to commit anything outstanding, per repo, taking the message in the
  same emacs editor (committing stages untracked files too, and says so);
- pushes each branch and opens a PR into the branch it was cut from, titled
  after the task, with the agent prompt and the commit list as the body;
- cross-links the PRs to each other when the task spans several repos, and
  records them in `.nm-task.json` so the list shows a `pr #42` badge;
- is safe to re-run: pushes are idempotent and an existing PR is reused.

`remove` and `complete` only stop to ask when there is something to lose —
uncommitted work, unpushed commits, files in `artifacts/`, or an agent still
working (which is stopped first). Otherwise they just do it. `-y` skips the
question.

### Rebasing a branch that is already out there

```bash
nm task rebase nm feature/auth
```

`nm task new` starts work; `nm task rebase` picks up work that already exists.
It takes exactly one repository and one branch, and that branch has to be on
the remote already — a typo becomes an error rather than a new branch.

```
~/projects/tasks/feature-auth-8d21b4/
└── nm-8d21b4/          the worktree, on branch feature/auth
```

The worktree keeps the branch's own name, because that branch is what gets
pushed back; only the task directory carries a hash. A branch with slashes in
it is flattened for the directory name and left alone for the branch.

What it does, in order:

1. checks `feature/auth` out into a task of its own, tracking `origin`;
2. runs `git pull --rebase origin <base>`, where the base is the remote's
   current default branch unless you pass `--base`;
3. if that was clean, pushes with `--force-with-lease` — a rebase rewrites
   history, so nothing else would land, and the lease is what keeps the push
   from quietly discarding a commit somebody else added while nm was working;
4. if it conflicted, leaves the rebase in progress and hands it to a
   background agent.

The agent resolves the conflicts, runs whatever verification gate the
repository documents for itself, and pushes with `--force-with-lease` only
once that passes. It is told to stop rather than guess when a conflict is
really a decision — when the two sides do different things, or the base
removed something the branch is built on. Escalating means leaving the
worktree exactly as it is, writing the choice up in
`artifacts/rebase-escalation.md`, and stopping; the task then shows up under
**Needs input** in `nm task` with an artifact waiting for you.

| Flag | Effect |
|---|---|
| `--base <branch>` | rebase onto this instead of the remote's default |
| `--remote <name>` | the remote holding the branch (default `origin`) |
| `--verify` | hold a clean replay back too, and have the agent run the checks first |
| `--no-agent` | leave the conflicts for you, with the commands to finish |
| `--no-push` | do the rebase, push nothing |

A clean replay is pushed without running anything: that is what `git pull
--rebase && git push --force-with-lease` does by hand, and nm has no idea what
your repository's checks are. `--verify` buys the agent's judgement for it —
git merging the text without complaining is not the same as the result still
working.

Your shell is left in the worktree. When you are done, `nm task remove
feature-auth` clears it away; the branch itself is on the remote and is never
deleted by nm.

### The task list

`nm task` groups tasks by what they need from you, most blocked first:

```
Needs input
▸ auth-9c31a0      agent: needs reply   ✎2 ↑1   artifacts:3
Finished
  docs-4b1e77      agent: done          clean
Working
  index-77ac10     agent: working       +1
No agent
  spike-0c2e91     no agent             clean
```

The grouping is not a guess: `claude agents --json` reports each session's
state, and `needs_approval`, `needs_reply`, and `blocked` all mean the agent
stopped and is waiting on you.

- `enter` cds into the task.
- `o` opens the task directory in your editor *and* attaches to its agent in
  this terminal; when it exits you are left in the task directory.
- `d` deletes, after listing uncommitted work in every repo and flagging a
  non-empty `artifacts/`. A running agent is stopped first.

## Configuration — `~/.nm.json`

Created with defaults on first run. Missing keys are filled in on load, and
keys nm does not recognize are left alone.

| Key | Default | Meaning |
|---|---|---|
| `projects_root` | `~/projects` | where source repositories live |
| `worktrees_root` | `~/projects/worktrees` | where worktrees are created |
| `tasks_root` | `~/projects/tasks` | where tasks are created |
| `artifacts_dir` | `artifacts` | output directory inside each task |
| `input_dir` | `input` | prompt and input assets inside each task |
| `scratch_dir` | `scratch` | throwaway working directory inside each task |
| `hash_length` | `6` | characters of hash in generated names |
| `list_rows` | `8` | entries visible in the list pane |
| `prompt_rows` | `10` | height of the prompt editor |
| `editor_command` | `code` | what `o` opens the task with |
| `claude_command` | `claude` | the agent CLI |
| `gh_command` | `gh` | the GitHub CLI used by `nm task pr` |
| `install_dir` | `~/.local/bin` | where `mise run install` puts the binary |
| `default_base_branch` | `""` | override the detected default branch |

The lists and the prompt editor draw as a pane below your command rather than
taking over the terminal, and scroll away with the rest of your scrollback;
`list_rows` and `prompt_rows` set how tall they get.

`nm config` prints the file's location and contents; `nm config get <key>`
prints one setting with `~` already expanded.

## Development

`mise run pre-commit` is the gate: format check, lint, build, test. It runs as
a git pre-commit hook and as CI. See [AGENTS.md](AGENTS.md).
