# slack

The `slack` system is the channels of a Slack workspace that a bot is in. A charter reads the
channels and writes to them through the iod. A task posts, replies, reads and reacts in them
through its runner.

## What a charter sees

`iod slack` writes into the store each channel the bot is in, a window of each channel's recent
messages, and the workspace's people:

| Entity | What it is |
|---|---|
| `slack.<ws>.channel.<C…>` | a channel: its name, whether it is private or archived, its topic |
| `slack.<ws>.message.<C…>."<ts>"` | a message in the window: its text, its author, its thread and its reactions |
| `slack.<ws>.user.<U…>` | a person: their name and email |

A rule reacts to a message as it reacts to any entity:

```tony
condition:
  subject:
    path: "slack.acme.message.C0456.*"
    value: {reactions: {shipit: null}}
```

[iod slack](../reference/iod.md#slack) has the flags, the scopes and the whole payload.

## What a charter writes

A message. With `-out`, the iod carries a rule's writes back to Slack: it posts what a rule
writes under `slack.<ws>.out` ([the out half](../reference/iod.md#the-out-half--out)). So a
rule can say something in Slack with no task. This is also how a question reaches a person in
Slack, and how the person's answer comes back.

## What a task can do

A task gets Slack from its runner, when the runner holds a bot token, and from its environment: a
`slack` block gives the verbs, its shapes the channels. The autonomy decides each call within
it:

```tony
spawn:
  agent: {name: verse}
  environment: {systems: {network: {mode: open}, slack: {post: {channel: deploys}, read: {channel: deploys}}}}
  prompt: "Post the release notes for v1.3 in #deploys, then read the thread for objections."
  autonomy:
  - {match: {system: slack, verb: post, channel: deploys}, then: ask}
  - {match: {system: slack, verb: read, channel: deploys}, then: allow}
  - {match: {system: slack}, then: deny}
  - {then: ask}
```

| Tool | What it does | Step |
|---|---|---|
| `slack-post` | posts a message in a channel, and returns its ts and link | `slack.post`, with the param `channel` |
| `slack-reply` | replies in a thread, under the message whose ts is the thread's | `slack.reply`, with `channel` and `thread` |
| `slack-read` | reads a channel's recent messages, or a thread's, newest last | `slack.read`, with `channel` and `thread` |
| `slack-react` | adds a reaction to a message, by its ts | `slack.react`, with `channel` and `emoji` |

Every verb is a step, a read too. What a channel holds is not the task's until the task reads
it, and an environment that gives no read of a channel keeps it from the task.

**A channel is named by its name**, without the `#`: `deploys`, never its id. The runner finds
the name among the channels the bot is in. The channel the environment and the autonomy decided
about is the channel the runner posts in.

**Every message a task writes ends with a line that names the task**, `verse task <id>`. All
tasks on a runner post as one bot, so the line says which task wrote each message. The iod
reads the line with the message, so a rule can tie a message back to its task. A reaction has
no such line.

A script calls the same tools with `verse-call-tool`, and the same rows decide them:

```sh
verse-call-tool slack-post -channel deploys -text "v1.3 is rolling out"
```

## Where the credential lives

**The iod** needs a bot token, `SLACK_BOT_TOKEN`, with the scopes that the
[iod reference](../reference/iod.md#slack) lists.

**The task** holds no credential. The runner makes each call with its own bot token: the
`SLACK_BOT_TOKEN`. Without it, a runner whose configuration names `slack` in a doer's ceiling
starts with the system closed, and a spawn that needs it is refused where it is written. The bot needs
`chat:write`; `channels:read` and `groups:read` to find a channel by its name;
`channels:history` and `groups:history` to read; and `reactions:write` to react. The iod and the
runner may use the same bot's token or two bots' tokens. Either way, a bot reaches only the
channels it is in.

**The operator** sets a limit on what any task may do in Slack, in the runner's configuration:

```tony
systems:
  slack:
    credential: SLACK_BOT_TOKEN
    ceiling:
      post: {channel: deploys}
      reply: {channel: deploys}
      read: [{channel: deploys}, {channel: "eng-*"}]
      react: {channel: deploys}
```

Verse refuses to install a spawn whose environment gives posting to another channel. The refusal
says what the ceiling gives ([the verse runner](../reference/runnerd.md#what-it-offers)). A call is
still held to the ceiling when the runner makes it, whatever the autonomy said.
