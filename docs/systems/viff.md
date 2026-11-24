# viff

The `viff` system is viff itself, acted on by a task as the person it runs for would act on it.
Each verb is a verb of `viff` and of [`viff mcp`](../reference/mcp.md), and its tool takes what
that MCP tool takes. An agent uses viff the same way whether it runs outside viff, through
`viff mcp`, or inside a task, through this system.

A task's own questions, which it waits on as a task, are the [question system](question.md)'s.

## What a task can do

A task has the viff system's tools when its environment gives `viff` and its runner reaches
viff with a token of its own. Each is a step, decided by the autonomy row that matches it.

The example on this page is one team's release. A task drafts the release notes for v1.3 from
`acme/app`'s history and asks dana whether to post them in `#deploys`. It does not wait for the
answer: the post happens when dana says yes, after the task has ended.

```tony
spawn:
  agent: {name: viff}
  environment:
    systems:
      viff: {ask-create: {}, ask-get: {}}
      slack: {post: {channel: deploys}}
      git:
      - repo: acme/app
        from: main
        fetch: {}
  prompt: "Draft the release notes for v1.3 from the commits since v1.2, and ask dana whether to post them in #deploys."
  autonomy:
  - {then: allow}
```

| Tool | Mirrors | What it does | Step |
|---|---|---|---|
| `viff-ask-create` | `viff ask [<name>] -q`, `viff_ask_create` | raises an ask as the person the task runs for: `name`, `question`, `to`, `due`, `on_silence`, `on`, `about`, `keep`, `label` ([asking](../asking.md)). It does not wait: the result is `raised question <id>: <question>`, and the ask stands after the task ends. Without a `name`, viff names it `run-<nonce>`. | `viff.ask-create`, with `to`, `labels` and `on` |
| `viff-ask-get` | `viff ask <name\|id>`, `viff_ask_get` | reads one of the person's asks, by `name_or_id`: the name it was asked under or its id. The result is the ask as `viff ask` shows it: its `status` and `answer`, `pending` until it settles (or `why`, where it settled with no answer), what it asked and of whom, its due, and what its `on` ran. | `viff.ask-get` |
| `viff-ask-list` | `viff ask`, `viff_ask_list` | lists the person's asks, each as `viff ask` lists it: its id, question, who is asked, its due, its `status`, and its `answer`, `pending` until it settles. | `viff.ask-list` |
| `viff-data-get` | `viff data`, `viff_data` | reads the viff at an address as the person: `at`, `if`, `if_at`, `trim`, and with `paths` the addresses beneath it, `depth` levels deep, one kinded path per line. | `viff.data-get`, with `path` |
| `viff-data-put` | `viff data <address> --put`, `viff_data_put` | writes a `value` at an address `at` as the person, merged into what stands there; with `create`, `update` or `expect` only on that condition, with `grants` for a node it creates. The result is what stands there after the write. | `viff.data-put`, with `path` and `value` |
| `viff-data-rm` | `viff data <address> --rm`, `viff_data_rm` | removes what stands at an address `at` as the person: the data there, or one field of it. | `viff.data-rm`, with `path` |
| `viff-charter-get` | `viff charter`, `viff_charter` | reads the charter at a `path` as the person: an installed charter as its file, with what the file cannot say (a rule held, suspended or in trouble, the charters under the address) as notes after it; a rule or level of one as it stands; with `paths` one kinded path per rule and level, `depth` levels deep. `at`, `if` and `trim` as `viff charter` takes them. | `viff.charter-get` |
| `viff-charter-check` | `viff charter --check`, `viff_charter_check` | asks whether `charters`, one tony document apiece, would install at the address `at`: each charter's verdict, and what the install would add, change and take away. Nothing is installed. | `viff.charter-check` |
| `viff-charter-install` | `viff charter -a`, `viff_charter_install` | installs `charters` as the person: one charter, or a rule, a level or a list of them, at the address `at`; several, each at its own `at:`; with `prune`, what they leave out at and under `at` goes, in the same step. The result says what went where, the rules held, and when a charter with a `ttl:` goes. | `viff.charter-install`, with `at` |
| `viff-charter-remove` | `viff charter -r`, `viff_charter_remove` | removes a package at a `path`, or one rule, level or schedule of it, as the person. What it wrote stays. `at` as `viff charter` takes it. | `viff.charter-remove` |

The ask is the person's, not the task's. Its `from` names them, with the task beside them, and
nothing waits on it. Its rows run as that person when it settles. A spawn in any of them
is held within the task when the ask is raised: an environment no wider than the task's, less
`viff.charter-install`, an autonomy no looser, and the task's credential where it spends one, and it runs on the task's runner. An answer in any of them is refused,
since it would be the person's, on any ask that waits on them, another task's gate among them. A task that needs the answer to
go on uses `question-ask`. One that looks again later reads the ask back with `viff-ask-get`,
which says whether it was answered.

A task reads and writes with the data tools as the person: what they cannot see, the task cannot
either, and what they may not write is refused in viff's words. What a task does on viff is done in
baseline, whatever scope the task runs in, and the runner refuses a request that names a scope.

A row decides these steps by what they carry. `viff.ask-create` carries the ask's `to`,
`labels` and `on` as the ask has them. Each data step carries its address as `path`, and
`viff.data-put` carries the `value` it writes:

```tony
autonomy:
- {match: {system: viff, verb: ask-create, on: !not.all.not {then: {ask: null}}}, then: deny}
- {match: {system: viff, verb: data-put, path: [scratch]}, then: allow}
- {match: {system: viff, verb: !or [data-put, data-rm]}, then: ask}
- {then: allow}
```

The first row refuses a raise with a row that raises a further ask. The second lets writes under
`scratch` go unasked, and the third asks before every other write or removal.

A step at an address reaches everything under it: a read of `play` returns `play.secret`, a
read of the root returns all of viff, and a write at an address can change what lies under it.
In the step's address, `*` is any one child and `..` any depth. So a row reads `path` toward
refusing: a row that allows matches a step whose whole reach lies under its pattern, and a row
that denies or asks matches a step that reaches anything under it. With these rows, a read of
`play.notes` is allowed, and a read of `play.secret.x`, `play`, `play.*` or the root is denied:

```tony
- {match: {system: viff, verb: data-get, path: [play, secret]}, then: deny}
- {match: {system: viff, verb: data-get, path: [play]}, then: allow}
- {match: {system: viff, verb: data-get}, then: deny}
```

An address the runner cannot read is refused before any row decides it.

A task reads the charters with `viff-charter-get`, asks whether its own would install with
`viff-charter-check`, and installs and removes them with `viff-charter-install` and
`-remove`, all as the person. A charter the task installs stands after the task ends, and every
spawn among its rules is held within the task: an environment no wider than the task's, less
`viff.charter-install`, an autonomy no looser, and the task's credential where it spends one, on whichever runner viff routes it to. The
install refuses a spawn beyond the task, and each firing is held to the task's environment again,
bound ([a charter a task installs](../reference/charter.md)). So neither what a task installs nor
what follows its asks can install charters. The hold is on spawns: a rule's `patch` and `derive`,
a write into an outbox among them, are the person's writes, as any charter's are, whether or not
the task has `viff.data-put`. Giving a task `viff.charter-install` gives it that.

Where a task may install is the spawn's to bound, with `at`: the address the install acts at,
which everything it installs lies at or under. With `prune` it is the prune's address; otherwise
it is the deepest address the charters it installs all share. An address matches a pattern when
it is at or under it, and a `*` in the pattern takes any one segment. This task may install at
`play.issues.issue` and under it, as a prune of one charter per issue would, and nowhere else:

```tony
environment:
  systems:
    viff: {data-get: {}, charter-install: {at: play.issues.issue}}
```

```sh
viff-call-tool viff-ask-create -question "Post these release notes for v1.3 in #deploys?" \
  -to user:dana@example.com -due 48h \
  -on '[{match: {verdict: proceed}, then: {spawn: {script: "viff-call-tool slack-post -channel deploys -text \"v1.3: …\"",
    autonomy: [{then: allow}], environment: {systems: {slack: {post: {channel: deploys}}}}}}}]'
```

The row above runs on the task's runner, with an environment that gives no more of Slack than
the task's, and an autonomy no looser. A spawn whose doer the task's runner does not offer, or
naming a channel the task's environment does not give, is refused when the ask is raised. So what follows the ask can do no
more than the task could have done itself: the ask lets the task end now and leave the post to
dana's answer, which may come days later.

A charter the task installs follows the same rule. Here, for the three days after the release, a
failed run on `main` is posted in `#deploys` (the task's environment would also give
`charter-install`):

```sh
viff-call-tool viff-charter-install -charters 'at: releases.v1-3.watch
ttl: 72h
rules:
- name: main-failed
  condition: {subject: {path: "github.acme.app.run.*", value: {branch: main, conclusion: failure}}}
  action:
    spawn: {script: "viff-call-tool slack-post -channel deploys -text \"main failed after v1.3: $RUN\"",
      autonomy: [{then: allow}],
      environment: {vars: {RUN: $[subject.ref]}, systems: {slack: {post: {channel: deploys}}}}}'
```

## Where the credential lives

In the runner. The task holds none. Each verb is a request to viff's API, which the runner
carries for the task with its own token, naming the task
([permissions](../reference/perms.md#runners-at-the-door)). Viff makes the request as the task's
owner, only while the task runs. A runner carries nothing but these verbs for a task. A refusal
of the runner itself, its token or the task, is an error to the task, and a refusal of the ask
is a no. A runner that reaches no viff doesn't offer the system, and refuses a launch that
names it.

**The operator** opens the system to each doer whose tasks may use it, in the runner's
configuration, as any system is opened. A doer whose section names no `viff` has the system
closed, and a spawn that gives that doer `viff` is refused where it is written. The runner needs
no credential for it beyond the token it proves itself to viff with. For the release task above
and the charter it installs, beside its `git` and `slack` ceilings:

```tony
agents:
  viff:
    systems:
      viff:
        ceiling: {ask-create: {}, ask-get: {}, charter-install: {}}
```

A ceiling lists what it allows: this one gives no `data-put` or `charter-remove`, so no task of
the viff agent's on this runner writes data or removes a charter through the system.
`ceiling: {}` gives every verb.
