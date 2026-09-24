# verse/claude-subagents

Design for discussion. Branch `verse/claude-subagents` starts at main ac9961fc with nothing on it yet; the design below is what it is for. The probe it cites is attached.

# Claude with verse: sub-agents governed through hooks

## The problem

Claude Code delegates. It spawns sub-agents with its `Agent` tool, often several at once, and they
run inside its process. Today verse sees the whole tree as one opaque run. It can't:

- see the sub-agents;
- give one less authority than its parent;
- ask about a delegation before it happens;
- say afterwards which sub-agent did what.

Verse already has this shape in its own agent loop. A delegation is a child task, and its grant
can only narrow (`Grant.Subset`, checked where the child is made). The gate over a delegation
offers the child's supervision mode: `proceed`, `careful` or `autonomous`
(`docs/reference/tasks.md`). By contrast, Google's Agent Executor pitches tasks as a tree an agent
spawns, but has no parent link and no narrowing: a task that can reach its API makes a peer.

## The proposal: keep Claude's sub-agents, and govern them with hooks

Claude keeps its built-in `Agent` tool. The runner's hooks, the same ones that govern the session, cover sub-agents too:

- **The spawn is a delegation gate.** Spawning is an `Agent` tool call, and `PreToolUse` sees its
  prompt and its sub-agent type. The runner treats it as verse treats a delegation: refused outside
  the grant, and otherwise asked about with the three supervision modes, or allowed. The hook can
  also rewrite the call's input.
- **Each sub-agent is a child on the record.** `SubagentStart` fires with the sub-agent's
  `agent_id` and type. The runner reports it as a child of the task, the same `child` event verse
  already reports for its own sub-tasks, so `verse flow show` lists it. The hook can't block
  creation, but it can add context to the sub-agent, such as the limits it runs under.
- **Each sub-agent can have less authority.** Every tool call a sub-agent makes fires the same
  `PreToolUse` and `PermissionRequest` hooks, carrying its `agent_id`. So verse can hold a child to a
  narrower grant than its parent, and in `careful` mode can ask about each of its actions.
- **Each sub-agent's result is recorded.** `SubagentStop` carries the sub-agent's final message and
  its own transcript path.
- **Policy can differ by kind of sub-agent.** Hooks can also live in a sub-agent's definition, and
  run only while that kind of sub-agent runs.

Nothing here disables anything Claude does, and nothing depends on Claude choosing to call a
different tool. The work still runs in the same session, on the same runner and the same seat or
key.

## What this does not do

A sub-agent governed through hooks still lives in its parent's process, so:

- **It can't run somewhere else**, such as another runner, a cheaper model, or its own isolation.
- **It can't be stopped on its own from outside.** Claude Code documents no way to cancel one
  sub-agent from outside; a hook can only refuse its next tool call.
- **It ends with its parent session.**

If any of these turns out to matter, the next step is to remove the `Agent` tool and offer
delegation as a verse MCP tool that starts a real child task elsewhere. A probe showed that works:
with `Agent` removed, Claude delegated through the MCP tool on its own. That is deliberately not
this proposal.

## Evidence

- Claude Code's hooks reference documents each piece:
  - `Agent` is a tool call that `PreToolUse` sees and can deny or rewrite;
  - `SubagentStart` and `SubagentStop` carry `agent_id`, and `SubagentStop` also carries the final
    message and transcript;
  - "hooks … also run inside subagents", with `agent_id` identifying the sub-agent;
  - hooks can be declared per sub-agent.
- Verse's loop already builds children, narrows their grant, offers the modes and reports `child`
  events (`task/loop.go`, `docs/task-control.md`).

A probe against Claude Code 2.1.281 on 2026-09-24, in an interactive Remote Control setup, with `Agent` kept:

1. **The spawn gate.** `PreToolUse` fired on the `Agent` call, carrying `subagent_type`. A deny
   stopped the spawn before any sub-agent existed, and Claude reported the reason without retrying.
2. **The child on the record.** `SubagentStart` and `SubagentStop` fired with the sub-agent's
   `agent_id` and type. Context added at `SubagentStart` reached the child: it quoted it back.
   `SubagentStop` carried the child's final message.
3. **The child's own calls.** The sub-agent's `Bash` call reached `PreToolUse` and
   `PermissionRequest`, both carrying its `agent_id`.
4. **The child's questions.** The child's permission prompt opened in the session, labelled "from
   the general-purpose agent", and reached verse through the channel relay. Verse's verdict there
   was applied.
5. **Fan-out.** Two sub-agents launched together got distinct ids, and their interleaved tool calls
   each carried the right one.

## Gaps and risks

- **Other ways Claude runs sub-agents.** Skills that run in a sub-agent, dynamic workflows, and
  agent teams may reach these hooks differently. Not probed.
- **Sub-agents run in the background.** The parent's `Stop` fired while the child was still
  running. `Stop` lists the running child in `background_tasks`, so the runner must read that
  rather than treat the first `Stop` as the task's end.
- **Not every `SubagentStop` is a child.** Claude Code also reports its own internal agents, with an
  empty `agent_type`. The runner records only ids it saw start.
- **Fan-out.** Several sub-agents at once means several `SubagentStart` events and interleaved
  tool calls. The per-child record keys everything by `agent_id`.