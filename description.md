# ask: a one-off question costs a rule of its own — an ask is a record with a "then", and the then runs as the asker

Every question verse asks is a gate, and every gate is a rule's, so asking somebody one question
— with something to do on the answer — means installing a rule into a shared charter, and ending it
means removing that rule: a durable, named, visible object for something meant to last minutes.

The gap is concrete, not hypothetical. It came up while proving the Slack path:

- Locally I wrote a `ship-ask` rule with a made-up `demo.ship.*` subject, posed one question,
  answered it, and was left with a rule and a subject nobody wants.

## The unit is "ask … then …"

A question on its own is a vote on the record: its answer lands somewhere and nothing acts on it,
which a Slack poll already does. What justifies verse is the consequence. "Bob, can you review
verse/ask this week? If not, any objection? Then: merge verse/ask to main." When the answer is yes,
or silence by the date, the then happens.

"Ask … then …" is a rule — a gate and an action — so every ask carries its then. The then is free
text, and it becomes the PROMPT of a task spawned when the ask settles on yes. Nothing restricts
what the text says: the task is governed step by step, by its vector and its grants, like any
other task, which is where verse keeps governance anyway. What the text MEANS — a task to run, a
direct act on the tree, or orchestration of several — is for a doer to decide, and later an LLM
can decide it: that is provider selection, which already picks a doer at fire time.

## The shape

- **An ask is a record**, `ask.<id>`: `{question, then}`, and with the next step `answerers`
  and a date. Writing it is asking. The Slack door, the Claude (MCP) door and the CLI door all
  write the same record.
- **One standing rule poses every ask** — the "one rule for all of them" shape, rather than a
  nonced rule per question. Its gate's `pose` expands from the record, so each ask has its own
  words; its action is `spawn` with `prompt: $subject.value.then`. A no runs nothing.
- **No teardown and no orphans**: the rule stands, each ask settles and latches on its record.
  The nonced-rule shape is dropped: it brings teardown and survival back for no capability the
  standing rule lacks.

Rehearsed 2026-09-26 on a local verse against Slack: a standing rule over
`ask.*` posed a record's `question` as an open question (waiting on `user:*`, `agent:*`), a second
rule (a channel-addressed companion of gate-ask) posted it to a channel, a reply `proceed` in the
thread answered it, and the answer came back onto the record. The drafts are in the rehearsal's
scratchpad (`ask.tony`, `gate-post.tony`); the vote-only action there is what "then" replaces.

## What it needs

1. **A spawn runs as the asker.** A task is its spawner's, and a rule's spawner is its installer,
   so under a standing rule anybody who may write an ask gets a task acting as the charter's
   owner — and with a free-text then, that is anybody acting as the owner. The spawn must act as
   the ask record's owner, so the then's grants and gates are the asker's. This is the one
   requirement everything else rests on.
2. **Answerers and the date from the record.** Today `answerers`, `deadline` and the options are
   the rule's, fixed and checked at install, so one standing rule is one audience and one clock.
   A natural ask names its person and its date ("Bob … this week"). Reading them from the
   record moves the install-time checks to fire time, where a bad record must fail visibly.
3. **The deadline as a date, and movable by its owner.** "This week" is Friday, not 48h from
   posing. The instant is stamped once at posing today; it must re-arm when the question's owner
   moves it, and refuse to move once the question has settled.
4. **The standing "ask … then" rule**, as a charter.

## What is decided in principle and open in detail

- **Who may ask whom.** With answerers from the record, writing an ask can make verse message
  anybody. What the asker must hold on the answerers is part of the design (their own grantees,
  or a group the charter names).
- **The asker is the record's owner**, not the charter's; the question shows who asked.
- **An extension is an ask back**: Bob asks the asker "Wednesday instead?", and its then moves
  the original's date (needs 3). Silence grants an extension ONCE; a second needs the asker's yes,
  so chains of extensions end and cannot postpone forever.
- **An ask about an ask lives only while that one is open**: its rule's condition reads the
  target as pending, so the target settling withdraws it (a withdrawal says nothing and runs
  nothing). Its clock must fall inside the target's, checked at posing, or it cannot matter.
  A clarification ("do you mean X?") is a reply in the thread, not an ask.
- **Chains are allowed**: each link settles on its own clock, so every chain terminates.
- **The word.** "Ask" for a person's ask and a task's ask (`verse/ask`) alike, one record under
  one name.

## Where people meet it

People have seen gates in Slack and do not know how to make one, and "write a charter" is not a
way in. So the entry is top-down: an ask is the concept, and a gate is an ask a rule raises by
itself.

- **Slack**: the medium is the ask. In a channel whose purpose is asking, a message that names
  somebody and asks them something is an ask; no bot mention, no command syntax. Addressed asks
  reach people by DM (gate-ask); open ones post to that channel.
- **Claude**: a `verse ask` MCP tool and CLI verb writing the same record.
- **One newcomer page**, "Asking in verse".

Open asks (anyone may answer, one clock, a then) need only 1 and 4; addressed asks need 2 and 3.

## Scenarios

What a person meets in Slack, by who raises it, who answers or is acted on, and what follows.
Three kinds: QUESTIONS somebody answers (1–8), QUERIES verse answers from its state (9–13), and
STEERING or COMMANDS that act now (14–18). Queries and commands are not questions, but in Slack
they look exactly like one, so whatever reads a message has to tell them apart.

WHERE follows SEQUENCE. Something that continues an existing flow — an answer, an extension, an
escalation, steering a running task — belongs in that flow's thread, where what came before is.
A one-off, or something that STARTS a sequence, belongs in a channel or a DM. A "then" is what
turns a one-off into the start of a sequence: the ask is one-off, its then is what follows in its
thread.

| # | In Slack | Kind | Raised by | Answered by / acts on | What follows | Where | Sequence |
|---|---|---|---|---|---|---|---|
| 1 | "Merge train for `c1863dc` is ready — ship?" | question | a rule | named people or a group | the rule's own action | DM, thread per question | in sequence: of the merge train |
| 2 | A drift finding: "this comment disagrees with the code — fix, keep, or ignore?" | question | a rule | the code's owner | one of several actions, by option | DM, thread per question | in sequence: of the range review |
| 3 | A running task: "which of these two approaches?" | question | a task (`verse/ask`) | whoever started it | the answer goes back to the task | the task's thread | in sequence: of the task's conversation |
| 4 | "Should we call it runverse?" | question | a person | anyone, or a group, `need: N` | nothing: a vote, the record is the value | channel | one-off |
| 5 | "Bob, review verse/ask this week? then merge it" | question | a person | named | then: a task | channel, or DM to Bob | one-off that starts one: the then |
| 6 | "Can someone take the flaky logd test?" | question | a person | anyone, first to claim | then: the claimer owns it | channel | one-off that starts one: the claim |
| 7 | "Wednesday instead?" on 5 | question | the answerer | the original asker | then: move 5's date | 5's thread | in sequence: of 5 |
| 8 | "Sam, this has waited 2 days" | question | a rule (`escalate`) | the next person up | the original gate's action | the original gate's thread | in sequence: of that gate |
| 9 | "What's waiting on me?" | query | a person | verse's docket | nothing | DM | one-off |
| 10 | "Where's the merge train?" / "show the tasks" | query | a person | verse's state (flows, registry) | nothing | channel or DM | one-off |
| 11 | "What's up with this branch / PR?" | query | a person | the subject's flow | nothing | the PR's thread if it has one, else channel | in sequence when asked in its thread; otherwise one-off |
| 12 | "What did issue-triage say about x7y32?" | query | a person | a rule's output about a subject | nothing | the issue's thread if it has one, else channel | one-off, about a sequence |
| 13 | "Attach to this task" | query, streaming | a person | the task's read plane | the task's frames, until detached | the task's thread | in sequence: of the task |
| 14 | "@task hurry" | steering (`say`) | a person | a running task | the task reads it at its next boundary | the task's thread | in sequence: of the task |
| 15 | "Pause / resume / cancel this flow" | steering | a person holding `control` | a flow or a task | stops, resumes or ends it | the flow's thread | in sequence: of the flow |
| 16 | "Release web" | command | a person | a task started now | then with no ask: the release runs | channel | one-off that starts one; high stakes, so the reading is confirmed first |
| 17 | "Whenever a PR touches `perms/`, ask Sam" | command: a rule | a person | a charter, proposed | a standing rule, once installed | channel | one-off that starts many |

A message that could be several of these ("ship it" in a merge train's thread is an answer; in a
channel it is a command) is told apart by where it is said as much as by what it says: a thread
fixes the sequence, and with it which question or flow the words are about.

## Why it is worth building

Beyond testing: "ask these people this question, here, now, and then do this" is what a person
wants from a verse that can already reach them. Today that requires writing a charter, which means
the ad-hoc case pays the full price of the durable one — and a system whose cheapest question costs
a rule is a system where questions are rare.
