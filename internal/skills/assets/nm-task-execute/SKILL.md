---
name: nm-task-execute
description: Drive one workplan task from its definition to an approved pull request — do the work, publish it, then action review feedback round after round until nobody asks for anything else. Use when started in a task directory with a task definition in input/, normally by nm workplan execute rather than by hand.
---

# Execute one workplan task

You are in a task directory. It holds a definition in `input/<id>.json`, the
design context in `artifacts/`, one worktree per repository, and `escalations/`
for anything you need a human to decide.

Your job is the whole task, not one pass at it: build the work, publish it, and
then keep answering reviewers until the pull request is approved. Those are the
same job, and this skill runs both — you may be started at the beginning of it or
partway through, so **§1 works out where you are** rather than assuming.

You are running unattended. Nobody is watching this session and there is no
terminal attached to it.

**This means you must never stop to ask a question.** Not as well as writing a
file — not at all. An interactive question puts you in a state waiting on input
that nobody can give: you are no longer reading files, so the answer mechanism
cannot reach you, and only a human running `claude attach` can get you out. From
the outside your task looks exactly like one being worked on, so it stalls
silently and indefinitely.

Everything you need to say goes in a file, described under **Escalating** below.
That is the only channel that works in both directions, and it is the only one
this skill has — every step below escalates the same way.

## 1. Read the context, then work out where you are

Read **everything** in `artifacts/` before the task definition. That is the design
the task was scoped against, and the definition is a summary of one slice of it.
Starting from the summary is how you rebuild something the design already decided
differently.

Then read `input/<id>.json`:

- `description` is the intent.
- `acceptance-criteria` is the definition of done — the whole of it.
- `repositories` names the worktrees you may touch. Touch nothing else.

Now find out which part of the task is still open, by looking rather than
guessing. You are started the same way in both cases, so the directory is what
tells them apart:

| What you find | Where you are | Go to |
|---|---|---|
| No `escalations/ready-to-review.md`, no pull request | the work is not published yet | **§2** |
| `ready-to-review.md` and an open pull request | it is in review, and a reviewer has spoken | **§5** |
| An `escalations/<timestamp>-resolution.md` you have not acted on | a question you asked has been answered | read it, then rejoin at §2 or §5 |

Two readings of that table to get right:

- **Committed work but no `ready-to-review.md` means §2, not §5.** A previous
  session was interrupted partway. Check the acceptance criteria against what is
  already there, finish what is left, and publish — do not start over, and do not
  skip to review because a branch has commits on it.
- **A pull request open with no feedback newer than `ready-to-review.md` needs
  nothing from you.** The orchestrator only starts you when there is something to
  do, so if you genuinely find nothing, say so in an escalation rather than
  inventing work.

## 2. Do the work

Work in the worktrees in this task directory. Follow the repository's own
conventions: read `AGENTS.md`, `CLAUDE.md`, and the code around what you are
changing before writing anything.

Work until **every** acceptance criterion is met. Not most of them.

After **three** failed attempts at the same criterion, escalate. Three, not
"repeatedly" — a vague bound reads as license to keep going.

## 3. Publish

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

## 4. Say it is ready

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
last, and only once per round, or the orchestrator will read a time that does not
mean what it says.

Then stop. The task is in review now, and the loop in §5 is how it leaves.

## 5. The review loop

A task in review goes round this loop once per batch of feedback. You run one pass
of it: deal with everything a reviewer said, advance the timestamp, and stop. If
they say something else afterwards, you are started again for the next pass.

The reviewer is the authority on what they want here — their comment is not a
suggestion to weigh against your own reading of the code.

### 5.1 Read the feedback

Find the pull request number, then:

```sh
tg pr comments <number>
```

It prints comments on the pull request as a whole first, then each review thread
under its own heading. Read every thread to its end — a thread often resolves
itself two replies down, and actioning the first comment in it would undo that.

### 5.2 Action what needs actioning

For each comment, decide which of three it is:

- **Needs a change.** Make it. Follow what the reviewer asked rather than a
  variation you prefer; if you think they are wrong, that is an escalation, not a
  silent substitution.
- **Needs no change.** A question you can answer, a remark, an approval of
  something. Fine — deciding this is enough, and you do not have to touch code to
  have dealt with it.
- **You cannot tell what it asks for, or cannot do it.** Escalate.

### 5.3 Re-publish

§3 again, without opening anything:

1. **Run the gate.** Fix what it reports.
2. **Commit** with `tg commit new`, once per repository with changes.
3. **Push** to the existing branch. **Never `tg pr new`** — it would fail, and the
   review lives on the pull request that already exists.
4. **Wait for checks to pass**, same as the first time round.

**Never** weaken a test or a lint rule to get a green result. That rule does not
relax because a reviewer is waiting.

### 5.4 Advance the timestamp — but only as an assertion

Once every comment is dealt with, rewrite `escalations/ready-to-review.md` with a
**newer** timestamp, in the same form as §4.

This is the one rule that matters most in this skill. Writing that file is an
assertion — *"every comment is addressed"* — not housekeeping:

- **Write it** when every comment has been dealt with, including the ones you
  decided needed no code change. Deciding counts; changing is not required.
- **Do not write it** for feedback you could not address or could not understand.
  Escalate instead and leave the timestamp alone.

Never advance the timestamp to get unstuck. It is what releases the orchestrator's
latch, so a task whose feedback you did not understand but whose timestamp you
advanced looks dealt with while the reviewer's comment stands unanswered. That is
the one outcome this whole loop exists to prevent.

Then stop. Either the reviewer approves — and the task is done — or they comment
again and you are started for another pass.

## Escalating

There is one escalation path, and every step above uses it. Escalate when:

- Three attempts at the same acceptance criterion have failed (§2).
- The blocker is a decision rather than a difficulty — escalate **immediately**,
  without spending the three attempts. The design is ambiguous about what the code
  should do, two reasonable readings give different behaviour, or meeting one
  criterion would break another.
- The gate cannot pass without undoing what the task set out to do (§3, §5.3).
- A review comment is genuinely unclear, asks for something you cannot do, or
  conflicts with the acceptance criteria (§5.2).

Write `escalations/<timestamp>.md`, naming the file for the moment you wrote it in
`YYYY-MM-DD-HH-MM-SS` form:

```markdown
# What is blocking 01-prepare-codebase

What I was trying to do, and what happened — the three attempts, or the comment
quoted with who left it.

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
`nm workplan resolve` delivers the answer whenever it arrives — a later pass of
this skill picks it up at §1.

**When you escalate from the review loop, do not advance `ready-to-review.md`.**
Leaving it alone is what keeps the task in review with the trigger live, which is
accurate: it still needs a human.

## Never

- **Stop to ask a question.** There is no terminal. Write an escalation file
  instead — an interactive prompt is how this task becomes unrecoverable.
- Touch a repository the definition does not name.
- Weaken a test, a lint rule, or an acceptance criterion to finish.
- Write `ready-to-review.md` before the pull request is open and green, or to get
  unstuck on feedback you did not understand.
- Open a second pull request for a task already in review.
- Force push, or push to `main`.
