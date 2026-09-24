---
name: nm-work-plan
description: Break a pending implementation design into a workplan of tasks, iterate on it with the author, then write it to disk with nm workplan define and nm workplan add. Use when a design is settled and the work needs decomposing into reviewable, dependency-ordered tasks.
---

# Turn a design into a workplan

A workplan is a directory of task definitions, each in the directory named for
the state it is in. This skill takes a design that is already settled and turns
it into one. It does not design anything: if the design is not settled, stop and
say so.

## 1. Find the plan document

Look for what the author is already iterating on, in this order:

1. A plan document open in this session — a local markdown file, a Claude plan,
   a design doc under `artifacts/`.
2. A file the author names when asked.
3. Nothing: create `workplan.md` in the current directory.

Do not create a second document beside one that already exists. The author has
been reading and editing one thing, and a second copy means the next edit lands
in whichever one they happen to open.

## 2. Write the tasks as prose

Break the design into tasks and write them into that document. **Not as JSON** —
as prose that makes each of the five fields explicit:

```markdown
**01-prepare-codebase**
- *predecessors*: none
- *repositories*: `nm`
- *description*: What to do, in enough detail that someone who has not read the
  design could start.
- *acceptance criteria*: The gate passes. The new command is reachable. A test
  covers the empty case.
```

The field rules, which `nm workplan verify` enforces later:

| Field | Rule |
|---|---|
| id | A number, then hyphen-joined lowercase words: `01-prepare-codebase`. Lowercase is required, not conventional — the id becomes a filename, and `01-Foo.json` and `01-foo.json` are one file on Windows and two on Linux. |
| predecessors | Ids in this same workplan. May be empty. The graph must be acyclic. |
| repositories | Directory names under `projects_root`. May be empty, for work that changes no code — a clarification, a decision to record. |
| description | Non-empty. |
| acceptance criteria | At least one, each non-empty. |

Three things to get right, because they decide whether the workplan runs:

- **Acceptance criteria are the definition of done.** An agent works until they
  are met, so a criterion that cannot be checked is a task that never finishes.
  Prefer "the gate passes and a test covers X" over "it works well".
- **Order tasks by testability, not only by dependency.** Two tasks with no
  dependency between them still want the one that makes something inspectable
  first, because that is where a wrong assumption surfaces cheaply.
- **Predecessors are for real blockers.** A task waits for its single predecessor
  to be *approved*, and for two or more to be *completed*. Every edge you add
  costs latency, so do not add one for tidiness.

## 3. Hand it back

Tell the author the tasks are written and let them review. **Do not proceed to
step 4 unasked.** Iterate on the document until they agree.

## 4. Write it to disk

Ask for a workplan name. Coerce it into shape rather than bouncing it back:
lowercase, hyphens for spaces, no slashes. Then:

```sh
nm workplan define -n <name> -a <the-plan-document>
```

`-a` copies the plan into the workplan's `artifacts/`, which is what every task's
agent reads first. Pass the design document, not just the task list — the agents
need the reasoning, not only the instructions.

Then one `add` per task, writing each definition to a file first because a
description is prose with newlines and a command line mangles that:

```sh
nm workplan add <name> --content @01-prepare-codebase.json
```

## 5. Verify, and escalate what you cannot fix

```sh
nm workplan verify <name>
```

It reports every problem at once and exits non-zero. Fix the mechanical ones — a
misnumbered id, a predecessor naming a task that got renamed, a repository whose
name is spelled differently on disk.

**After two or three attempts at the same error, stop and ask the author.** An
error that survives three fixes is usually not mechanical: it means the plan says
something the schema cannot express, and guessing again will not find that out.
