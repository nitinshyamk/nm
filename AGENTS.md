# AGENTS.md

Instructions for AI agents working in this repository.

## What nm is

`nm` is a personal CLI for three workflows:

- **Worktrees** — throwaway git worktrees under `~/projects/worktrees`, created with
  predictable names, then listed, entered, and deleted through a TUI.
- **Tasks** — a unit of work spanning one or more repos, living in
  `~/projects/tasks/<name>-<hash>/`: one worktree per repo, `input/` holding
  `prompt.md` and any assets the task was given, `artifacts/` for uncommitted
  outputs, `scratch/` for throwaway working notes, and optionally a background
  Claude agent.
  `nm task rebase` is the one task that starts from a branch that already
  exists on the remote rather than cutting a new one.
- **Workplans** — a body of work as a set of tasks under
  `~/projects/workplans/<name>/`, moving through five states. `nm workplan execute`
  is the orchestrator: one pass, then it exits. It is also the only thing that
  launches task agents, and it launches one — running `nm-task-execute` — per task,
  which then owns that task from its definition to an approved pull request.

## Workplans

A task's state **is** which directory its definition sits in — `planned`,
`in-progress`, `review`, `approved`, `completed` — so a transition is a file move
and `ls` answers "where is everything". Nothing is cached, which is what makes
`execute` a reconciler: each pass reads the filesystem and GitHub from scratch, so
an interrupted pass is repaired by running it again rather than cleaned up after.

Seven things here are load-bearing and easy to break by accident:

- **A background agent has no terminal, so it must never stop to ask.** An
  interactive prompt leaves it waiting on stdin, which means it is no longer reading
  files, so nothing written to its escalations directory can reach it — only
  `claude attach` recovers it, and from outside it looks exactly like an agent that
  is working. Agents launch with `--permission-mode auto` for this reason;
  `bypassPermissions` has no classifier and `--dangerously-skip-permissions` is
  never used. Escalation is by file and by nothing else.
- **One agent per task, for the whole of its life.** `execute` launches an agent
  exactly once, when a task starts. That agent publishes, then blocks on
  `nm workplan await-feedback` and answers every review round in the session that
  built the work. **Step 3 must never launch anything** — it reports. Starting a
  second agent on feedback is what allowed two live agents in one worktree, because
  nothing latched the handoff: an agent that had written `ready-to-review.md` but not
  yet exited was still there when its replacement arrived, and both committed. The
  `refining.md` latch that guarded the old respawn is gone, because a trigger that
  starts no process cannot re-fire.
- **A held session is the cost, and the backoff is what makes it affordable.**
  `await-feedback` spends a `gh` call per check, unlike `await-resolution` which stats
  a local file. So it widens: 30s for 5m, 2m for 20m, 10m for 2h, then hourly — 54
  calls a day per task instead of ~2900, pinned by `TestADayOfWaitingIsCheap`.
  Statuses are cached on disk (not in memory) because the readers are separate
  processes: a waiting agent and an orchestrator pass would otherwise both pay for
  the same read.
- **An `await-feedback` timeout is resumable, not a failure.** It gives up after 12h
  so a pull request nobody reviews cannot pin an agent forever, and the skill is told
  to run it again. An agent that exits instead leaves a reviewer with nobody to answer
  them, which `execute` then reports as a dead agent.
- **Read pull requests with `--state all`, never just open.** A merged pull request
  is how a task's life normally ends; filtering to open ones made a merged task look
  like work that was never published and stuck it in `review` forever.
- **`--merge` gates nm performing a merge, not noticing one.** A pull request merged
  by hand still completes its task without the flag.
- **An agent's pull request comments are prefixed `nm-agent:`.** A reviewer weighs an
  unattended agent's "this looks fine" differently from a colleague's, and a thread
  with both in it is unreadable if nobody can tell them apart. Commit messages are
  not prefixed — those follow the repository's own convention.

The skills in `internal/skills` drive all of this and are embedded in the binary so
a test can check every command they name against the real command tree. A skill is
read by an agent working unattended, so a command that does not exist fails in the
middle of the work rather than at load time.

**Deleting a skill means adding it to `skills.Retired`, not just removing the
asset.** Install writes files and never swept, so a dropped skill stayed in
`~/.claude/skills` and stayed invocable — an older copy of instructions something
else now owns. Names stay on that list permanently; dropping one once "everyone has
upgraded" leaves the stale file forever on the machine that had not.

## Committing — the rule

After **any** change, run the gate and only commit when it passes clean:

```bash
mise run pre-commit    # format:check, lint, build, test
git add -A && git commit -m "..."
```

- Never commit with `--no-verify`, and never weaken a lint rule or delete a test to
  make the gate pass. Fix the code.
- If `format:check` fails, run `mise run format` and re-run the gate.
- The same gate runs as a git pre-commit hook (`mise run hooks` installs it) and in
  CI (`.github/workflows/ci.yml` runs `mise run ci` on Linux and Windows).
- Commit when the gate is green rather than batching many unrelated changes into
  one commit.

## Layout

```
main.go              thin entrypoint -> internal/cli
internal/cli/        cobra command definitions, one file per command group
internal/config/     ~/.nm.json load/create/defaults
internal/nmhash/     deterministic short hashes for directory and branch names
internal/gitx/       every git invocation lives here (os/exec, not go-git)
internal/repos/      what is a repository under projects_root (completion)
internal/worktree/   worktree create / discover / delete
internal/task/       task create / discover / delete, .nm-task.json, rebase
internal/workplan/   workplans: the task schema, verify, and the orchestrator
internal/skills/     the agent skills that drive workplans, embedded and installed
internal/filecopy/   copying documents between directories, never overwriting
internal/agent/      `claude` background agents: launch, status, attach
internal/forge/      the `gh` CLI: auth, find, create, and edit pull requests
internal/shellint/   shell integration scripts and the cd-file protocol
internal/tui/        bubbletea models (list, confirm dialog, prompt editor)
```

## Conventions

- Adding a command: the cobra wiring goes in `internal/cli`, the behavior goes in
  its own package with tests alongside. Keep `internal/cli` thin.
- Every command that takes an argument needs a `ValidArgsFunction`, or tab
  completion falls back to listing files — which is never what an nm argument
  wants. `TestEveryArgumentHasACompletion` in `internal/cli` enforces this; the
  completion functions themselves live in `internal/cli/complete.go` and must
  never error, never touch the network, and never write anything, because they
  run in the user's live shell on every tab press.
- Shell out to `git`, `claude`, and `gh` only from `internal/gitx`,
  `internal/agent`, and `internal/forge`. `task.Publish` takes a `Forge`
  interface so the whole pull request pipeline is tested against local
  repositories with no network and no GitHub login.
- Tests build real temporary git repositories with `t.TempDir()`; they must not
  touch `~/projects`, `~/.nm.json`, or the user's real `claude` sessions. Point
  `NM_CONFIG` at a temporary file for anything that calls `config.Load`, and
  clear `GIT_DIR` and the other plumbing variables (the `isolate` helpers do
  this) before running git in a temporary directory — the pre-commit hook
  exports them, so without that a test passes standalone and answers with this
  repository under the hook.
- Anything that rewrites shared history pushes with `--force-with-lease`, never
  `--force`. The lease is what stops a rebase from discarding a commit someone
  else pushed in the meantime, and it is the only push the rebase agent is
  allowed to make.
- A mise task's `run` is **one process invocation, never a shell script**. mise
  hands inline `run` strings to `cmd.exe` on Windows and to a POSIX shell
  everywhere else, so `$(...)`, `[ ... ]`, `||`, a pipe, or a coreutil such as
  `mkdir -p` works on one platform and breaks on the other. When a task needs
  more than one command: a computed value comes from a mise template, logic goes
  into Go and is reached with `go run .`, and a pass/fail check uses the tool's
  own flag rather than shell exit-code glue. `nm self install` exists because
  the install task used to be bash.
- Paths that came from `git`, or from a shell, cannot be string-compared against
  paths built by Go. git answers with forward slashes and long names, a shell has
  its own spelling (`/c/Users/...` under Git Bash), and `t.TempDir()` returns
  `C:\Users\NITINS~1\...` on Windows. `filepath.EvalSymlinks` collapses
  separator, 8.3 short name, and symlink at once; `samePath` in `internal/gitx`
  and `shellPWD` in `internal/shellint` are the helpers. **A bare `!=` on a path
  has been a bug every single time it appeared** — it is what made every shell
  wrapper push a duplicate directory-stack entry. In shell scripts, canonicalize
  with the shell's own tool: `cd && pwd` in bash, `path expand` in nu,
  `Get-Item .FullName` in PowerShell (`Resolve-Path` and `Convert-Path` both keep
  8.3 short names).
- Shell integration lives in `internal/shellint`, one script constant per shell,
  and each one is tested by **running that shell** rather than asserting on the
  script's text. The tests skip when the shell is absent, so the suite still
  passes without nu or PowerShell installed. A test that only parses a script
  proves nothing: the old `sh -n` check passed on a script dash could not run.
- Never guess which shell the user is in. `shellint.Detect` ranks evidence —
  process tree, then `NU_VERSION`, then `$SHELL` away from Windows — and errors
  rather than defaulting, because `nm shell setup` writes to an rc file and a
  wrong guess silently configures a shell the user is not in. `$SHELL` is not
  evidence on Windows, and `NU_VERSION` is exported to descendants so it proves
  only that a nushell is somewhere above nm.
- An rc file path is a place to be wrong quietly. nushell reads
  `%APPDATA%\nushell` on Windows rather than XDG, and PowerShell's `$PROFILE`
  follows OneDrive redirection of Documents, so both are resolved rather than
  assumed. Writing the right content to the wrong file reports success and
  changes nothing.
- `~/.nm.json` is user configuration. It is created at runtime, is never committed,
  and nothing in the repo should assume values other than the defaults in
  `internal/config`.
- `mise run install` builds and installs to `~/.local/bin/nm` — use it to try a
  change for real. Remember the shell wrapper (`nm shell init bash`) is what makes
  directory changes stick.

## Tasks

| Command | Purpose |
|---|---|
| `mise run build` | build `bin/nm` |
| `mise run run -- <args>` | run from source |
| `mise run test` | `go test ./...` |
| `mise run lint` | golangci-lint |
| `mise run format` | gofumpt write, through `golangci-lint fmt` |
| `mise run format:check` | gofumpt check, no writes (`golangci-lint fmt --diff`) |
| `mise run pre-commit` | the full gate |
| `mise run install` | build + install to `~/.local/bin` |
| `mise run setup-shell` | add shell integration to your rc file |
| `mise run hooks` | install the git pre-commit hook |

`mise run install` reads `install_dir` from `~/.nm.json` through
`nm config get`, so the install location is configuration rather than a
hardcoded path in the task.
