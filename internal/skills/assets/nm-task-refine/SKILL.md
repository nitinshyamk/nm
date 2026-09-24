---
name: nm-task-refine
description: Read the review comments on a workplan task's pull request, action what needs actioning, and advance ready-to-review.md when every comment is dealt with. Use when a reviewer has left feedback on a task already in review, normally started by nm workplan execute.
---

# Action review feedback on one task

You are in a task directory whose pull request is in review and has feedback newer
than the timestamp in `escalations/ready-to-review.md`. Your job is to deal with
every comment and then say so.

You are running unattended, and the orchestrator started you because a reviewer
said something. Treat their comment as the authority on what they want, not as a
suggestion to weigh against your own reading of the code.

## 1. Read the feedback

Find the pull request number, then:

```sh
tg pr comments <number>
```

It prints comments on the pull request as a whole first, then each review thread
under its own heading. Read every thread to its end — a thread often resolves
itself two replies down, and actioning the first comment in it would undo that.

## 2. Action what needs actioning

For each comment, decide which of three it is:

- **Needs a change.** Make it. Follow what the reviewer asked rather than a
  variation you prefer; if you think they are wrong, that is an escalation, not a
  silent substitution.
- **Needs no change.** A question you can answer, a remark, an approval of
  something. Fine — deciding this is enough, and you do not have to touch code to
  have dealt with it.
- **You cannot tell what it asks for, or cannot do it.** Escalate. See below.

Then re-run the repository's gate (usually `mise run pre-commit`), commit with
`tg commit new`, and push. **Never** weaken a test or a lint rule to get a green
result, and never open a new pull request — `tg pr new` would fail, and the review
lives on the pull request that already exists.

## 3. Escalate what you cannot resolve

When a comment is genuinely unclear, or asks for something you cannot do, or
conflicts with the task's acceptance criteria, write
`escalations/<timestamp>.md` naming the file for the current moment in
`YYYY-MM-DD-HH-MM-SS` form:

```markdown
# Unclear review feedback on 01-prepare-codebase

The comment, quoted, and who left it.

## What I cannot tell

The two readings, and what each would mean for the code. Name the one you would
pick and why.
```

Surface it the normal way as well, then block on the answer:

```sh
nm workplan await-resolution escalations/<timestamp>.md --timeout 60m
```

**Do not advance `ready-to-review.md` when you escalate.** Leaving it alone is
what keeps the task in review with the trigger live, which is accurate: it still
needs a human.

## 4. Advance the timestamp — but only as an assertion

Once every comment is dealt with, rewrite `escalations/ready-to-review.md` with a
**newer** timestamp:

```markdown
# Ready for review

2026-05-01T09:12:44Z
```

This is the one rule that matters most in this skill. Writing that file is an
assertion — *"every comment is addressed"* — not housekeeping:

- **Write it** when every comment has been dealt with, including the ones you
  decided needed no code change. Deciding counts; changing is not required.
- **Do not write it** for feedback you could not address or could not understand.
  Escalate instead and leave the timestamp alone.

Never advance the timestamp to get unstuck. It is what releases the orchestrator's
latch, so a task whose feedback you did not understand but whose timestamp you
advanced looks refined while the reviewer's comment stands unanswered. That is the
one outcome this whole loop exists to prevent.

Then you are done. Stop.
