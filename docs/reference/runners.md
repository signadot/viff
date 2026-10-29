# Runners

This page lists the commands, routes and fields of runners. A
[runner](../glossary.md#runner) is a place where verse runs a [task](../glossary.md#task).
For what a runner is and why it has this shape, see [../runners.md](../runners.md). For the
contract that a runner serves, see [../task-control.md](../task-control.md). To build a
runner, copy [examples/runner](examples/runner/main.go).

A runner is installed as data at `verse.runners.<name>`, its registration. The registration carries its `name` and three fields:

| Field | What it holds |
|---|---|
| `url` | the base URL of task control that faces verse. `client.HTTP` drives this URL, and a restart rebuilds the runner from it. Required. |
| `identity` | the [principal](../glossary.md#principal) that the runner acts as. Verse writes the reports of a task as this principal. A spawn grants this principal `read-write` and `control` on the task. A runner acts as itself. It never acts as its installer, or as the person whose work it runs. Default: the runner's name ([perms.md](../perms.md)). |
| `subject` | how verse recognises the runner: the subject of its workload token, `system:serviceaccount:<namespace>:<name>` on Kubernetes, from the issuer the deployment names under `auth.iod` ([runners at the door](perms.md#runners-at-the-door)); or, for a runner proved by a static token, the token's hash, `sha256:<64 lowercase hex digits>`, which `verse runner add -token-file` computes. Required: a runner is never taken at its word. Verse refuses a workload subject where it names no such issuer, any subject where it issues no tokens of its own (`auth.issuer`, which is what it proves itself to the runner with; `verse up` always does), and one another registration or an iod (`auth.iod.accounts`, or the hash of a token in `auth.iod.static`) already holds, since a token proves one principal, and in a named scope, since a principal is one thing across the verse. |

Verse writes one more, and refuses it by hand: `described`, what the runner offers, as it said
at the last describe ([runners.md](../runners.md#who-the-runner-is)). Every describe is a proof,
so a description is there only while the runner proved it is its `subject`: one that answers and
does not prove it has its description removed, and takes no spawn until a check finds it proving
itself. The handshake is made again before every launch, so `described` is the last launch's
result at the latest. A registration an older verse wrote may hold `verified` or `unverified`
beside it; verse removes both at the next describe.

A listing row (`verse runner`) also says `offers`, the names of the doers the runner described,
an agent's name or `script`, empty where it has described none.

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
           [-subject <sub>]                verse recognises it by its workload token's subject
           [-token-file <f>]               …or by the hash of the static token in f
       show <ref>                          its registration
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
verse task run -f <spawn>                 run a spawn there: its `runner:` names the runner
```

## Routes

| Route | What it does |
|---|---|
| `GET /runners` | lists the runners. `?probe=true` adds a live `reach` for each runner. |
| `POST /runners` | installs a runner. `?probe=false` skips the check. The answer is `201` and the runner's row as the listing shows it, with what the runner described and `reach`, plus `warning` when the runner is unreachable (`api.RunnerRow`). |
| `GET /runners/{name}` | returns the registration |
| `GET /runners/{name}/check` | returns `{at, url, reach}`, and `why` when the URL is not a runner or did not prove it is the one registered; records what the runner said, or removes what it said before where it did not prove itself |
| `POST /runners/hello` | the check, asked by the runner itself with its own token: verse finds the registration by the token's subject and describes the runner now, retrying briefly a url nothing answers at. The answer is the check's: `200` when the runner was described, even when it says `not verified`, since what is wrong is then its token, which trying again will not fix; `503` when nothing answered at its url, or another describe of it is in flight, so the runner's backoff keeps announcing until verse has heard what it is. A token no registration names, and a runner's name with no token, are refused `401` at the door; anyone else gets `400`. One describe in flight per registration. |
| `DELETE /runners/{name}` | removes the runner. Pass `?cancel=<why>` or `?leave=true`. When work is running and you give neither, the answer is `409` and names both. |
| `GET /tasks?runner=<ref>` | lists the work on one runner |

`reach` is one of `a runner`, `not a runner`, `unreachable`, or `not verified`: it answered
as a runner and did not prove it is its registration's `subject`,
which is also what a verse that presented no token of its own gets, since the runner answers
its token to the verse it verified alone. A runner that refused the token verse presented (it
serves another verse) is `unreachable`, since the deployment fixes it. Verse writes nothing about `reach` to the
store. Verse answers it live and then discards it, so a column of reachability
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
- `runner:` on the spawn that `verse task run` reads, on the ad-hoc path.

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
| `runnerSubject` | the subject that registration held at the spawn. The door holds a runner carrying a request for the task to it ([permissions](perms.md#runners-at-the-door)). |
| `startedAt` | when the task began. Verse carries it across a new attach, and does not stamp it again. |

A restart can act on the URL with nothing else present. So removing a runner's registration cannot
leave running work stranded. A person reads the name, and `verse ps -runner` selects on the
name. The name keeps working after the registration is gone.

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
person needs a name to read, and a selection needs a name to match after the registration is gone.
One field cannot do both jobs, so the task carries both.

### Why a ref has no middle form

Verse does not accept `runners.gpu` because two ways to say one thing is one too many.
