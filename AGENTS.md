# AGENTS.md

Instructions for AI agents working in this repository.

## What nm is

`nm` is a personal CLI for two workflows:

- **Worktrees** — throwaway git worktrees under `~/projects/worktrees`, created with
  predictable names, then listed, entered, and deleted through a TUI.
- **Tasks** — a unit of work spanning one or more repos, living in
  `~/projects/tasks/<name>-<hash>/`: one worktree per repo, an `artifacts/`
  directory for uncommitted outputs, and optionally a background Claude agent.
  `nm task rebase` is the one task that starts from a branch that already
  exists on the remote rather than cutting a new one.

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
  CI (`.github/workflows/ci.yml` runs `mise run ci`).
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
| `mise run format` | gofumpt write |
| `mise run format:check` | gofumpt check (no writes) |
| `mise run pre-commit` | the full gate |
| `mise run install` | build + install to `~/.local/bin` |
| `mise run setup-shell` | add shell integration to your rc file |
| `mise run hooks` | install the git pre-commit hook |

`mise run install` reads `install_dir` from `~/.nm.json` through
`nm config get`, so the install location is configuration rather than a
hardcoded path in the task.
