# verse/claude-runner

Objective: a claude runner, deployable and running on staging, offering `doer: {agent: claude}`.

Worktree `.worktrees/claude-runner`, branch `verse/claude-runner`, off main 55af9728.

## Shape

A `claude` agent in `runner/runnerd/claude`, beside codex (`runner/runnerd/codex`) and built the same way: Claude Code is driven over its stream-json control protocol (`claude -p --input-format stream-json --output-format stream-json --permission-prompt-tool stdio`), so every action it proposes is an action of the task's loop, decided by the policy, put on the docket when it asks, and narrated.

- `can_use_tool` is the proposal. Bash is `commands.run`, Edit/Write/NotebookEdit `files.write` on their path, AskUserQuestion `question.ask`, a runner tool its own step. Allow is `allow` with the input; a refusal is `deny` with the reason, which the model reads.
- Read/Glob/Grep ask nothing: free reads, narrated, attested as codex's are.
- The runner's tools are an in-process MCP server (`"type":"sdk"`, `mcp_message` control requests), the counterpart of codex's `dynamicTools`.
- The runner owns the settings: `--setting-sources ""`, `--strict-mcp-config`, `--tools <the mapped set>`. A tool without a mapping is refused.
- Steering is a user message on stdin; cancel is the `interrupt` control request; the result is the final `result` message.

The interactive "governed session" (Remote Control, hooks, channels; Notion "verse+claude umbrella", js1e345a) is a later layer on top, not this branch.

## Probe, Claude Code 2.1.304, 2026-10-20 (local, haiku)

- Bash arrived as `can_use_tool` with the command; Read arrived as nothing; an sdk MCP tool arrived as `can_use_tool` then `tools/call` over `mcp_message`.
- A workspace `.claude/settings.json` allowing `Bash(*)`, setting `bypassPermissions` and adding a PreToolUse hook had no effect under `--setting-sources ""`: Bash still asked and the hook never ran.
- `--tools Bash,Read,Edit,Write,Glob,Grep` made the session's tools exactly those plus the sdk server's.

## Credential

Start with an Anthropic API key (`apiKeyEnv`, Secret `verse-claude`), as codex starts with `verse-codex`. Seats (a staging seat, or developers letting their seats run on staging) are to be evaluated and the config shaped for them early; the evaluation goes on this issue.

## Steps

1. The wire and the agent, tested against a stand-in claude as codex's tests use a stand-in codex.
2. verserunner: the `claude:` section, the offer, the inner systems, the model host.
3. `deploy/claude-runner` (image with a pinned claude, manifest), `claudeRunner` in build.tony and the staging profile, `ship-runners`, README.
4. Proven against a local verse, then shipped to staging and registered: `verse runner add claude http://claude-runner:9802 --identity claude-runner`.