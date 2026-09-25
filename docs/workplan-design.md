# Workplan: design plan

## Summary

**Ask:** approve this design and implementation starts at task 01. Every open
question O1–O6 is resolved and recorded in §7. Nothing has been built yet.

**How it gets built (§8.1).** Tasks 01–08 are executed **by hand** in this session
and this worktree — edit, `mise run pre-commit`, commit, one commit per task. The
orchestrator is not needed to build the orchestrator. Task 09 is the first
orchestrated run, and it drives a throwaway two-task workplan (§8.2), not this plan.
The first real use of the feature is the *next* feature.

**What this delivers.** A workplan is a directory of task definitions moving
through five states, plus the machinery to drive them: six new `nm` commands
(`workplan define | add | verify | execute | resolve | await-resolution`), one
extension to task creation (`--artifacts`, `--taskfile`), and four skills
(`/nm-work-plan`, `/nm-task-execute`, `/nm-task-refine`,
`/nm-workplan-execute`). The orchestrator is a stateless reconciler: every
invocation reads the filesystem and GitHub, makes whatever transitions the rules
allow, and prints what changed. State lives in directory membership, not in a
database.

**The one structural claim worth arguing about.** Directory membership *is* the
state. A task in `review/` is in review because the file sits there. This keeps
the orchestrator restartable and inspectable with `ls`, but it means every
transition is a file move that must be atomic and idempotent, and it means two
concurrent `nm workplan execute` runs on the same workplan will race. §5.5
handles this with a lock file.

---

## 1. Scope

| ID | Deliverable | Shape |
|---|---|---|
| D1 | Workplan directory + task schema | `internal/workplan` |
| D2 | `nm workplan define` / `add` / `verify` | `internal/workplan` + `internal/cli` |
| D3 | Task creation with `--artifacts` / `--taskfile` | extends `internal/task` |
| D4 | `nm workplan execute` (the state machine) | `internal/workplan` |
| D5 | `nm workplan resolve` + `await-resolution` (§5.5.2 L2) | `internal/workplan` |
| D6 | GitHub reads the orchestrator needs (reviews, comments, merge) | extends `internal/forge` |
| D7 | Four skills, embedded in the binary + an install command | `internal/skills` |

Out of scope: any change to `nm worktree`, `nm task rebase`, or the task TUI
beyond what D3 requires.

---

## 2. Decisions already made

| # | Decision | Consequence |
|---|---|---|
| A1 | Relax `task.Create` to accept zero repositories | A 0-repo task is an ordinary `nm` task with `input/`, `artifacts/`, `scratch/`, `escalations/` and no worktrees. One code path. Forces the fix in §7 O4. |
| A2 | `nm workplan execute` merges only under `--merge` | Default run reports "approved, ready to merge" and stops. The 1-minute poller in `/nm-workplan-execute` does **not** pass `--merge`, so nothing reaches `main` without a human asking. |
| A3 | Skills are embedded in the `nm` binary, installed by a command | Same pattern `tg skill install --target anthropic` uses. Skills version with the CLI they drive, so a skill can never reference a flag the installed `nm` lacks. |

---

## 3. What the codebase already gives us

Verified by reading `C:\repos\nm` at `5ab8ce5`. These are facts, not assumptions.

- **`config.Load` self-heals.** It fills in keys missing from `~/.nm.json` and
  rewrites the file, preserving unknown keys (`internal/config/config.go:110`).
  Adding `workplans_root` needs no migration — it appears on next run.
- **`task.Create` is transactional.** Any failure unwinds every worktree already
  added and deletes the directory (`internal/task/task.go:172`). D3 must extend
  that unwind to cover copied artifacts and the taskfile.
- **`task.Create` currently requires ≥1 repo** (`task.go:143`) — the line A1
  removes.
- **`task.Options`** is `{Name, Repos, Offline, Now}`. D3 adds `Artifacts`,
  `TaskFile`.
- **`task.makeDirs`** grows missing directories on every save (`task.go:88`), so
  adding `escalations/` to it back-fills tasks created before it existed.
- **Agents launch with `claude --bg -n <name> --add-dir <dir>... -- <prompt>`**
  (`internal/agent/agent.go:172`). `nm task rebase` already generates a prompt
  programmatically and launches an agent with it (`internal/cli/rebase.go:163`)
  — that is the precedent D3 follows, including its "the task is fine, only the
  agent failed to start" error handling (`task.go` caller at `cli/task.go:119`).
- **`internal/forge`** wraps `gh` with `CheckAuth`, `Find`, `Create`,
  `EditBody`. `task.Publish` takes a `Forge` **interface** (`task/pr.go:55`)
  precisely so the pipeline is testable with no network. D6 extends that
  interface; the orchestrator must take it as an interface too, or D4 is
  untestable.
- **Verification gate is `mise run pre-commit`** = `format:check`, `lint`,
  `build`, `test`.

### 3.1 Two findings that changed the design

**F1 — `tg pr comments --json` has no timestamps.** Its JSON is
`{number, title, comments[{id, author, body}], threads}`. The Review→Approved
rule needs "comments or reviews newer than the `ready-to-review` timestamp", so
the orchestrator cannot use it. `gh` does carry them, and both are already
reachable through `internal/forge`:

| Need | Source | Field |
|---|---|---|
| Approval | `gh pr view --json reviewDecision` | `"APPROVED"` |
| Review recency | `gh pr view --json reviews` | `reviews[].submittedAt` |
| Comment recency | `gh pr view --json comments` | `comments[].createdAt` |
| Inline comment recency | `gh api repos/{owner}/{repo}/pulls/N/comments` | `created_at` |
| Mergeability | `gh pr view --json mergeable,mergeStateStatus` | `MERGEABLE` / `BLOCKED` |

So: **`gh` (via `internal/forge`) is the orchestrator's read surface; `tg pr
comments` stays the reading surface for `/nm-task-refine`**, where a human-shaped
rendering of threads is exactly what the agent wants and timestamps don't matter.
Confirmed against a live PR: `reviewDecision: "APPROVED"` with
`reviews[].submittedAt` and `comments[].createdAt` all populated.

**F2 — `reviewDecision` beats scanning reviews.** GitHub already collapses "did
an authorized reviewer approve the current state" into one field, and it resets
when new commits land. Scanning `reviews[]` for `state == "APPROVED"` would
report approval of a stale commit. Use `reviewDecision`.

---

## 4. Data model

### 4.1 Workplan on disk

```
<workplans_root>/<name>/            # C:/repos/workplans/<name>, no hash
  planned/        <id>.json
  in-progress/    <id>.json
  review/         <id>.json
  approved/       <id>.json
  completed/      <id>.json
  escalations/<id>/                 # one directory per task
      2026-05-01-08-34-02.md                 # escalation, from the task
      2026-05-01-08-34-02-resolution.md      # resolution, from the human
  artifacts/                        # only when define -a was passed
  .nm-workplan.json                 # name, created_at, schema version
  .lock                             # §5.5
```

`workplans_root` is a new config key defaulting to `~/projects/workplans`,
resolving to `C:/repos/workplans` under the existing `~/.nm.json`. Unlike tasks
there is no hash: a workplan name is chosen deliberately and typed often.

**State is directory membership.** `planned/01-foo.json` means task `01-foo` is
planned. A transition is a move. The five states, verbatim from the brief:

| State | Means |
|---|---|
| `planned` | scoped work |
| `in-progress` | actively being worked |
| `review` | passes local validation, branch exists, passes CI — needs human approval |
| `approved` | human approved, needs merging; other reviewers may still be pending |
| `completed` | merged into production |

### 4.2 Task schema

```json
{
  "id": "01-modify-contract-to-include-gdt",
  "predecessors": ["00-prepare-codebase"],
  "repositories": ["nm"],
  "description": "What to do, in prose.",
  "acceptance-criteria": ["Criterion one.", "Criterion two."]
}
```

| Field | Rule |
|---|---|
| `id` | `^[0-9]+(-[a-z0-9]+)+$` — leading number, hyphen-joined, lowercase, no spaces. Must also pass `worktree.ValidateName` (it does), because the id becomes a branch name. Lowercase-only is a choice, not a requirement: it keeps ids unambiguous on a case-insensitive filesystem where `01-Foo.json` and `01-foo.json` collide. |
| `predecessors` | list of ids. May be empty. Every entry must resolve to a task in this workplan. |
| `repositories` | list of repository names under `projects_root`. May be empty (clarification-only work — see §7 O4). |
| `description` | non-empty string. |
| `acceptance-criteria` | non-empty list of non-empty strings. An empty list is rejected: a task with no acceptance criteria has no definition of done, so `/nm-task-execute` would never terminate. |

Note `acceptance-criteria` is hyphenated in JSON and needs an explicit struct
tag; unknown fields are rejected on read so a typo'd key is an error rather
than a silently dropped criterion.

### 4.3 Task directory, once started

`nm task new <repos> -n <id> --artifacts <dir> --taskfile <file>` produces the
ordinary `nm` task directory, plus three things:

```
<tasks_root>/<id>-<hash>/
  <repo>-<hash>/          worktree per repository (none, if repositories is empty)
  input/
    prompt.md             "/nm-task-execute the task in <taskfile>"
    <id>.json             the taskfile, copied
  artifacts/              contents of the workplan's artifacts/, copied
  escalations/            created when --taskfile is passed
    <timestamp>.md
    ready-to-review.md
  scratch/
```

The task keeps its hash and so lists in `nm tasks` with no change to the TUI.
Two directional links are recorded in `.nm-task.json` so neither side has to
guess: `workplan` (name) and `task_id`. Without them the orchestrator would have
to reverse a hash to find the task directory, which it cannot.

---

## 5. CLI

### 5.1 `nm workplan define -n <name> [-a <path>]`

Creates `<workplans_root>/<name>` with the six state directories plus
`escalations/`. With `-a`, copies a file (into `artifacts/<basename>`) or a
directory's **contents** (into `artifacts/`), per the brief. Idempotent:
re-running on an existing workplan creates missing directories and fails rather
than overwriting artifacts.

### 5.2 `nm workplan add <name> --content <content>`

Validates `<content>` against §4.2, then writes `planned/<id>.json` and creates
`escalations/<id>/`. `--content` takes JSON inline or `@file` / `-` for stdin —
a task description is prose with newlines, and Windows command lines mangle
inline JSON. Refuses an id that already exists in any state directory.

### 5.3 `nm workplan verify <name>`

Three checks, all reported together rather than failing on the first:

1. **V1 schema** — every file in every state directory parses and conforms;
   filename matches the `id` inside.
2. **V2 resolution** — every predecessor resolves to a task in this workplan;
   every repository resolves to a real checkout under `projects_root`
   (`repos.IsCheckout`).
3. **V3 acyclicity** — the predecessor graph is a DAG. Report the actual cycle
   (`03 → 05 → 03`), not just "a cycle exists", or the caller cannot fix it.

Exit non-zero on any failure. `/nm-work-plan` retries against this output.

### 5.4 `nm workplan execute <name> [--merge]`

One pass, in this order. Each step is independently idempotent, so a crashed run
is fixed by running again.

**Step 1 — collect escalations.** For each task, copy new files from the task
directory's `escalations/` into `escalations/<id>/`. "New" = filename not already
present (timestamps are the filenames, so this is a set difference, not an mtime
comparison — mtime is destroyed by copying).

**Step 2 — `in-progress` → `review`.** If the task directory has
`escalations/ready-to-review.md`, move `<id>.json` to `review/`.

**Step 3 — `review` → `approved`.** For each task in `review/`, read its PR(s):
  - `reviewDecision == "APPROVED"` → move to `approved/`.
  - Otherwise, if any review or comment is newer than the timestamp inside
    `ready-to-review.md` → start a refine round, subject to the latch in §5.5.1.
  - Otherwise leave it: waiting on a reviewer.

**Step 4 — `approved` → `completed`.** Only with `--merge`. Merge the PR(s);
on success move to `completed/`. Without `--merge`, print
`approved, ready to merge: <id> (<pr-url>)` and do nothing. Merge requires
`mergeable == "MERGEABLE"`; a `BLOCKED` PR is reported, not forced.

**Step 5 — `planned` → `in-progress`.** Start eligible tasks, per the brief:

| Predecessors | Starts when |
|---|---|
| 0 | immediately |
| exactly 1 | that predecessor is at `approved` or `completed` |
| 2 or more | every predecessor is at `completed` |

The asymmetry is deliberate as I read it: a linear chain should keep moving the
moment its predecessor is approved, so the pipeline doesn't stall on merge
latency, while a fan-in has to integrate several branches and is far safer
against merged code. It has a real consequence — see §7 O1.

Starting a task = the D3 creation path, then a background agent whose prompt is
`/nm-task-execute the task in input/<id>.json`.

Step 5 runs **last** so a single pass can observe a merge and start the
successor it unblocked, rather than needing two passes.

**Output** — two sections, per the brief, machine-readable with `--json`
because `/nm-workplan-execute` parses it every minute:

```
transitions:
  01-prepare-codebase   in-progress -> review
  00-scaffold           approved -> completed  (merged #1421)
  02-add-schema         planned -> in-progress  (agent a3f9c1)

escalations:
  02-add-schema  2026-05-01-08-34-02
    <full content of the escalation file>
```

Silence when nothing changed. A poller printing "no change" 60 times an hour is
noise the agent then has to filter.

### 5.5 Concurrency

Two `execute` runs on one workplan would double-start tasks. Mitigation: an
exclusive `.lock` file (create-exclusive, PID + start time inside); a second run
exits 0 with `another execute is running (pid N)`. A lock older than 30 minutes
is stale and broken. This is the minimum needed to make a 1-minute cron safe.

### 5.5.1 The refine latch — why Step 3 needs one and Step 5 does not

Every other transition is latched by the file move itself. Step 3's refine round
is the one exception, and without a latch a 1-minute poller starts an unbounded
number of agents in the same worktree.

**Step 5 cannot duplicate.** Moving `<id>.json` out of `planned/` *is* the
transition, so the next pass does not see the task. `task.Create` independently
refuses an existing task directory (`task.go:161`), so even a crash between
launching the agent and moving the file fails loudly on the next pass rather than
starting a second agent. Two guards, no extra bookkeeping.

**Step 3 can, and does.** The task file stays in `review/` throughout a refine
round, so nothing latches. The trigger is "a comment newer than
`ready-to-review.md`", and `/nm-task-refine` only advances that timestamp when it
*finishes* — so the trigger stays true for the entire run. One poll per minute
against a 20-minute refine starts roughly 20 agents in one worktree, each
committing over the others.

This is a gap in the state machine as specified, not an implementation detail.
`In progress <--> Review` is the only two-way edge in the model, and a loop with
no latch re-fires. Review→Approved is really Review → *refining* → Review, and
the middle needs to be recorded.

**Fix: a `refining.md` marker** in the task directory's `escalations/`, holding
the launch timestamp and the agent id. Step 3 consults it before launching:

| Marker | Agent liveness | Step 3 does |
|---|---|---|
| absent | — | launch `/nm-task-refine`, write the marker |
| present | agent is live | nothing — this is the latch |
| present | agent gone, `ready-to-review.md` advanced | delete the marker; the round finished |
| present | agent gone, `ready-to-review.md` unchanged | escalate a crashed refine; do **not** relaunch silently |

A bare "last launched at T" field in `.nm-task.json` was the obvious alternative
and is worse: it suppresses the trigger forever, so a refine agent that dies
leaves the task stuck with nothing to retry it. The marker plus a liveness check
distinguishes *running* from *crashed*, and only the second case needs a human.

### 5.5.2 Why the CLI stays polled, and the agents do not

The three loops in this design look alike and are not. Each has a different event
source, and only two of the three have one worth using.

| Loop | What it waits on | Who can push it | Verdict |
|---|---|---|---|
| L1 — `nm workplan execute` every 1 min | GitHub review state | Only GitHub, via webhook → needs a public endpoint | **stays polled** |
| L2 — escalation resolution, every 2 min | A local file appearing | The local filesystem | **becomes blocking** |
| L3 — refine launch, once per `execute` pass | A local file changing | The CLI that writes it | **already edge-triggered** |

The distinction that decides each row: an **agent** polling is expensive, because
every wakeup reloads its context; a **process** polling is free. So the rule is
move the waiting out of the model loop and into a process — not eliminate polling.

**L1 stays polled, and the reason is structural, not lazy.** The only true event
source for "a human approved the PR" is a GitHub webhook, which needs a public
HTTPS endpoint, a secret, and a process alive to receive it. That is a service.
`nm` is a personal CLI with five direct dependencies and no network listener
anywhere in it; adding one to save 60 `gh` calls an hour is the wrong trade. The
honest framing: **L1 is not a wakeup loop, it is a reconciler.** Each pass is a
full read of desired-vs-actual state, which is what makes it restartable, testable
against a fake `Forge`, and safe to run after a crash. That property is worth more
than the latency. `gh` also gives a cheap improvement without changing the model —
`gh pr view --json updatedAt` short-circuits the expensive reads when a PR has not
moved since the last pass.

**L2 should be event-driven, and this is the clear win.** A `/nm-task-execute`
agent blocked on an escalation currently wakes every 2 minutes to stat a file, and
each wakeup reloads its context — that was the ~360-wakeups-overnight problem. But
the agent harness can already block on a process and notify on exit, so the skill
can wait on a command that exits when the file appears instead of polling in the
model loop:

```
nm workplan await-resolution <escalation-file> --timeout 60m
```

One blocked OS process, zero context reloads, and the agent resumes within a second
of the resolution landing rather than up to two minutes later. The agent stops
*polling*; the waiting moves into a process built to wait.

Implementation note: do this with a 1–2s stat loop inside the Go command, not
fsnotify. `go.mod` has no filesystem-watch dependency and adding one for this is
not justified — the cost being removed is the *agent's* context reload, not the
stat syscall. A blocked process spinning a cheap stat is invisible; a blocked agent
is not. `nm workplan resolve` writing the file is what ends the wait.

**L3 is already event-driven and just needs the write to be the trigger.** The
`refining.md` marker of §5.5.1 turns "launch when a comment is newer" into
"launch on the *edge* where no marker exists" — the same latch a level-triggered
interrupt needs. That is the whole fix, and it is why L3 needed no new mechanism.

**What does not change.** The transition rules, the directory-as-state model, the
`.lock`, and the output format are all untouched — L2's fix lives entirely inside
one new command plus two lines of skill text. That is the test of whether this
design was factored right, and it passes.

Liveness needs no new machinery: `agent.Client.List` and `matchSession`
(`internal/cli/task_view.go`) already resolve a recorded agent id to a live
session, falling back to matching by working directory. Reuse both.

### 5.6 `nm workplan resolve <name>`

The mirror of Step 1: copy `escalations/<id>/<timestamp>-resolution.md` files
into the task directory's `escalations/`, where a blocked
`nm workplan await-resolution` sees them within a second. Skips files already
present.

### 5.7 `nm workplan await-resolution <escalation-file> [--timeout 60m]`

Blocks until the matching `<timestamp>-resolution.md` exists beside the given
escalation, then exits 0. Exits 1 on timeout. This is the L2 fix of §5.5.2: it lets
a blocked agent wait on one OS process instead of waking every 2 minutes, which is
the difference between one blocked process and ~360 context reloads overnight.

Returns immediately when the resolution already exists, so an agent that restarts
after a resolution landed does not wait for nothing. Implemented as a 1–2s stat
loop, deliberately not fsnotify (§5.5.2).

---

## 6. Skills

All four are embedded in the binary and written to `~/.claude/skills/<name>/SKILL.md`
by `nm self install-skills` (folded into `mise run install`, so a dev build
refreshes them).

### 6.1 `/nm-work-plan` — design → workplan

1. Find the plan document being iterated on in-session — a local file, a Claude
   plan, whatever. If none, create one in the current directory.
2. Break the design into tasks written **as prose with labeled fields**, not
   JSON. Each task names its id, predecessors, repositories, description, and
   acceptance criteria explicitly.
3. Hand back to the author to review and iterate. Do not proceed unasked.
4. On agreement: ask for a workplan name, coerce it to the required format, then
   `nm workplan define -n <name>` and one `nm workplan add` per task.
5. `nm workplan verify`. Fix mechanical failures — a misnumbered id, a
   predecessor naming a task that was renamed. After 2–3 failed attempts on the
   same error, stop and escalate to the author rather than looping.

### 6.2 `/nm-task-execute <taskfile>` — do the work

1. Read everything in `artifacts/` first: it is the design context the task was
   scoped against.
2. Read the taskfile. Treat the schema as an interpretation aid — `description`
   is the intent, `acceptance-criteria` is the definition of done.
3. Work until every acceptance criterion is met.
4. **Escalate** after **three** failed attempts at the same criterion — not a
   vague "repeatedly", which an agent will read as license to keep going. Write
   `escalations/<timestamp>.md` (`YYYY-MM-DD-HH-MM-SS`, local) describing what was
   tried, what the blocker is, and what decision is needed — *and* surface it
   through the normal approval path so a human watching the session sees it. Then
   **block** on the resolution rather than polling for it (§5.5.2 L2):

   ```
   nm workplan await-resolution escalations/<timestamp>.md --timeout 60m
   ```

   Run it as a backgrounded command and let the harness notify you when it exits:
   exit 0 means the resolution file appeared and is ready to read, exit 1 means the
   timeout elapsed. Do not re-check the file on a timer in between — that is the
   thing this command exists to replace.

   On timeout, stop with the escalation left open. It is already in the workplan,
   and `nm workplan resolve` lands the answer whenever it arrives, so nothing is
   lost by exiting; a fresh `/nm-task-refine` or `/nm-task-execute` picks it up.
5. **Finish**: `mise run pre-commit` (or the repo's own gate, discovered from
   AGENTS.md / CLAUDE.md / mise.toml / Makefile / package.json) → `tg commit new`
   → `tg pr new` → confirm checks pass → write `escalations/ready-to-review.md`
   containing the timestamp → file a "ready for review" escalation. Then stop.

Never weaken a test or lint rule to get a green gate — the existing convention in
`AGENTS.md` and the rebase prompt, restated here because this agent runs
unattended.

### 6.3 `/nm-task-refine` — action review feedback

1. Find the PR for the current branch, run `tg pr comments <n>`.
2. Address every comment that needs action.
3. Where a thread's resolution is genuinely unclear, escalate it with the §6.2
   mechanism rather than guessing.
4. Re-run the gate, push, then rewrite `ready-to-review.md` with a **newer**
   timestamp. That single write is what tells the orchestrator the round trip is
   done: Step 3's "newer than `ready-to-review`" comparison goes quiet.

**The one rule that governs step 4 (O6).** Writing `ready-to-review.md` is an
assertion — "every comment is addressed" — not housekeeping. It releases the
§5.5.1 latch, so:

- Write it once every comment is dealt with, **including** a comment the agent
  decided needed no code change. Deciding is enough; changing is not required.
- **Do not write it** for feedback the agent cannot address or cannot understand.
  Escalate that instead (§6.2 mechanism) and leave the timestamp alone, so the task
  stays in `review/` with the trigger live — which is accurate, because it still
  needs a human.

Never advance the timestamp to get unstuck. An agent that cannot understand
feedback and writes the file anyway leaves the task looking refined while the
reviewer's comment stands unanswered, which is the one outcome this whole loop
exists to prevent.

### 6.4 `/nm-workplan-execute` — drive the workplan

1. Read the workplan's current structure and the CLI behavior above.
2. Take the workplan name as input. If invoked from inside a workplan directory,
   confirm that one rather than assuming.
3. Run `nm workplan execute <name>` every minute in the background. Report new
   transitions and new escalations; solicit input on the escalations. The command
   is silent when nothing changed, so a quiet minute costs one process and produces
   nothing to read — this is the one loop that stays polled, deliberately (§5.5.2
   L1).
4. Write the human's answer to `escalations/<id>/<timestamp>-resolution.md`,
   matching the escalation's timestamp.
5. `nm workplan resolve <name>` to push resolutions down to the task directories.

---

## 7. Decisions on the open questions

O1–O5 are **resolved** (answered 2026-09-23) and recorded here with the reasoning
that produced them. O6 is new, and is the only thing still open.

**O1 — What does a successor branch from, when its predecessor is approved but
not merged? → Stacked branches.** Step 5 starts a single-predecessor task as soon
as its predecessor is `approved`, but `task.Create` branches from `origin`'s
default branch (`gitx.ResolveBase`). So `02` would start from a `main` that does
not contain `01`, and duplicate or conflict with it. The options weighed:

| Option | Cost | |
|---|---|---|
| **Branch the successor from the predecessor's branch (stacked PRs)** | `tg pr new` defaults its base to origin's default branch, so the base must be passed explicitly; a rebase of `01` orphans `02` | **chosen** |
| Require `completed` for all predecessors, dropping the 1-vs-2+ distinction | Always correct; costs the pipeline latency the asymmetry buys | |
| Start from `main`, rebase the successor once the predecessor merges | Keeps the latency win, adds a rebase the orchestrator must drive | |

Stacking is the only option that both preserves the rule as specified and produces
a correct tree. Consequences for implementation: `task.Options` needs an explicit
base (branch **and** commit, since the predecessor's branch is the base), Step 5
must look up the predecessor's branch from its task record, and `/nm-task-execute`
must pass `tg pr new --base <predecessor-branch>` rather than letting it default.
The orphaning case — `01` rebased after `02` stacked on it — is a known sharp edge
left unhandled in v1; it surfaces as a conflict at merge rather than silently.

**O2 — Multi-repo tasks have N PRs, not one. → All-of-N.** `task.Publish` opens
one PR per repository and cross-links them, so "the PR has been approved" means
every one approved, and `--merge` means N merges that can partly fail. On a
partial merge the task stays in `approved` and the output names which repos
merged; a re-run finishes the rest.

**O3 — Sequencing `tg commit new` and `tg pr new`. → Stage, commit, verify clean,
then open.** `tg commit new` needs staged changes; `tg pr new` errors if staged
changes exist *or* if an open PR already exists for the head/base pair. So §6.2
step 5 stages → commits → confirms the tree is clean → opens the PR, and
`/nm-task-refine` never calls `tg pr new` again. The PR body is therefore
generated from the commits rather than from the taskfile.

**O4 — A 0-repo task has no PR, so it cannot reach `approved` by review.**
Consequence of A1. A 0-repo task writes `ready-to-review.md` when its acceptance
criteria are met (moving it to `review/`), and reaches `approved` only by explicit
human resolution in `escalations/<id>/`, skipping `--merge` and moving straight to
`completed`.

**O5 — Two inconsistencies in the brief, resolved as follows.**
  - The resolution path was given as the *same* path as the escalation
    (`escalations/01-prepare-codebase/2026-05-01-08-34-02.md`) in step 4 but as
    `timestamp_resolution` in step 5. Standardizing on
    `<timestamp>-resolution.md`, which keeps escalation and resolution adjacent
    when sorted and never overwrites the escalation.
  - `nm workplan execute task <name>` appears once against `nm workplan execute
    <name>` elsewhere. Standardizing on `nm workplan execute <name>`.

**O6 — Should `/nm-task-refine` advance `ready-to-review.md` when it decides no
comment needed action? → Always advance, but only on a decision.** Tracking
dismissed comment ids is deferred past v1.

The rule has two halves, and the second is what makes the first safe:

- **Advance** `ready-to-review.md` when the agent has decided every comment is
  addressed — including deciding a comment needed no code change. This clears the
  §5.5.1 latch and is the normal exit.
- **Escalate, and do not advance,** when the agent cannot address a comment or
  cannot tell what it asks for. The task stays in `review/` with the trigger still
  live, which is correct: it genuinely still needs a human.

So the file is never written as housekeeping — it is written only as the assertion
"I have dealt with all of this." The failure mode it forecloses is an agent that
cannot understand feedback, advances the timestamp anyway, and leaves the task
looking refined while the reviewer's comment stands unanswered. The two halves are
exhaustive: every comment ends in addressed-or-escalated, so the latch always
clears one way or the other.

Known cost, accepted for v1: the orchestrator cannot distinguish "refined and
pushed" from "read and dismissed". A reviewer whose comment was judged
non-actionable gets no signal beyond the absence of a change. Recording dismissed
comment ids (`tg pr comments` exposes stable ids) fixes it later without changing
this contract.

---

## 8. Implementation tasks

Written in the §4.2 schema's fields, in the prose form `/nm-work-plan` step 2
calls for. §8.1 below says concretely who executes them and when the tooling takes
over from hand execution.

**01-workplan-config-and-layout**
- *predecessors*: none
- *repositories*: `nm`
- *description*: Add `workplans_root` to `internal/config` (default
  `~/projects/workplans`) with a `Workplans()` accessor beside `Tasks()`. Create
  `internal/workplan` holding the directory layout: the six state directories,
  `escalations/`, `artifacts/`, `.nm-workplan.json`, and the path helpers the
  rest of the feature calls.
- *acceptance criteria*: `config.Load` adds `workplans_root` to an existing
  `~/.nm.json` without disturbing other keys, covered by a test. State directory
  names are constants, not string literals at call sites. `mise run pre-commit`
  passes.

**02-task-schema-and-verify**
- *predecessors*: `01-workplan-config-and-layout`
- *repositories*: `nm`
- *description*: Implement the §4.2 task type with strict JSON decoding, the id
  regex, and the three verification checks V1–V3, reporting all failures at once
  and naming the actual cycle.
- *acceptance criteria*: Round-trips a task through JSON with the hyphenated
  `acceptance-criteria` key. Rejects: a bad id, an empty
  `acceptance-criteria`, an unknown field, a predecessor naming nothing, a
  repository with no checkout. Detects a 2-cycle, a 3-cycle, a self-cycle, and a single n-cycle when graph is n-nodes,
  and names the cycle's members. A valid multi-task graph verifies clean.

**03-define-add-verify-commands**
- *predecessors*: `02-task-schema-and-verify`
- *repositories*: `nm`
- *description*: Wire `nm workplan define -n <name> [-a <path>]`,
  `nm workplan add <name> --content <content>` (inline, `@file`, `-`), and
  `nm workplan verify <name>` as cobra commands, keeping `internal/cli` thin per
  `AGENTS.md`. Every positional argument gets a `ValidArgsFunction` — workplan
  names for `add` and `verify` — or `TestEveryArgumentHasACompletion` fails.
- *acceptance criteria*: `define` creates all seven directories; with `-a` on a
  file copies the file, on a directory copies its contents. `add` writes
  `planned/<id>.json` and `escalations/<id>/`, and refuses a duplicate id or
  content failing V1. `verify` exits non-zero and names every problem.
  Completion functions never error, never touch the network, never write.

**04-task-creation-with-taskfile**
- *predecessors*: `03-define-add-verify-commands`
- *repositories*: `nm`
- *description*: Extend `task.Options` with `Artifacts`, `TaskFile`, and an
  explicit `Base` (branch **and** commit, for the stacking of O1), and allow an
  empty repository list (A1). Copy artifacts into the task's `artifacts/`, copy
  the taskfile into `input/`, create `escalations/` when a taskfile is given,
  generate the prompt `/nm-task-execute the task in input/<id>.json`, and record
  `workplan` and `task_id` in `.nm-task.json`. Extend the existing rollback to
  remove copied files on failure. Expose the flags on `nm task new`.
- *acceptance criteria*: A 0-repo task creates cleanly with no worktrees and
  appears in `nm task list`. A task with a taskfile has the copied artifacts, the
  copied taskfile, `escalations/`, and the generated prompt in `input/prompt.md`.
  An explicit `Base` branches from that commit rather than from origin's default
  branch, covered by a test asserting the new branch's merge base. A failure
  partway leaves nothing behind. Existing `nm task new` behavior is unchanged with
  none of the new flags.

**05-forge-review-reads**
- *predecessors*: `01-workplan-config-and-layout`
- *repositories*: `nm`
- *description*: Add to `internal/forge` the reads in §3.1 F1 —
  `reviewDecision`, `reviews[].submittedAt`, `comments[].createdAt`,
  `mergeable`/`mergeStateStatus` — and a `Merge`. Extend the `Forge` interface so
  the orchestrator is testable with a fake, as `task.Publish` already is.
- *acceptance criteria*: Parsers are unit-tested against captured `gh` JSON
  including a PR with no reviews and one with a mix of `COMMENTED` and
  `APPROVED`. `gh` is invoked only from `internal/forge`. No test needs a network
  or a GitHub login.

**06-execute-state-machine**
- *predecessors*: `04-task-creation-with-taskfile`, `05-forge-review-reads`
- *repositories*: `nm`
- *description*: Implement the five ordered steps of §5.4 over an injected
  `Forge`, clock, and agent launcher, with the `.lock` of §5.5, the `refining.md`
  latch of §5.5.1, `--merge` gating the last transition, and the two-section
  output plus `--json`. Step 5 resolves each predecessor's branch and passes it as
  the successor's base (O1).
- *acceptance criteria*: Each transition rule is tested at its boundary —
  including one-predecessor-approved and two-predecessors-where-one-is-only-
  approved. **A task in `review/` with actionable feedback launches exactly one
  refine agent across ten consecutive passes**, and a pass whose marker names a
  dead agent with an unadvanced `ready-to-review.md` escalates instead of
  relaunching. A successor's worktree branches from its predecessor's branch.
  Escalation collection copies only unseen filenames. A second `execute` while one
  holds the lock exits 0 without acting. A run with no changes prints nothing.
  Without `--merge` nothing is ever merged. Running the same pass twice produces no
  duplicate transitions and no duplicate agents.

**07-resolve-command**
- *predecessors*: `06-execute-state-machine`
- *repositories*: `nm`
- *description*: `nm workplan resolve <name>`, copying
  `escalations/<id>/<timestamp>-resolution.md` down into each task directory's
  `escalations/`, skipping what is already there. Plus
  `nm workplan await-resolution <escalation-file> --timeout <d>` (§5.7), the
  blocking wait that replaces the agent's 2-minute poll.
- *acceptance criteria*: A resolution reaches the right task directory; a second
  run copies nothing; a resolution for an unknown id is reported, not silently
  dropped. `await-resolution` returns 0 immediately when the resolution already
  exists, returns 0 within ~2s of a resolution appearing while it waits, and
  returns 1 on timeout — all three covered by tests using a short timeout and no
  real sleeping in the test body.

**08-skills-and-install**
- *predecessors*: `06-execute-state-machine`
- *repositories*: `nm`
- *description*: Author the four SKILL.md files of §6, embed them with
  `go:embed`, and add `nm self install-skills` writing
  `~/.claude/skills/<name>/SKILL.md` — identical content is left alone, a
  conflict fails without `--force`, symlinked destinations are refused. Fold it
  into `mise run install`.
- *acceptance criteria*: Every `nm` command and flag a skill names exists in this
  build, enforced by a test that greps the embedded text against the command
  tree — this is the check that stops skills drifting from the CLI. Install is
  idempotent. A conflicting existing file fails without `--force` and is
  replaced with it. The mise task stays a single process invocation.

**09-end-to-end-dogfood**
- *predecessors*: `07-resolve-command`, `08-skills-and-install`
- *repositories*: `nm`
- *description*: Drive the throwaway two-task workplan of §8.2 through all five
  states using only the documented commands and skills — the first orchestrated run
  of the feature. Then record where the model broke and update `README.md` and
  `AGENTS.md` to match what shipped.
- *acceptance criteria*: Both tasks reach `completed/`. Every transition appeared in
  `execute` output. The induced escalation was raised, answered via
  `escalations/<id>/`, and picked up by a blocked `await-resolution`. `02` branched
  from `01`'s branch, not from `main` (O1). A `review/` task with actionable feedback
  launched exactly one refine agent over ten consecutive passes. `README.md`
  documents all six commands and `AGENTS.md` describes `internal/workplan` alongside
  the existing packages.

Critical path: 01 → 02 → 03 → 04 → 06 → (07, 08) → 09, with 05 joining at 06.
Tasks 05 and 03/04 are the only real parallelism.

### 8.1 Bootstrap: who executes these, and when the tooling takes over

The circularity is real — tasks 01–08 build the thing that runs tasks. It resolves
because **the orchestrator is not needed to build the orchestrator**; it is needed
only to build things *after* it. So:

> **Tasks 01–08 are executed by hand, in this session, in one worktree.
> Task 09 is the first thing the tooling runs, and the workplan it runs is a
> throwaway — not this one.**

That is the whole answer. The rest of this section is the detail.

**Bootstrap anchor (verified, not assumed).** `~/.local/bin/nm.exe` reports version
`5ab8ce5`; this worktree's HEAD is `5ab8ce5` with a clean tree. Installed binary and
source are identical, so `mise run install` is a safe way to get new subcommands on
PATH mid-session, and `nm.exe.old` beside it is the rollback. Nothing in the
existing `nm` breaks while `internal/workplan` is being added, because every task
below is additive.

**Phase 1 — tasks 01–08, executed by hand (this session, this worktree).**
No workplan directory, no `nm workplan` commands, no background agents. The
existing loop is sufficient and already proven: edit → `mise run pre-commit` →
commit. One commit per task, per `AGENTS.md`'s "commit when the gate is green rather
than batching". Task 09 is what changes that.

The order is the critical path, and it is ordered by *testability*, not by
dependency alone:

| # | Task | Verified by, at the end of the task |
|---|---|---|
| 01 | config + layout | `mise run install`, then `nm workplan define -n scratch-01` creates seven directories |
| 02 | schema + verify | unit tests only; nothing user-visible yet |
| 03 | define/add/verify | by hand: define a 3-task workplan, induce a cycle, confirm `verify` names it |
| 05 | forge reads | unit tests against captured `gh` JSON; plus one read of a real PR |
| 04 | task creation | `nm task new --taskfile` on a throwaway task; inspect the directory |
| 06 | execute | unit tests for every transition, then one dry run on the §8.2 workplan |
| 07 | resolve + await | `await-resolution` in one terminal, `resolve` in another |
| 08 | skills | `nm self install-skills`, then invoke `/nm-work-plan` on a trivial design |

01 → 02 → 03 comes first because **03 is the first point where anything is
inspectable by hand.** 05 slots after 03 rather than first, despite having no
dependency on it, because until 03 exists there is no workplan to read PRs *for*,
and unit tests against captured JSON are equally valid at any point. 04 waits for 03
because its taskfile input is 03's output.

**Phase 2 — task 09, the first orchestrated run.** By 08 the tooling exists and is
hand-verified. 09 is where it drives something for real, and it must **not** be this
plan: 01–08 are already done by then, so "running" them would be a no-op that proves
nothing. 09 uses a purpose-built throwaway workplan instead — see §8.2.

**Where the first real use is.** The first genuine use of this feature is the *next*
feature, not this one. `/nm-work-plan` gets pointed at a fresh design, and 01–08
never run under orchestration at all. That is the honest version of "building
itself": this feature is hand-built, and the thing it builds is whatever comes next.

### 8.2 The task-09 verification workplan

Task 09 needs a workplan that exercises every transition while being small enough
that a failure is obvious. Concretely, two tasks in `nm` itself:

- **`01-add-workplan-doc-stub`** — no predecessors, one repository. Add a short
  `## Workplans` section to `README.md`. Acceptance: the section exists and
  `mise run pre-commit` passes.
- **`02-extend-workplan-doc`** — predecessor `01`, one repository. Extend that
  section with the command table. Acceptance: every §5 command is listed and the
  gate passes.

Two tasks with one dependency is the minimum that exercises the interesting paths:
0-predecessor start, single-predecessor start from `approved` (the O1 stacking —
`02` must branch from `01`'s branch), the refine latch, and the merge gate. Both are
documentation edits, so a broken agent produces a bad README rather than broken code,
and both PRs are small enough to review in a minute.

One step is deliberately manual: **the escalation round-trip is induced, not waited
for.** Task 09's acceptance requires an escalation raised, answered, and picked up,
and rather than hope an agent gets stuck, `02`'s description will contain a genuine
ambiguity ("list the commands in the order you think best, and escalate if unsure
which ordering the author wants"). That makes the round-trip deterministic and
schedulable instead of incidental.

**Rollback.** If 09 goes wrong, the damage is bounded by construction: two
documentation PRs (closeable), one workplan directory under `C:/repos/workplans`
(deletable), and up to two task directories (`nm task rm`). Nothing touches `main`
without `--merge`, per A2.

**Exit criteria before this is used on anything real.** All of: both tasks reached
`completed/`; every transition appeared in `execute` output; the induced escalation
round-tripped; and — the one I most expect to fail — a `review/` task with actionable
feedback launched exactly **one** refine agent over ten consecutive `execute` passes
(§5.5.1). If that last one fails, the latch is wrong and no further orchestrated run
happens until it is fixed.

---

## 8.3 What the first orchestrated run found

Task 09 ran on 2026-09-24 against the §8.2 workplan. Both tasks went through the
pipeline; `01` is in `completed/` and `02` is in `review/` awaiting a reviewer. It
found three defects, and none of them were things the unit tests could have caught,
because all three were about the gap between the code and the world it runs in.

**F3 — an unattended agent deadlocked on a permission prompt.** The very first
agent stopped on its second action: `cd <worktree> && git status` triggered a
confirmation dialog, with no terminal attached to answer it. It sat in
`state=blocked` having done nothing.

The consequence is worse than a stall and is the reason this got a structural fix
rather than a retry. **An interactive prompt is a one-way door.** A blocked agent is
waiting on stdin, so it is no longer reading files — which means a resolution
written to its escalations directory can never reach it. Nothing nm does recovers
the task; only a human running `claude attach` can. And from the outside it is
indistinguishable from an agent that is working, so the workplan stops silently.

Three independent causes, all fixed:

| Cause | Fix |
|---|---|
| Agents launched in a mode a dialog can stop | `--permission-mode auto`. Verified both ways before choosing it: it runs the exact `cd && git status` chain with no prompt, and its classifier refuses `rm -rf / --no-preserve-root`. Never `bypassPermissions`, which has no classifier, and never `--dangerously-skip-permissions`. |
| The orchestrator could not see the state | `execute` now reports a task whose agent stopped without escalating or finishing, and names the `claude attach` command, since nm cannot fix it |
| The skills permitted it by instruction | Escalation is file-only. The earlier wording said to write a file *and* surface it the normal way; the two channels are not additive, and the interactive one destroys the file one. Pinned by a test that also rejects the old phrasing. |

**F4 — a merged pull request left its task stuck forever.** `StatusForBranch` went
through `Find`, which hardcodes `--state open`. Once a pull request merges, `Find`
answers nil, so the orchestrator saw no pull request, could not decide whether the
work was approved, and parked the task in `review/` reporting `0 of 1 open`
permanently.

Merging is how a task's life normally ends, and merging *without* a separate
approval is how a solo maintainer works — so the broken path was the common one.
Note the shape: `allApproved` had accepted a merged pull request since it was
written. The intent was right and only the read was wrong, which is a condition
guarding against something the layer below could never report.

Two consequences follow. A task whose pull request is already merged completes
without `--merge`, because that flag gates nm *performing* a merge and cannot gate
observing one somebody else did. And the transition note says `already merged`
rather than `approved`, so the output never claims an approval nobody gave.

**F5 — the acceptance criteria were written against the wrong code state, and the
agent caught it.** Task `02`'s criterion named seven subcommands including
`await-resolution`. The agent escalated rather than guessing, with evidence:
`git grep await-resolution origin/main -- internal/` finds nothing, and
`git log --all -S"await-resolution"` finds it only in a commit reachable from the
feature branch. Verified independently before answering — it was correct.

The criterion had been written against the *installed binary*, built from the
in-flight branch, rather than against what task `02`'s base branch would contain.
Two different code states, and nobody checked which one the agent would get. That
is a specification defect, not an ambiguity the agent should have resolved.

The lesson generalizes beyond this run: **a task's acceptance criteria have to be
true of the base its agent will branch from, not of the machine the author is
sitting at.** A criterion naming a command, flag, or file is a claim about a
specific commit.

It also refused the tempting fix — implementing the missing command itself — on the
grounds that it belonged to another task and would put Go code inside a pull
request scoped as a README edit. That was the right call.

**What the run verified in production:** every transition including the merged-PR
path; automatic successor start in the same pass as its predecessor's completion;
correct base selection (task `02` branched from `main`, not stacked, because `01`
was completed rather than merely approved); escalation surfacing with full content
and no repetition on later passes; and the whole escalation → answer → `resolve` →
resume round trip.

**Still unverified:** the refine latch against a live reviewer. The
one-agent-across-ten-passes property is unit-tested but has not met a real comment.

---

## 9. Risks

| Risk | Why it matters | Mitigation |
|---|---|---|
| Step 3 starting a refine agent every minute for the length of a refine round | ~20 agents committing over each other in one worktree. The one genuinely unlatched transition | The `refining.md` marker of §5.5.1, plus a liveness check to tell *running* from *crashed* |
| `/nm-task-execute` looping on an unmeetable criterion | Burns tokens indefinitely, unattended | Escalate after 3 failed attempts at the same criterion (§6.2) |
| An unanswered escalation polling forever | ~360 context reloads overnight for an idle agent | `nm workplan await-resolution` blocks one process instead (§5.5.2 L2), with a 60m timeout after which the agent exits leaving the escalation open |
| `await-resolution` blocking forever if the timeout is dropped | An agent pinned on a dead wait, invisible in `nm tasks` | `--timeout` has a default rather than meaning "forever"; exit 1 is a normal outcome the skill handles |
| `ready-to-review.md` as the sole In-progress→Review signal | An agent that dies after writing it but before the PR is green moves a broken task to review | Step 2 could also require an open PR; deferred, since a spurious `review/` is visible and cheap to undo |
| Path comparison bugs on Windows | `AGENTS.md` records this as having been a bug "every single time" | Use `filepath.EvalSymlinks` / the existing `samePath` helper, never bare `!=` |
