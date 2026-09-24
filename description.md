# verse/claude-gates

Design for discussion. Branch `verse/claude-gates` starts at main ac9961fc with nothing on it yet; the design below is what it is for. The probe it cites is attached.

# Claude with verse: a governed session you can attach to

## The problem

When verse runs Claude Code on a runner today, it runs `claude -p` with `bypassPermissions`. Verse
gates starting Claude, and nothing Claude does after that. What the task was allowed to do is
attested, not enforced. And nobody can look in: the run is a subprocess with no terminal, so a
person who wants to see or steer it has to go through verse.

A second problem is where people answer. A developer's questions sit on verse's docket, in a
terminal or in Slack, and not where they're working. That one is the user face's to solve
(a5mvads7), not this design's: see "Answering from your own session" below.

## The shape: one interactive session per task

The runner gives each task its own terminal and starts an ordinary interactive Claude session in
it:

```sh
claude --remote-control <task> --permission-mode default \
  --settings <the runner's hooks> \
  --mcp-config <verse's MCP server for this task> \
  --channels <verse's channel> \
  --append-system-prompt-file <the task's frame> \
  -- "<the task's prompt>"
```

- **Anyone allowed can attach.** It's a real terminal on the runner, and Remote Control makes the
  same session reachable from claude.ai or the Claude app.
- **The runner owns the settings,** so the hooks are the runner's, not something the agent can
  edit in the workspace. The permission mode is set explicitly: some accounts start in auto mode,
  where a classifier approves most calls before any gate would see them.
- **The task's frame** is appended to the system prompt rather than pasted in front of the user's
  prompt. It tells Claude it is running as a verse task and that a refusal from verse is final.
- **Verse's MCP server** is the channel between the session and verse: it carries tools, steering
  messages in, and permission prompts out.

## How verse gates what Claude does

Two hooks and one MCP channel, each doing one job. The task's grant and vector, which its launch
already carries, decide the answers.

**The grant: a `PreToolUse` hook.** It runs before every tool call. For a call outside the grant it
answers deny, with a reason Claude reads and re-plans around. The runner answers this one locally
and immediately. It must stay fast: if a `PreToolUse` hook times out, the call goes ahead through
the normal permission flow.

**The questions: the permission prompt.** In `default` mode, Claude asks before any call its rules
don't already allow. Inside the grant and `auto`, the runner's rules allow the call and nothing is
asked. Otherwise a permission prompt opens, and it can be answered in three places at once:

- **In the session**, by whoever is attached: at the terminal, or on a phone over Remote Control.
- **By the `PermissionRequest` hook**, which raises the question in verse and waits for its
  verdict, up to the hook's timeout.
- **By verse's channel**, which receives the same prompt through Claude Code's permission relay,
  with a request id. Verse answers with that id, and has no time limit.

The first answer wins, and Claude Code closes the others. The question goes wherever verse's gates
go: the docket, Slack, a resolver. So a question that waits all weekend just waits, with the
session live and anyone allowed able to attach and look.

## Steering, completion, and answering from anywhere

- **Steering.** Verse pushes a message into the session through its channel. An idle session
  takes it as a new turn. This is what `verse task say` becomes.
- **Completion.** When Claude finishes a turn, a `Stop` hook receives its final message, and
  whether background work is still running. The runner gets the task's result from that, without
  reading the transcript.
- **Answering from your own session is the user face's.** "What is waiting on me" and "answer
  this" are tools of `verse mcp`, the user face (a5mvads7), which a person's own host starts and
  which acts as the person. They are not tools of this server. A task's server speaks for its
  task, and a person's docket spans every ask waiting on them, including when no task of theirs
  runs. An agent that answers for a person without them is a resolver the person grants
  (docs/attached-resolvers.md on `verse/ask-next`). This server stays the task's: its tools,
  steering in, and permission prompts and questions out.

## Evidence

A probe against Claude Code 2.1.281 on 2026-09-24 checked every mechanism above except sub-agents.
Its script will be attached to this branch's issue.

- One `claude --remote-control` command carried the settings, MCP config, frame, channel and
  prompt together. Remote Control was active and the frame was applied.
- The `PermissionRequest` hook allowed a Bash call after 3 seconds.
- A hook that stalled past its 20-second timeout rendered no decision. The call did **not** run,
  the dialog stayed open, and a verdict through the channel relay then allowed it. A verdict with a
  stale request id was ignored.
- The relay notification arrived at the same moment the hook started, so they run in parallel.
- `PreToolUse` refused a call outside the "grant" before any prompt opened, and Claude didn't retry.
- `Stop` delivered the final message. A message pushed through the channel started a new turn in
  an idle session.

On verse's side, the launch already carries the grant and the vector, which are resolved on the
engine side. Slack already answers gates as a person (`905cdad1`).

## Gaps and risks

- **Channels are a research preview.** A custom channel needs a development flag, which asks for a
  confirmation at startup that the runner must answer in the terminal. The alternative is an
  approved channel plugin: on Team and Enterprise an admin sets `channelsEnabled` and
  `allowedChannelPlugins` in managed settings.
- **A relay verdict carries no reason.** It is only allow or deny. To tell Claude why it was
  refused, verse either denies through the hook, which carries a message, or follows the deny with
  a channel message.
- **When someone answers in the session, verse's question must be withdrawn**, or it stays on the
  docket after the fact.
- **Tool names are Claude's.** The mapping from Claude's tools to verse's effects is new code, and a
  wrong mapping would widen a grant. It fails closed: a tool with no mapping is refused under any
  narrowed grant, as verse's own loop already does.
- **The session lives as long as the task.** While a question waits, the terminal and the Claude
  process stay up on the runner. That's cheap on a flat seat, but a runner restart ends the live
  session.
- **Not yet checked:** answering from a phone while it's attached, and a hook timeout longer than
  the 600-second default.
