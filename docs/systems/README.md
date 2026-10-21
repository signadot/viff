# Systems

A system is something outside verse that verse reads or acts on: a repository, its issue
tracker, a forge, Linear, Slack. Verse meets each system in two places
([basic concepts](../concepts.md#an-outside-system-has-two-faces)):

- An [iod](../glossary.md#iod) connects the system to the store. It writes what the system
  holds into the store, where a charter reads it. It also carries what a charter writes back
  out to the system: a comment, a tag, a message.
- A runner performs the system's verbs for a task. The task calls a verb, and the runner makes
  the call with its own credential.

Verse keeps one document for each system, at `verse.systems.<name>`. The document names the
system's verbs and the params of each verb:

```sh
verse d verse systems git-issue
```

A verb is written three ways, one for each reader:

| Spelling | Example | Who reads it |
|---|---|---|
| the tool, `<system>-<verb>` | `git-issue-push` | the task that calls it: an agent, or a script with `verse-call-tool` |
| the step, `<system>.<verb>` | `git-issue.push` | the task's environment, which gives it, and its [autonomy](../glossary.md#autonomy), whose rows match it by its params |
| the row, `{system, verb, <params>}` | `{system: git-issue, verb: push, repo: github.com/acme/app}` | the author of a spawn, and the operator who writes the runner's [ceiling](../glossary.md#ceiling) |

A task's events and its questions name the step and its params. So you can see what the task
called, which row decided it, and what happened.

## A block gives verbs

A spawn's `environment.systems` gives a task what it may do, one block per system (or per
repository, for the systems that name one). A block names the verbs it gives, each with its
shapes: `git: {repo: acme/app, fetch: {}, push: {ref: "refs/heads/verse/**"}}`.

**A block that names no verb gives every verb of its system, any call**: `files: {}`,
`question: {}`, `git-issue: {repo: acme/app}`. Once one verb is written, only the verbs written
are given: `git-issue: {repo: acme/app, pull: {}}` gives `pull` alone. The network is apart:
`network: {mode: open}` is its mode, and under `allow` its `tcp-client` shapes name the hosts.

A runner's ceiling is written the same way, and an empty ceiling block allows every verb of its
system. A `git` block can be written empty too, and then gives every push, forced or deleting,
of any branch; a charter that ships writes its git push shapes instead.

| System | Verbs | Its iod |
|---|---|---|
| [`git`](git.md) | `push`, `fetch` | [iod git](../reference/iod.md#git) |
| [`git-issue`](git-issue.md) | `create`, `comment`, `close`, `pull`, `push` | [iod git-issue](../reference/iod.md#git-issue) |
| [`github`](github.md) | `create-pr`, `update-pr` | [iod github](../reference/iod.md#github) |
| [`github-issue`](github-issue.md) | `create`, `comment`, `close` | [iod github](../reference/iod.md#github) |
| [`linear-issue`](linear-issue.md) | `create`, `comment`, `close` | [iod linear](../reference/iod.md#linear) |
| [`slack`](slack.md) | `post`, `reply`, `read`, `react` | [iod slack](../reference/iod.md#slack) |
| [`question`](question.md) | `ask` | none: a question goes to the docket ([asking](../asking.md)) |
| [`files`, `commands`, `agent`](inside.md) | `write`; `run`; `call-sub-agent` | none: these are what an agent does inside its run |
| [`network`](inside.md#the-network) | `tcp-client` | none: the connections a run opens through its runner's proxy |

The runner performs the verbs of `git`, `git-issue`, `github`, `github-issue`, `linear-issue`
and `slack`. It uses its own credential, and stays within the ceiling its configuration puts
over each system ([the verse runner](../reference/runnerd.md#configuration)).
