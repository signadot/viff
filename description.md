# design: Claude with verse — how cost, a governed session and sub-agents fit together

Design for discussion. No branch of its own: this ties together verse/claude-cost, verse/claude-gates and verse/claude-subagents.

# Claude with verse: how it fits together

This page assumes two pieces are built: [a governed session you can attach to](#), and
[sub-agents governed through hooks](#). Each step below says what makes it work, and marks what is
assumed.

## One piece of work, end to end

A PR opens on a repository a charter watches, and its rule spawns a review.

1. **The rule fires and names where the work runs.** The spawn names Ana's runner, because the
   rule's owner may read it. Verse checks that read as the rule's owner and refuses otherwise.
   *Works today: 13-check probe on `tc4qyrs0`.*
2. **The work costs Ana's seat.** Her runner opens a terminal for the review and starts Claude in
   it with her own login. Her token never enters verse. *Works today, as `claude -p`; assumed as
   an interactive session.*
3. **Anyone allowed can look in.** The session is a real terminal on Ana's runner, and it's
   reachable over Remote Control from claude.ai or her phone. *Assumed: a governed session.*
4. **Claude reads freely, can't push, and asks before it edits.** The review runs under a grant and
   a vector. Reads are inside the grant and `auto`, so nothing is asked. A push is outside the
   grant: the runner refuses it, and Claude is told why. An edit is `ask`: a permission prompt
   opens, and the question goes to verse at once. *Assumed: a governed session.*
5. **Ana answers wherever she is.** The question is on her docket and in Slack, in the review's
   session for anyone attached, and in her own Claude session through verse's tools. She answers
   from her phone. The first answer wins, and the edit goes ahead. *Assumed: a governed session.*
6. **Claude fans out.** It spawns three sub-agents to check tests, docs and API compatibility. The
   spawn is a delegation, so verse asks about it or allows it under the grant. Each sub-agent
   appears as a child of the review in `verse flow show`. The docs checker is held to read-only.
   *Assumed: sub-agents governed through hooks.*
7. **Ana steers.** She tells the review to skip the generated files. The message reaches the
   session as a new instruction. *Assumed: a governed session.*
8. **Everything is on the record.** Each gate, each answer and who gave it, each child and its
   result are in verse. So "what did this review do, and who allowed it" has an answer afterwards.

## Why this should work, and not just read well

- **The Claude Code mechanisms have been exercised**, in probes against Claude Code 2.1.281:
  - one interactive, Remote Control session carrying the runner's hooks, verse's MCP server, a
    channel and the task's frame;
  - a grant refusal through `PreToolUse`;
  - a question answered by the `PermissionRequest` hook;
  - the same question answered by verse through the channel after the hook timed out, with the
    dialog held open meanwhile;
  - steering through the channel;
  - the final result through `Stop`;
  - a sub-agent spawn gated and refused through `PreToolUse`, children recorded by `agent_id`,
    context added to a child, a child's own calls and permission prompts reaching verse, and two
    children at once.

  Not yet exercised: answering from a phone, and a hook timeout longer than its 600-second
  default.
- **Nothing depends on Claude choosing to cooperate.** The runner owns the settings and the hooks,
  and a refused call is refused.
- **Every mechanism it leans on in verse exists, and the new work reuses it:**
  - the gate record and the docket;
  - resolvers, and answering as a person (Slack);
  - the grant and the vector on a launch;
  - children with a narrowing grant;
  - a runner's permissions deciding who may spend it.

  The new parts are adapters, not a second model.
- **The limits are stated rather than discovered later:**
  - channels are a research preview;
  - a relay verdict carries no reason;
  - a live session lasts only as long as its runner;
  - a sub-agent can't run elsewhere or be stopped on its own.

## The order it gets built

1. **The session.** One terminal per task, Remote Control, the runner's settings, the frame, and
   the result through `Stop`, with no gating yet. It's attachable from day one, and it closes "a
   foreign agent's run narrates nothing" (`1qdmzhac`).
2. **The grant.** `PreToolUse` refuses what is outside it.
3. **The questions.** Permission prompts raised in verse, through the hook and the channel,
   answered on the docket. Steering through the channel.
4. **Sub-agents.** The spawn as a delegation gate, children on the record, and a narrower grant
   per child.
5. **Answering from your own session.** Can go at any point, because it depends on none of the
   above.

Each step leaves `main` working and is useful alone.