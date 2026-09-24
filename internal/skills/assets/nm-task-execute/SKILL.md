---
name: nm-task-execute
description: Execute one workplan task to its acceptance criteria, escalating when stuck and publishing a pull request when done. Use when started in a task directory with a task definition in input/, normally by nm workplan execute rather than by hand.
---

# Execute one workplan task

You are in a task directory. It holds a definition in `input/<id>.json`, the
design context in `artifacts/`, one worktree per repository, and `escalations/`
for anything you need a human to decide.

You are running unattended. Nobody is watching this session and there is no
terminal attached to it.

**This means you must never stop to ask a question.** Not as well as writing a
file — not at all. An interactive question puts you in a state waiting on input
that nobody can give: you are no longer reading files, so the answer mechanism
cannot reach you, and only a human running `claude attach` can get you out. From
the outside your task looks exactly like one being worked on, so it stalls
silently and indefinitely.

Everything you need to say goes in a file, described under **Escalate** below.
That is the only channel that works in both directions.

## 1. Read the context first

Read **everything** in `artifacts/` before the task definition. That is the design
the task was scoped against, and the definition is a summary of one slice of it.
Starting from the summary is how you rebuild something the design already decided
differently.

Then read `input/<id>.json`:

- `description` is the intent.
- `acceptance-criteria` is the definition of done — the whole of it.
- `repositories` names the worktrees you may touch. Touch nothing else.

## 2. Do the work

Work in the worktrees in this task directory. Follow the repository's own
conventions: read `AGENTS.md`, `CLAUDE.md`, and the code around what you are
changing before writing anything.

Work until **every** acceptance criterion is met. Not most of them.

## 3. Escalate rather than guess

After **three** failed attempts at the same criterion, escalate. Three, not
"repeatedly" — a vague bound reads as license to keep going.

Escalate immediately, without spending the three attempts, when the blocker is a
decision rather than a difficulty: the design is ambiguous about what the code
should do, two reasonable readings give different behaviour, or meeting one
criterion would break another.

To escalate, write `escalations/<timestamp>.md`, naming the file for the moment
you wrote it in `YYYY-MM-DD-HH-MM-SS` form:

```markdown
# What is blocking 01-prepare-codebase

What I was trying to do, and what happened the three times I tried.

## The decision I need

The two options, and what each would mean. Name the one you would pick and why,
so the answer can be "yes" rather than an essay.
```

Write the file, and **do not also ask interactively**. The orchestrator collects
escalations on its next pass and puts them in front of a human; a dialog only
deadlocks you.

Then **block** on the answer rather than polling for it:

```sh
nm workplan await-resolution escalations/<timestamp>.md --timeout 60m
```

Run that as a backgrounded command and let the harness notify you when it exits.
Exit 0 means `escalations/<timestamp>-resolution.md` is there to read; exit 1
means the timeout elapsed. Do not check the file on a timer in between — that is
exactly what this command replaces, and a session that wakes every two minutes
reloads its whole context each time.

On timeout, stop and leave the escalation open. It is already in the workplan, and
`nm workplan resolve` delivers the answer whenever it arrives.

## 4. Publish

Once every criterion is met, in this order. The order matters — `tg pr new` errors
if staged changes exist, so the tree has to be clean before it runs.

1. **Run the gate.** Find it in `AGENTS.md`, `CLAUDE.md`, `mise.toml`, `Makefile`,
   or `package.json` — usually `mise run pre-commit`. Fix what it reports.
   **Never weaken a test or a lint rule to get a green result.** If the gate
   cannot pass without changing what the task set out to do, that is an
   escalation, not a judgement call.
2. **Commit** with `tg commit new`, once per repository with changes.
3. **Confirm the tree is clean** — `git status` — before the next step.
4. **Open the pull request** with `tg pr new`. If this task has a predecessor
   whose branch is not yet merged, pass `--base <predecessor-branch>`: the
   default base does not contain the work this task was built on.
5. **Wait for checks to pass.** A red pull request is not ready for review. Fix
   what CI reports and push again.

## 5. Say it is ready

Only once the pull request is open and green:

1. Write `escalations/ready-to-review.md` containing the current timestamp:

   ```markdown
   # Ready for review

   2026-05-01T08:34:02Z
   ```

2. Write an escalation file saying the task is ready for review and naming the
   pull request, so it reaches whoever is watching the workplan.

`ready-to-review.md` is what moves the task into review, and its timestamp is the
line between work you published and feedback that arrives afterwards. Write it
last, and only once, or the orchestrator will read a time that does not mean what
it says.

Then you are done. Stop.

## Never

- **Stop to ask a question.** There is no terminal. Write an escalation file
  instead — an interactive prompt is how this task becomes unrecoverable.
- Touch a repository the definition does not name.
- Weaken a test, a lint rule, or an acceptance criterion to finish.
- Write `ready-to-review.md` before the pull request is open and green.
- Force push, or push to `main`.
