# verse/mcp

`verse mcp`: the user face of verse as an MCP server (55crejnf, a5mvads7, v27eah17, kqa3sawf). The host of a person's agent starts it; it talks on stdin/stdout and acts as the person, the way every other `verse` subcommand does. Three phases, each a PR off `verse/mcp` that can land alone.

## What holds across the phases

- **One command, in the CLI's package.** `cmd/verse/mcp.go` registers `mcp` beside the other verbs. The server is built with `github.com/modelcontextprotocol/go-sdk` (v1.8.0, what `git issue mcp` is built on) over `mcp.StdioTransport`. Address, scope, output format, login and principal come from the same config the CLI reads; `verse mcp --addr … --as …` are the person's flags, set by the host's command line, never by the agent.
- **Tools add no operation** (a5mvads7 rule 1). Each tool calls the request a `verse` verb makes. Where a verb's run function builds its request inline (the `question` query, the `ask` body), that part is pulled into a function the verb and the tool both call. A tool whose operation the CLI does not have gets the CLI flag first.
- **No tool names an identity** (v27eah17). The principal is `principal("")`, the bearer the login's. A verse that refuses a nobody-write refuses the tool the same way.
- **Reads and writes are separate tools, by name**, so a host can allow one set and refuse the other. Reads carry `ReadOnlyHint: true`; writes say in their description what they write, with the question, the answerers, the deadline and the consequence as separate parameters so the host's confirmation is readable.
- **Names follow the verbs**: `verse_<verb>` reads, `verse_<verb>_<action>` writes — `verse_data`, `verse_data_put`, `verse_question`, `verse_question_answer`. A reader of the command reference maps one onto the other.
- **A watch is as wide as what it names, and the tools keep them narrow.** The cost of a watch on the store is its width, not its count: one task is nothing, every task spikes, every task and everything the iods reflect in does not scale. So this is the one place the tools do not mirror the CLI: there is no `verse_watch` at an arbitrary address in any phase. What waits, waits on one thing — `verse_task_events` on one task, `verse_wait` on the asks subtree, whose throughput is what a small team with a limited verse budget can consume — as narrow as one task, or narrower — and a wide question is a read (`verse_data` with `if`, `verse_task`, `verse_flow`), asked once. If a watch tool at an address is ever wanted, it takes a literal prefix below the top-level systems and refuses the root and the subtrees the iods fill, and the refusal says why.
- **Answers are what `verse` prints**: text content in the output format (tony by default; `--ofmt yaml` on the server's command line changes it). A 404 is an answer in the server's words (not in view), as rule 2 says; a 403 is a refusal in its words; a transport failure is an error. A record is never cut (rule 3): an answer past a size the server states says so and answers the paths instead.
- **Tests at three ranges.** In-process: `mcp.NewInMemoryTransports` connects a go-sdk client to the server against a versed started as `serveAskingAt` starts one (entitytest store, `ScriptedBuilder`), so every tool is exercised through MCP against a real engine. Binary: a `smoke.sh` step drives `verse mcp` on stdio with an initialize, a `tools/list` and one call. Host: the acceptance walkthrough for each phase runs from Claude Code against the throwaway local verse.

## Phase 1 — `d | ask | qs | task`, end to end

| tool | binds to | writes |
|---|---|---|
| `verse_data` (address, if, trim, paths) | `data [<step>…] [--if] [--trim] [--paths]` | no |
| `verse_data_put` (address, value, create\|update\|expect, grants) | `data … --put` | yes |
| `verse_data_rm` (address) | `data … --rm` | yes |
| `verse_question` (id?, all, served, if, trim) | `question`, `question --all`, `question <id>` | no |
| `verse_question_answer` (id, option, say, force) | `question <id> --answer <option>` | yes |
| `verse_ask` (name_or_id?) | `ask`, `ask <name>` | no |
| `verse_ask_create` (name, question, to, due, on_silence, then, else, about, keep, label) | `ask <name> -q …` | yes |
| `verse_ask_change` (name_or_id, due, to, then, else) | `ask <name> --due/--to/--then` | yes |
| `verse_ask_withdraw` (name_or_id) | `ask <name> --withdraw` | yes |
| `verse_task` (id?, runner, if, trim) | `task`, `task get <id>` | no |
| `verse_task_events` (id, since) | the stream `task attach` reads, answered to a cursor | no |
| `verse_task_run` (prompt, name, subject, agent\|script, policy rows, network, git, runner, answer_contract, virtual_key) | `task run` | yes |
| `verse_task_policy` (id, rows) | `task policy` | yes |
| `verse_flow_suspend` / `verse_flow_resume` (task) | `flow suspend --task` / `flow resume --task` | yes |
| `verse_flow_say` (task, text) | `flow say --task` | yes |
| `verse_flow_cancel` (task) | `flow cancel --task` | yes |
| `verse_wait` (since, timeout) | `ask --watch` / `question --watch` | no |

`verse_wait` is the one that waits: it answers when an ask of the person settles or a question starts waiting on them, with a cursor to pass back, or with none at its timeout. The timeout is the one departure from a5mvads7 (rule 5 wanted no limit): `git issue mcp`'s `issue_watch` has shown a bounded wait plus a cursor loses nothing and works in the hosts that exist, so the same shape here (default 300 s, cap 3600 s, hosts set what they can hold). Its design is below, since it is the one tool whose shape is not a request the CLI makes.

### `verse_wait`, specifically

**The problem.** `verse ask --watch` is correct because the process keeps a copy of every ask record (`askWatch.recs`, `cmd/verse/ask.go:632-658`): a frame is said as what changed from the record held, so a stream followed again from `last-1` says nothing twice (`:539-542`), and a watch from now opens with `init` so each record's first copy is whole (`:476-480`). None of that is in the cursor on the wire; it is in the process. A tool call returns, so a `verse_wait(since)` that built a fresh `askWatch` from `since` would have no copy: the route refuses `init` with `since`, a record met that way lacks every field written before, and an answer between two calls is said against nothing, or twice. The happy-path test (one ask, one answer, one wait) never sees this.

**The design: the server keeps the watch alive.** `verse_wait` answers the lines `verse ask --watch` would have printed, numbered. One `askWatch` lives for the `verse mcp` process; the tool reads its output. Nothing in `askWatch` changes.

- The watch opens on the first `verse_wait` call, not at server start, so a session whose agent never waits holds no store watch. It is never closed after that: `fromNow` suppresses "answered" on asks standing when a watch opens, so a watch closed and reopened would lose an answer that landed meanwhile. Cost on the store: one subtree watch on `verse.ask` per session (`watchFor` → `WatchPattern` → one libctl watch), the same as one `verse ask --watch`, on a subtree whose rate is people answering people; every reopen is an `init` read of it, bounded by the watch's own 1–30 s backoff.
- When it opens, after flags: `me, _ := principal("")` (`--as` on the `mcp` command line has set `asOverride` by then, `task.go:945-955`). `w := newAskWatch(me, true, true)`; `w.askVerse = waitingOnMe` — exactly `watchAsks` (`ask.go:448-468`), with `say` replaced.
- A goroutine runs `w.follow(ctx, "", say)` for the server's life, `ctx` the one `Run` gives the transport. `follow` returns nil only when `ctx` ends; it returns an error when a stream never opened (`:508-512`, a verse down at start, or refusing). On an error the goroutine appends the error's text as a line, waits out a backoff (`waitOut`, `:556`, same package), and calls `follow` again; it stops only on `ctx.Err()`.
- `say(rev, line)` appends to a `waitLog`: `mu sync.Mutex`; `lines []waitLine{seq uint64, rev, line string}`; `next uint64` (the next seq); `cap` (`waitLogCap = 1024`, a package var so a test can set it to 3); `wake chan struct{}`, closed and replaced under the lock on every append, which is the broadcast to every blocked call. Every line the watch says is a line in the log — the events, the gap lines, and the watch's own notes about its stream (`rev == ""`: "the verse is not answering", "the stream ended; following from here"). They are kept because `verse ask --watch` shows them to a person, and a reconnect is worth one wake. The log keeps the last `cap` lines and the seq of the oldest it still holds.
- `verse_wait(since string, timeout int)`:
  1. `me == ""` (nobody configured, a remote verse): answer at once, `isError`, "nobody is configured, so nothing can wait on you: set `user:` in the config or start `verse mcp --as <you>`".
  2. `timeout` ≤ 0 → 300; > 3600 → 3600.
  3. `since == ""` → from seq 0: everything the log holds since the server started. This is deliberate and differs from `issue_watch` ("from now"): the watch opened with `init`, so the first lines are "waiting on you …" for each question already waiting when the server started, and an agent's first wait is the docket. `since` given → lines with `seq > since`; `since` older than the oldest kept → `isError`, "cursor %s is older than the %d lines this server keeps; call without since, and verse_question to catch up" (the shape `git issue mcp` uses).
  4. If there are lines: answer them. Else: take `wake` under the lock, release, `select` on `wake`, `time.After(timeout)`, `ctx.Done()`; on `wake` go to 4; on timeout answer no lines; on `ctx` return `ctx.Err()` (the host cancelled the call).
  5. The answer: `{lines: [{rev, line}], cursor}` as the typed `Out` of `mcp.AddTool[In, Out]` (structured content comes with it) rendered as text in the server's format. `cursor` is the seq of the last line answered, or `since` unchanged when none, so a call never moves the cursor past what it said.
- No `verse_wait` short-circuits on a dead verse: the watch's own "not answering" line is what wakes the call, so a dead verse is reported within one wake, as it is to a person.
- Threading: `frame` calls `servedBy` and `waitingOnMe` over HTTP under `w.mu` (`:713`, `:789-795`); unchanged, and the only lock a tool call takes is the log's.
- The tool's description says all of this in three sentences: what wakes it, what `since` is, and that without `since` it answers what has stood since the server started.

**Tests** (in-memory transports; a versed as `serveAskingAt` starts one; the CLI's `askAs`/`runCLI` write the record, the tool reads it — the two sides of every test are different clients):

1. `TestWaitSaysWhatWaitsAtStart`: daniel asks davido, then davido's server starts; `wait()` answers one "waiting on you <qid>: …" at once, with a cursor.
2. `TestWaitSaysWhatArrives`: davido's server starts first; `wait(timeout: 10)` blocks; daniel asks; it answers the one line.
3. `TestWaitMissesNothingBetweenCalls` (the test that motivates the design): daniel's server; daniel asks twice; `wait()` → two "asked" lines, cursor c1. With no call in flight: davido answers the first, daniel withdraws the second (both via the CLI). `wait(since: c1)` → "answered … yes" and "withdrawn …", in that order, each once. `wait(since: c2, timeout: 1)` → no lines, cursor c2.
4. `TestWaitSaysAnAnswerOnce`: an ask standing before the server started, answered after: "answered" said once (the copy from `init` is what makes it said at all, `:750-752`), and not again on a later wait.
5. `TestWaitRefusesACursorTheServerForgot`: `waitLogCap = 3`, four lines, `wait(since: 0)` → the error naming the cap.
6. `TestWaitFollowsAgainWhenItsStreamEnds`: `ts.CloseClientConnections()`; the next ask is still said, and the "stream ended" note is one line (the model is `TestTheWatchFollowsAgainWhenItsStreamEnds`, `ask_test.go:518`).
7. `TestWaitReturnsWhenTheHostCancels`: cancel the call's context during a block; it returns promptly.
8. `TestWaitWithNobodyConfigured`: `VERSE_USER` cleared, remote address → the `isError` answer.

**Deferred, on purpose:** the CLI's one-command-per-process assumptions about credentials (`logins` read once, `login.go:80-81`; the bearer's renewal of an expired id_token) are not reworked in phase 1. A long `verse mcp` session on a verse with logins is known to be untested there.

Control of one task is spelled as the CLI spells it, under `flow`: the four control tools exist from phase 1 and take the `task` selection alone. Phase 3 widens the same tools to `flow`'s other selections (a charter and a rule, a subject, recursive) and adds the two `flow` reads; no control verb is added later. `task say` and `task cancel` are not mirrored as tools of their own.

Not a tool: `task attach` (a stream; the agent reads `verse_task_events` and waits), `task run --attach/--wait`, and every `--as`.

Steps:

0. `verse mcp` with `verse_data` alone: the dependency, the transport, config → client, the answer rendering, the error mapping. Test: an in-memory client lists the tool and reads an address the CLI wrote.
1. question + ask + wait. Test: `verse_ask_create` as one person, `verse_question` as the other sees it, `verse_question_answer`, `verse_wait` reports the settle at the cursor; a withdraw; a second wait returns none at a short timeout.
2. task, and flow control by task. Test: `verse_task_run` with a script doer, `verse_task_events` to a cursor, `verse_flow_say` shows up as an ask the task hears, `verse_flow_suspend` holds it and `verse_flow_resume` lets it continue, `verse_flow_cancel`.
3. The smoke step, `docs/reference/mcp.md` (the tools by phase, the host's command line, the identity rule) and its row in `docs/reference/README.md`; a comment on a5mvads7 recording the names and the wait's shape.

Acceptance: from Claude Code with `verse mcp` configured against the local verse — ask a colleague's identity something with a deadline, see it on the docket as them, answer it, see the wait return; run a script task and cancel it.

## Phase 2 — `c` read-only, and the data around a charter

| tool | binds to | writes |
|---|---|---|
| `verse_charter` (address, depth, paths\|kpaths, if, trim) | `charter <steps> [--depth] [--paths]` | no |
| `verse_charter_check` (document(s), at, prune) | `charter --check` (new flag over `POST /charter/check`) | no — a question, never an install |
| `verse_status` | `status` | no |
| `verse_schema` (name?) | `schema [<name>]` | no |

The analysis is done by the agent from reads, so the phase adds reads and sequences them, not an operation:

- `verse_charter_check` is the one read the CLI lacks: a verdict on a document before anything is installed, with the rules and strata it would add, change, remove or hold. The CLI flag comes first (step 4), the tool binds to it.
- `verse_data` with `if` is the selector the server applies: "which entities match this rule's condition now" is a read, not a feature.
- `verse_task` and `verse_question --all` filtered by the charter's id prefix answer "what did this rule spawn, what is it asking".
- Two MCP prompts (no operation; a prompt is a script of reads): `explain-charter` (address → the charter at depth, its rules as paths, what each matches now, what it spawned) and `why-not` (address, subject → the rule's condition against the subject's record, the questions and suspensions standing on it).

`flow` (bare) and `flow show` are reads too; they stay in phase 3 as asked, and come forward if `why-not` turns out to need the subject's tree. `flow` control exists from phase 1, by task.

Steps:

4. `verse charter --check` in the CLI, with its test.
5. The four tools and the two prompts. Test: a charter installed by the CLI read at depths 0, 1, -1 and as paths; a check of a document that installs and of one that is refused; status after a firing.
6. Docs; a comment on kqa3sawf answering its question 1 (the first version reads).

Acceptance: from Claude Code, "what does the merge charter do, and why has pr 7 not moved" answered from reads alone.

## Phase 3 — `c` writes, and `flow`

| tool | binds to | writes |
|---|---|---|
| `verse_charter_install` (at, document(s) \| dir, prune) | `charter -a -f` / `charter -a -d [--prune]` | yes |
| `verse_charter_remove` (address) | `charter -r` — one package or one rule; `-R` stays on the command line (kqa3sawf) | yes |
| `verse_flow` (task \| charter[:rule] + recursive \| subject, if, trim) | `flow` | no |
| `verse_flow_show` (subject, active) | `flow show` | no |
| `verse_flow_suspend` / `_resume` / `_say` / `_cancel`, widened to charter[:rule] + recursive and subject | `flow <verb> --charter/--subject` | yes |

An install answers what verse answers (kqa3sawf rule 3): the warning, the held rule. Whether an agent proposing a rule should be a rendezvous to the place's owner instead of an install (kqa3sawf question 2) is left open; this phase ships the tool, and the host's confirmation is the gate until the record wants more.

Steps:

7. charter install and remove. Test: install from text, read it back, install a tree with prune, remove one rule, a recursive remove refused by name.
8. flow: the two reads, and the control tools' charter and subject selections. Test: a suspension on a charter taken through the tool holds its next firing, which `verse_flow` and the CLI both show; resume releases it; `verse_flow_show` answers the subject's tree.
9. Docs; the branch issue closed with the three PRs.

## Branch and issues

`verse/mcp` in `.worktrees/mcp` off main, with a `branch` issue holding this plan; every commit carries the issue. Phase 1 is its own PR so the first host can use it while 2 and 3 are built. The command is new and breaks nothing, so what lands on main declares `Tier: change` (repository.md: a tier is declared where the work lands, not on each branch commit).