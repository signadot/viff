# Claude and Codex hold a conversation: their offers say converse

`verse task run -f <spawn for verse.runners.claude> --attach` is refused on staging (deploy/staging.24):

    task run: 400 Bad Request: spawn: verse.runners.claude: converse: claude on this runner can't hold a conversation

and codex the same. An attached run asks for a conversation by default (`-converse`), so the only way to attach to either is `--no-converse`.

The refusal is the offer's: `verserunner.Config.Describe` sets `converse: true` on verse's own agent alone, the line dating from the cut (#24), before the claude runner existed; #31 copied codex's offer. Nothing in the design keeps a conversation from Claude or Codex, and both drivers already hold one: each implements `task.Conversation` (Fold), and takes words between turns as a new turn (`TestALateSayIsTheNextTurn`, `TestCodexSteerReachesTheRunningTurn`). `SetConverse` is applied in `InProcessHandler.host` for every agent.

The fix: every agent the verse runner offers says `converse: true`; a script still doesn't. Tests: a claude run and a codex run as conversations through the loop, the turn handed to the person when the agent goes quiet, their reply reaching the agent as its next turn, ending when they end it; the description says converse for each agent.