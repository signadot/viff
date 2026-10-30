# verse

The `verse` system is verse itself, acted on by a task as the person it runs for would act on it.
Each verb is a verb of `verse` and of [`verse mcp`](../reference/mcp.md), and its tool takes what
that MCP tool takes. An agent uses verse the same way whether it runs outside verse, through
`verse mcp`, or inside a task, through this system.

A task's own questions, which it waits on as a task, are the [question system](question.md)'s.

## What a task can do

A task has the verse system's tools when its environment gives `verse` and its runner reaches
verse with a token of its own. Each is a step, decided by the autonomy row that matches it.

The example on this page is one team's release. A task drafts the release notes for v1.3 from
`acme/app`'s history and asks dana whether to post them in `#deploys`. It does not wait for the
answer: the post happens when dana says yes, after the task has ended.

```tony
spawn:
  agent: {name: verse}
  runner: verse.runners.build
  environment:
    systems:
      verse: {ask-create: {}, ask-get: {}}
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
| `verse-ask-create` | `verse ask [<name>] -q`, `verse_ask_create` | raises an ask as the person the task runs for: `name`, `question`, `to`, `due`, `on_silence`, `then`, `else`, `about`, `keep`, `label` ([asking](../asking.md)). It does not wait: the result is `raised question <id>: <question>`, and the ask stands after the task ends. Without a `name`, verse names it `run-<nonce>`. | `verse.ask-create` |
| `verse-ask-get` | `verse ask <name\|id>`, `verse_ask_get` | reads one of the person's asks, by `name_or_id`: the name it was asked under or its id. The result is the ask as `verse ask` shows it: its `status` and `answer`, `pending` until it settles (or `why`, where it settled with no answer), what it asked and of whom, its due, and what its `then` and `else` did. | `verse.ask-get` |
| `verse-ask-list` | `verse ask`, `verse_ask_list` | lists the person's asks, each as `verse ask` lists it: its id, question, who is asked, its due, its `status`, and its `answer`, `pending` until it settles. | `verse.ask-list` |
| `verse-data-get` | `verse data`, `verse_data` | reads the verse at an address as the person: `at`, `if`, `if_at`, `trim`, and with `paths` the addresses beneath it, `depth` levels deep, one kinded path per line. | `verse.data-get` |
| `verse-data-put` | `verse data <address> --put`, `verse_data_put` | writes a `value` at an address `at` as the person, merged into what stands there; with `create`, `update` or `expect` only on that condition, with `grants` for a node it creates. The result is what stands there after the write. | `verse.data-put` |
| `verse-data-rm` | `verse data <address> --rm`, `verse_data_rm` | removes what stands at an address `at` as the person: the data there, or one field of it. | `verse.data-rm` |
| `verse-charter-get` | `verse charter`, `verse_charter` | reads the charter at a `path` as the person: an installed charter as its file, with what the file cannot say (a rule held, suspended or in trouble, the charters under the address) as notes after it; a rule or level of one as it stands; with `paths` one kinded path per rule and level, `depth` levels deep. `at`, `if` and `trim` as `verse charter` takes them. | `verse.charter-get` |
| `verse-charter-check` | `verse charter --check`, `verse_charter_check` | asks whether `charters`, one tony document apiece, would install at the address `at`: each charter's verdict, and what the install would add, change and take away. Nothing is installed. | `verse.charter-check` |
| `verse-charter-install` | `verse charter -a`, `verse_charter_install` | installs `charters` as the person: one charter, or a rule, a level or a list of them, at the address `at`; several, each at its own `at:`; with `prune`, what they leave out at and under `at` goes, in the same step. The result says what went where, the rules held, and when a charter with a `ttl:` goes. | `verse.charter-install` |
| `verse-charter-remove` | `verse charter -r`, `verse_charter_remove` | removes a package at a `path`, or one rule, level or schedule of it, as the person. What it wrote stays. `at` as `verse charter` takes it. | `verse.charter-remove` |

The ask is the person's, not the task's. Its `from` names them, with the task beside them, and
nothing waits on it. Its `then` and `else` run as that person when it settles. A spawn in either
is held within the task when the ask is raised: an environment no wider than the task's, less
`verse.charter-install`, an autonomy no looser, the task's runner and the task's credential where it spends one. An answer in either is refused,
since it would be the person's, on any ask that waits on them, another task's gate among them. A task that needs the answer to
go on uses `question-ask`. One that looks again later reads the ask back with `verse-ask-get`,
which says whether it was answered.

A task reads and writes with the data tools as the person: what they cannot see, the task cannot
either, and what they may not write is refused in verse's words. What a task does on verse is done in
baseline, whatever scope the task runs in, and the runner refuses a request that names a scope.

A task reads the charters with `verse-charter-get`, asks whether its own would install with
`verse-charter-check`, and installs and removes them with `verse-charter-install` and
`-remove`, all as the person. A charter the task installs stands after the task ends, and every
spawn among its rules is held within the task: an environment no wider than the task's, less
`verse.charter-install`, an autonomy no looser, the task's credential where it spends one, and any runner. The
install refuses a spawn beyond the task, and each firing is held to the task's environment again,
bound ([a charter a task installs](../reference/charter.md)). So neither what a task installs nor
what follows its asks can install charters. The hold is on spawns: a rule's `patch` and `derive`,
a write into an outbox among them, are the person's writes, as any charter's are, whether or not
the task has `verse.data-put`. Giving a task `verse.charter-install` gives it that.

```sh
verse-call-tool verse-ask-create -question "Post these release notes for v1.3 in #deploys?" \
  -to user:dana@example.com -due 48h \
  -then '{spawn: {script: "verse-call-tool slack-post -channel deploys -text \"v1.3: …\"",
    runner: verse.runners.build, autonomy: [{then: allow}],
    environment: {systems: {slack: {post: {channel: deploys}}}}}}'
```

The `then` above runs on the task's runner, with an environment that gives no more of Slack than
the task's, and an autonomy no looser. A `then` naming another runner, or a channel the task's
environment does not give, is refused when the ask is raised. So what follows the ask can do no
more than the task could have done itself: the ask lets the task end now and leave the post to
dana's answer, which may come days later.

A charter the task installs follows the same rule. Here, for the three days after the release, a
failed run on `main` is posted in `#deploys` (the task's environment would also give
`charter-install`):

```sh
verse-call-tool verse-charter-install -charters 'at: releases.v1-3.watch
ttl: 72h
rules:
- name: main-failed
  condition: {subject: {path: "github.acme.app.run.*", value: {branch: main, conclusion: failure}}}
  action:
    spawn: {script: "verse-call-tool slack-post -channel deploys -text \"main failed after v1.3: $RUN\"",
      runner: verse.runners.build, autonomy: [{then: allow}],
      environment: {vars: {RUN: $subject}, systems: {slack: {post: {channel: deploys}}}}}'
```

## Where the credential lives

In the runner. The task holds none. Each verb is a request to verse's API, which the runner
carries for the task with its own token, naming the task
([permissions](../reference/perms.md#runners-at-the-door)). Verse makes the request as the task's
owner, only while the task runs. A runner carries nothing but these verbs for a task. A refusal
of the runner itself, its token or the task, is an error to the task, and a refusal of the ask
is a no. A runner that reaches no verse doesn't offer the system, and refuses a launch that
names it.

**The operator** opens the system to each doer whose tasks may use it, in the runner's
configuration, as any system is opened. A doer whose section names no `verse` has the system
closed, and a spawn that gives that doer `verse` is refused where it is written. The runner needs
no credential for it beyond the token it proves itself to verse with. For the release task above
and the charter it installs, beside its `git` and `slack` ceilings:

```tony
agents:
  verse:
    systems:
      verse:
        ceiling: {ask-create: {}, ask-get: {}, charter-install: {}}
```

A ceiling lists what it allows: this one gives no `data-put` or `charter-remove`, so no task of
the verse agent's on this runner writes data or removes a charter through the system.
`ceiling: {}` gives every verb.
