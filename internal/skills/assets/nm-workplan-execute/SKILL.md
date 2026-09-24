---
name: nm-workplan-execute
description: Drive a workplan to completion by running nm workplan execute on a timer, reporting transitions and escalations, and writing the human's answers back. Use when a workplan is defined and the work should start moving.
---

# Drive a workplan

You are the loop between a workplan and the person who owns it. The CLI makes the
transitions; you run it, read what it says, and carry escalations to a human and
answers back.

## 1. Understand the shape

A workplan lives at `<workplans_root>/<name>/` and holds one file per task, in the
directory named for the state it is in:

```
planned/  in-progress/  review/  approved/  completed/
escalations/<id>/     one directory per task
artifacts/            the design the tasks were scoped against
```

**A task's state is which directory its file is in.** So `ls` answers "where is
everything", and a transition is a file move.

`nm workplan execute <name>` does one pass: collects escalations, moves what can
move, starts what is unblocked, prints what changed. It prints **nothing** when
nothing changed. It is idempotent — an interrupted pass is repaired by running it
again, not cleaned up after.

It does **not** merge unless given `--merge`. Do not pass `--merge` from the
poller. Merging into production is a thing to do when a human asks, not on a
timer.

## 2. Get the workplan name

If the user named one, use it. If you are inside a workplan directory, **ask them
to confirm that one** rather than assuming — being in a directory is weak evidence
about intent. Otherwise ask.

Check it exists and is sound before starting the loop:

```sh
nm workplan verify <name>
```

## 3. Poll

Run `nm workplan execute <name>` every minute in the background. Use `--json` if
you want to parse it; the plain output is made to be read.

Each pass, report only what changed:

- **Transitions** — which task moved where, and the note (`agent a3f9c1`,
  `merged #1421`).
- **Escalations** — with their full content. Whoever reads this has to decide
  something, and summarizing it would make them open the file anyway.
- **Problems** — including the one that matters most: *"a refine round started N
  ago is gone and never finished"*. That means an agent died mid-round and the
  orchestrator is deliberately refusing to start another, because whatever killed
  the first will kill the next. Surface it; do not try to work around it.

Say nothing when the pass says nothing. Sixty "no change" reports an hour is noise
the user then has to filter.

When a pass reports an escalation, **ask the user for a decision** and include what
the agent said it would pick. A yes-or-no question gets answered; an open one gets
postponed.

## 4. Write the answer back

An escalation at `escalations/01-prepare-codebase/2026-05-01-08-34-02.md` is
answered by a file beside it with `-resolution.md` in place of `.md`:

```
escalations/01-prepare-codebase/2026-05-01-08-34-02-resolution.md
```

Match the timestamp exactly — that is the only link between question and answer.
Write what was decided and enough of why that the agent does not have to re-derive
it.

## 5. Deliver it

```sh
nm workplan resolve <name>
```

This copies resolutions into the task directories, where the blocked agent picks
them up within a second or two. The next `execute` pass would do it too, so this
is for when you do not want to wait a minute.

## Finishing

The workplan is done when every task is in `completed/`. Tasks sitting in
`approved/` need a human to merge them: report them as ready and say so, rather
than passing `--merge` yourself.

Stop the poller when the user asks, or when every task is completed and nothing is
waiting.
