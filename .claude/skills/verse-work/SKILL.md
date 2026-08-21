---
name: verse-work
description: Pick up and execute a brokered verse task from inside this Claude session — find unclaimed work, claim it, narrate progress onto the task, raise gates for anything risky, and complete it with a result. Use when the user wants to work a verse task, take verse work, see what verse has waiting, or asks about being a verse worker/executor.
argument-hint: "[task id, or nothing to take whatever is waiting]"
---

# Working a verse task

A verse task that is **brokered** has no runtime of its own. It sits `unclaimed` until an
executor dials in, does the work, and reports back. You are that executor: this session, with
its tools and the user's credentials, is the runtime.

The user is the identity. Everything below carries `--as`, because a claim must be attributable
and the person at the keyboard is who it is attributable to. Ask for it once if you cannot
infer it, and reuse the same string for the whole task — see *Never change identity* below.

## The loop

```sh
verse task                                    # what exists; look for status: unclaimed
verse task get <id>                           # the prompt, in full
verse task do note <id> "picked this up" --as <who>     # claim it and say so
# … do the work in this session, narrating at milestones …
verse task do complete <id> "<the result>" --as <who>
```

`verse task do` **is** the dial-in client. It reads the task's `dialURL`, claims the task on
the broker, and reports — so there is no URL to configure and no protocol to speak by hand.
Never construct broker requests with `curl`; if a `verse task do` verb cannot express something,
say so rather than reaching around it.

## Finding work

```sh
verse task --if '{status: unclaimed}'
```

A brokered task is the one with a `dialURL`. A task without one runs somewhere else and is not
yours to claim — leave it alone. If `$ARGUMENTS` names an id, work that one; otherwise take the
oldest unclaimed brokered task, and if there are several, show the user the list and let them
pick rather than guessing.

## Claiming

There is no separate claim verb: the first `verse task do` claims. Make it a note that says
what you are about to do, so the record starts with something a reader can use:

```sh
verse task do note <id> "claimed by <who>; reading the charter first" --as <who>
```

The task moves `unclaimed → running` when this lands. If it comes back **already claimed
(409)**, somebody else is holding it — tell the user and stop. Do not retry, and do not claim
it under a different identity to get around it.

## Doing the work

This is an ordinary session. Read files, run tests, edit code, ask the user. The point of a
brokered task is that the work happens where the credentials and the context already are.

**Narrate at milestones**, not per thought:

```sh
verse task do note <id> "the charter installs; the failing rule is train-load" --as <who>
```

Each note is a `say` on the task's read plane, so anybody running `verse task attach <id>` sees
it as it happens. That is the entire reason a note exists — the user watching from another
terminal is the audience. Three to six notes over a task is about right: what you found, what
you decided, what you changed, what you could not do.

## Gates: ask before anything irreversible

Anything that leaves this machine or is hard to undo — pushing a branch, writing to a shared
verse, deleting anything, touching a deploy — raise a gate and wait:

```sh
verse task do gate <id> <gate-name> --as <who> --reason "push verse/auto/<sha> to origin" --wait
```

`--wait` blocks until somebody answers. Approved: proceed. Denied: do **not** do it, note that
it was refused, and carry on with the rest — a refused gate is an answer, not a failure.

You never resolve your own gate. Raising it hands the decision up to the verse, which decides
who answers. If you catch yourself wanting to approve it, that is the signal it needed asking.

Whoever answers does it from the other side — worth telling the user, since a `--wait` that
nobody knows about is a session that looks hung:

```sh
verse gate                                                  # every pending decision
verse task approve <task-id> <decision-id> --as <who> --say "ok"
verse task deny <task-id> <decision-id> --as <who> --say "not this branch"
```

## Finishing

Exactly one terminal report, and it must happen — a task nobody terminates sits `running` for
ever and the charter that spawned it never fires its hook.

```sh
verse task do complete <id> "<result>" --as <who>    # it worked
verse task do fail <id> "<why>" --as <who>           # it did not
```

The result is what downstream reads: an `onDone` hook binds it, and a person reads it in
`verse task get`. Write the answer, not a transcript — what was found, what changed, what a
reader must do next. If the work was refused at a gate, that is still `complete` with a result
saying what was and was not done; `fail` is for work that could not be carried out.

## Control coming the other way

```sh
verse task inbox <id>      # what has been queued for this task's worker
```

Check it if a task runs long, and honour what is there:

- `cancel` — stop. Wind down, then `verse task do fail <id> "cancelled" --as <who>`.
- `say` — the operator is talking to you. Read it and act on it; it is the user steering
  mid-task.
- `resolve` — a gate you raised was answered.

## Never change identity

Every `verse task do` re-claims, and a claim by a *different* identity is a takeover: it fences
the previous holder, who becomes a stranger and may no longer report. So use one `--as` for the
whole task. If the user gives you a different name partway, ask which one is theirs rather than
switching and orphaning your own claim.

## When it goes wrong

| what you see | what it means |
|---|---|
| `already claimed` / 409 | somebody else holds it, or you changed `--as`. Stop; do not retry. |
| `is not a brokered task` | it runs on a runner and has no dialURL. Not yours to execute. |
| `410 Gone` | it already reached a terminal state. Nothing more to report. |
| a note works, then stops | you were fenced by another worker. Stop and tell the user. |

## Optional: be told when work appears

The user can have a session announce waiting work at startup, by adding to
`.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          {
            "type": "command",
            "command": "verse task --if '{status: unclaimed}' 2>/dev/null || true"
          }
        ]
      }
    ]
  }
}
```

Suggest it only if they ask to be notified; it is their settings file, so do not add it
unprompted.
