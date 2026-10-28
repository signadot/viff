# linear-issue

The `linear-issue` system is the issues of a Linear workspace. Each verb is a call that the
runner makes to Linear with its own credential. The task never has the credential. A runner
without one offers none of these verbs.

## What a charter sees

`iod linear` writes each team that its credential can see into the store: the team's issues,
its cycles and the projects it owns.

| Entity | What it is |
|---|---|
| `linear.<KEY>.issue.<KEY>-<n>` | an issue: its title, `state` and `stateType`, priority, assignee, labels and url |
| `linear.<KEY>.cycle."<n>"` | a cycle: when it starts and ends |
| `linear.project.<uuid>.owners.<KEY>` | a project, one node for each team that owns it |

A rule reacts to an issue as it reacts to any entity. Match on `stateType`, Linear's own word
for the kind of state, rather than on the state's name:

```tony
condition:
  subject:
    path: "linear.ENG.issue.*"
    value: {stateType: started, assignee: null}
```

[iod linear](../reference/iod.md#linear) has the flags and the whole payload.

## What a charter writes

A comment. With `-out`, the iod carries a rule's write back to Linear: it comments on the issue
that a rule names under `linear.<KEY>.out`
([the out half of linear](../reference/iod.md#the-out-half-of-linear)).

## What a task can do

A task gets Linear from its runner, when the runner holds a credential, and from its environment:
a `linear-issue` block gives the verbs, its shapes the teams or issues. Linear is not a checkout,
so the block names no repository. The autonomy decides each call by the team or the issue:

```tony
spawn:
  agent: {name: verse}
  environment: {systems: {network: {mode: open}, linear-issue: {create: {team: ENG}, comment: {}, close: {}}}}
  prompt: "Read $subject and file the follow-ups it names in ENG."
  autonomy:
  - {match: {system: linear-issue, verb: create, team: ENG}, then: allow}
  - {match: {system: linear-issue}, then: ask}
  - {then: allow}
```

| Tool | What it does | Step |
|---|---|---|
| `linear-issue-create` | creates an issue in a team, named by its key, with a title and description; the task reads back the identifier (`ENG-42`) and url | `linear-issue.create`, with `team` |
| `linear-issue-comment` | comments on an issue, by its identifier | `linear-issue.comment`, with `issue` |
| `linear-issue-close` | closes an issue, by its identifier: the issue moves to its team's completed state | `linear-issue.close`, with `issue` |

Every verb is a step. In a row or a shape, `team` is the team's key and `issue` is the
identifier, as a person types them. A script calls the same tools with `verse-call-tool`, and the
same environment and rows decide them.

## Where the credential lives

**The iod** needs `LINEAR_API_KEY`. Its value is the `Authorization` header as Linear takes it:
a personal key alone, or an OAuth token as `Bearer …`.

**The task** holds no credential. The runner makes each call with its own credential, in the
same form, `LINEAR_API_KEY`. The runner offers the system where a doer's ceiling in its
configuration names it and the variable is set; without it, the runner starts with the system
closed, and a spawn that needs it is refused where it is written. The iod and the runner may hold one
credential or two.

**The operator** sets a limit on what any task may do, in the runner's configuration:

```tony
systems:
  linear-issue:
    credential: LINEAR_API_KEY
    ceiling: {create: {team: ENG}, comment: {}}
```
