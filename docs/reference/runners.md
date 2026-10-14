# Runners

This page lists the commands, routes and fields of runners. A
[runner](../glossary.md#runner) is a place where verse runs a [task](../glossary.md#task).
For what a runner is and why it has this shape, see [../runners.md](../runners.md). For the
contract that a runner serves, see [../task-control.md](../task-control.md). To build a
runner, copy [examples/runner](examples/runner/main.go).

A runner is an entity at `verse.runners.<name>`. It carries its `name` and two fields:

| Field | What it holds |
|---|---|
| `url` | the base URL of task control that faces verse. `client.HTTP` drives this URL, and a restart rebuilds the runner from it. Required. |
| `identity` | the [principal](../glossary.md#principal) that the runner acts as. Verse writes the reports of a task as this principal. A spawn grants this principal `read-write` and `control` on the task. A runner acts as itself. It never acts as its installer, or as the person whose work it runs. Default: the runner's name ([perms.md](../perms.md)). |

The `name` is the whole of the address. It may not contain `/`, because it is a segment of
the route that serves it.

## Commands

```
verse runner                              what is installed, with running/total/lastSpawn
        [-probe]                           …and what each one says right now (not recorded)
verse runner add <name> <url>              install one, probing the url first
       add [-file <f>] | [file] | stdin    …or as a document (.tony/.json/.yaml)
           [-no-probe]                     install without asking the url what it is
           [-identity <id>]                it acts as runner:<id> (default: its name)
       show <ref>                          the entity
       check <ref>                         ask this runner what it is, right now
       remove <ref>                        take it away; refused if work is running on it
              [-cancel "<why>"]            …the place is going away: end that work, with a reason
              [-leave]                     …the place stays: leave it running
```

A `<ref>` has two forms:

- `verse.runners.gpu`: the whole [address](../glossary.md#address), as verse lists and
  stores it;
- `gpu`: the name, as you type it on a command line.

Verse does not accept the middle form, `runners.gpu`. The address of a runner is its name and
nothing else, so each name is unique on its own. Adding a runner with a name that stands
replaces the runner.

On the task side:

```
verse ps  [-runner <ref>]                  only the work on that runner (`local` = named none)
verse task run -runner <ref> <prompt>     run this one there
```

## Routes

| Route | What it does |
|---|---|
| `GET /runners` | lists the runners. `?probe=true` adds a live `reach` for each runner. |
| `POST /runners` | installs a runner. `?probe=false` skips the check. The answer is `201` with `{at, reach}`, plus `warning` when the runner is unreachable. |
| `GET /runners/{name}` | returns the entity |
| `GET /runners/{name}/check` | returns `{at, url, reach}`, and `why` when the URL is not a runner |
| `DELETE /runners/{name}` | removes the runner. Pass `?cancel=<why>` or `?leave=true`. When work is running and you give neither, the answer is `409` and names both. |
| `GET /tasks?runner=<ref>` | lists the work on one runner |

`reach` is one of `a runner`, `not a runner` or `unreachable`. Verse writes nothing about
`reach` to the store. Verse answers it live and then discards it, so a column of reachability
is always as of now.

A route that names one runner has two answers when it does not find the runner:

| Status | Answer |
|---|---|
| `404` | `in your view, no runner "<name>" is installed`. The runner is not installed, or you may not read it. The answer is the same for both. |
| `503` | `runner "<name>" could not be read: …`. The store did not answer. Verse knows of nothing missing, so ask again. |

The listing leaves out the runners that you may not read. It answers `503` when verse could
not read the store.

## On a spawn

A spawn names its runner in one of two places:

- `runner:` on the `spawn:` of a charter;
- `-runner` or `runner` on the ad-hoc path.

An empty runner means the default runner of this verse, which `verse up -runner` sets.

Verse **refuses a spawn that names a runner that is not installed**, on both paths:

- an ad-hoc run gets `400`, with the name in the message;
- a charter spawn becomes a failed spawn, and its reason goes onto the flow.

Verse never runs the task somewhere else without saying so.

Verse looks up the runner in the view of the spawner. For a rule, that is the view of the
rule's owner. Verse refuses a runner that the spawner may not read in the same words as a
runner that is not installed: `in the view of <spawner>, no runner "<name>" is installed`.
The runners that the spawner may read follow. When verse could not read the store, the refusal
says so, and says nothing about what is installed.

## On the task

`verse.task.instance` carries both the URL and the name of the runner:

| Field | What it holds |
|---|---|
| `runner` | the resolved URL, or `local`. A restart attaches again with it. |
| `runnerName` | the `verse.runners.<name>` that the spawn named, when it named one |
| `startedAt` | when the task began. Verse carries it across a new attach, and does not stamp it again. |

A restart can act on the URL with nothing else present. So removing a runner entity cannot
leave running work stranded. A person reads the name, and `verse ps -runner` selects on the
name. The name keeps working after the entity is gone.

## What a caller can tell about a control call

A control call that fails says which failure it is (`task/errors.go`):

| Error | Meaning |
|---|---|
| `ErrUnreachable` | the far side did not answer. Nothing is known about whether it acted. |
| `ErrNoSuchTask` / `ErrNoSuchDecision` | the far side answered, and has never heard of this task or decision. Look for a typo. |
| `ErrAlreadyAnswered` | somebody else answered first |
| `ErrEnded` | the task is over, so words said to it would be heard by no one (`410` on `/say`) |
| `ErrUnsupported` | this runner has no such route at all |

A caller that cannot tell these failures apart does one of two wrong things. It retries a call
that the far side already applied, or it gives up on a call that never arrived.

## What a reader gets after a gap

A reader of the stream resumes with `since=<cursor>`, not from the start. The resume replays
the questions that are still open. So a reader that missed a gate does not have to learn about
it from the store.

## Why it is this way

### Why the task carries both the URL and the name

A restart must be able to act with nothing else present, and the URL is enough for that. A
person needs a name to read, and a selection needs a name to match after the entity is gone.
One field cannot do both jobs, so the task carries both.

### Why a ref has no middle form

Verse does not accept `runners.gpu` because two ways to say one thing is one too many.
