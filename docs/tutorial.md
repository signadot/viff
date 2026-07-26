# Verse — a hands-on tutorial

Verse is a small runtime for emergent SDLC flows on a dial of agentic autonomy. You
describe a *charter* of trigger rules; the runtime reacts to changes in an entity-verse
and does the work with governed agents. Every reaction passes a gate — *auto*, *ask*
(a human or agent approves), or *park* — so autonomy is turned up or down per action,
and the agents stay observable and steerable. Nobody authors the flow: it emerges from
triggers reacting to state that other actions write, and you set how much runs on its
own.

The whole system is one loop:

```
entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
     ▲                                                       │
     └───────────── actions write entities ◀────────────────┘
```

This tutorial gets you from zero to a running, steerable, self-driving flow, plus
federating your local working tree into it. It introduces each idea where you first
need it; for the model behind them see [the trigger model](trigger-model.md), for the
flat command surface see [the reference](reference.md), and for the notes behind each
section see [the docs index](README.md).

---

## 1. Build and start

One binary. `up` runs the engine + process runtime and brings up its store; every
other subcommand is a client.

```sh
cd verse
go build -o verse ./cmd/verse

export VERSE_ADDR=localhost:9797     # where clients reach the verse (this is the default)
./verse up                           # docd-backed by default (see below)
```

By default `up` is docd-backed: it brings up logd and docd on a fresh temp data dir
(printed, and kept) via `o sys up`, and points the store at
[docd](https://signadot.github.io/tony-format/docd/), the tony-format document store.
You don't need to know anything about docd to *play* with verse, since `up` stands it up
for you, but it is where verse's deeper properties come from — copy-on-write scopes,
conditional and coordinated writes, edge-faithful watches, compactions — and §10 draws
that out. For a zero-dependency run with no daemons and no persistence, add
`--store memory`. To use a durable dir, `--data <dir>`; to attach to an already-running
docd, `--docd-addr <addr> --docd-mount-addr <addr>`.

`up` decides two things for the whole runtime, because it hosts the process runtime and
the clients don't.

The first is which driver runs a process by default. With LLM creds configured — an `llm`
block in `~/.config/verse.tony`, or `PORTKEY_*`/`OPENAI_MODEL` in the environment — the
default is a real llm agent; with no creds it is `scripted`, which runs a task as a shell
command. The creds are read by the *server*, so set them before `up`, not in the client's
environment. `verse status` shows an `llm` block with `available` and a masked key. Any
single spawn can still say `--driver scripted|llm`.

The second is where an agent acts — its root. An llm agent reads and writes under a
directory you choose with `verse up --root <dir>`, or `--root .` for your tree. With no
flag, `up` makes an isolated temp dir and prints it at start (`agents rooted at …`, when
the llm driver is available), which is why a file an agent writes is *not* in your cwd.
To let an agent read a tree *through* the verse instead — no shared filesystem, wherever
it runs — use `verse connect` (§8).

Leave it running and open a second terminal for the client commands below; you'll
install a charter and drive it in §2. (Everywhere below, `verse` means the binary you
just built.)

Prefer `verse` on your `PATH`, or use `./verse`. To point at a non-default address,
`export VERSE_ADDR=host:port` or pass `--addr host:port` to any command.

---

## 2. Install a charter and drive it

A *charter* is one trigger, or a list of them. An *entity* is anything the flow refers to —
a PR, a build, a release — named by a `system:kind:id` triple and carrying a small payload.
A *trigger* says: when a matching entity changes and a gate resolves, an action fires.

Here's a tiny one-rule charter; save it as `charter.tony`:

```tony
- name: review
  condition:
    when: { system: demo, fields: { stage: open } }
  gates:
  - { name: approval, mode: ask }
  action:
    patch:    { stage: reviewing }
    spawn:  { driver: scripted, task: "echo reviewing $subject; sleep 1" }
    onDone:
      patch: { stage: reviewed }
```

Read it in four parts. `condition` is the cause — `when` matches an opened `demo` PR, and
because no `subject` is named, the firing is about the entity that changed. `gates` is the
guard: one gate named `approval`, in `ask` mode, so a human decides. `action` is the
consequence: mark the subject *reviewing* and spawn a review process. And `onDone` is a
[completion hook](completion-hooks.md) — when that process finishes, mark the subject
*reviewed*. The whole chain reads top-to-bottom in one rule. Every field, and the grammars
behind them, are in [the trigger model](trigger-model.md).

```sh
verse trigger add -f charter.tony     # or: cat charter.tony | verse trigger add -f -
verse triggers                        # what's installed
verse trigger show review             # one rule, handed back in tony
verse trigger remove review           # uninstall (add rejects a duplicate name)
```

`trigger show` hands the rule back in [tony](https://signadot.github.io/tony-format/), the
same document language you wrote `charter.tony` in — so installing and inspecting are a
round trip, not two formats. Tony is JSON's document model made easier to write: unquoted
keys and strings, optional commas, comments, since a charter is a rulebook and rulebooks
want annotating. It adds one thing the charter grammar leans on, which is *tags*. A tag
names what a value is, right where the value sits — `!glob "hotfix-*"`, `!quorum 2`,
`!until [ … ]` — and that is what keeps a charter a structure the engine walks rather than
a string it re-parses.

Save it as `charter.json` or `charter.yaml` instead and the same command reads it: the
suffix decides, and all three are dialects of one document model rather than three
supported formats (see [Representation](reference.md#representation)). This tutorial is in
tony because that's the default, not because the rest won't work.

In terminal A, watch the `demo` entities change:

```sh
verse watch --system demo
```

In terminal B, open a change and drive it:

```sh
verse entity put demo:pr:1 '{stage: open}'
# op: set
# ref: demo:pr:1
# rev: 1
# to: {
#   stage: open
# }
```

A payload typed on the command line names no file, so it is read in the default format —
tony here, which is why `{stage: open}` needs no quotes around either half. Commas are
optional in tony, so `{stage: open, id: 7}` and `{stage: open id: 7}` are the same thing.

The `review` trigger fires but its gate is `ask`, so it blocks:

```sh
verse gates
# - gate: approval
#   id: trigger:review:approval~1
#   mode: ASK
#   origin: review
#   reason: "trigger review gate approval on demo:pr:1"
#   spawns: review@demo:pr:1
#   subject: demo:pr:1
#   tool: trigger:review
#   where: engine
```

Three different names show up there, and they are three different things: `gate` is the
gate's own name (`approval`), `origin` is the trigger that raised it (`review`), and `tool`
is what it governs. `subject` is what the gate is *about* and `spawns` is what a "yes" will
start — the process id is derived from the trigger and the subject, so it is settled before
the gate is raised even though nothing is running yet. (`where: engine` gates have no
`process` for that reason; a process gate, §3, has one because its process is already
running.)

Approve it, using the `id` from `verse gates` (the flag can come after the id — the
CLI handles that). The id is `trigger:<trigger>:<gate>~n` — a trigger can have
several named gates, which is why the gate's name is part of it:

```sh
verse gate approve trigger:review:approval~1
# approved trigger:review:approval~1
```

Now watch it self-drive. In terminal A you'll see the stage advance twice:

```
{op: set ref: demo:pr:1 rev: 1 to: {stage: open}}
{from: {stage: open} op: set ref: demo:pr:1 rev: 2 to: {stage: reviewing}}       ← review action
{from: {stage: reviewing} op: set ref: demo:pr:1 rev: N to: {stage: reviewed}}   ← review's onDone, after the process finished
```

Each change is one line — flow form, so a watch stays greppable and one event never
wraps into the next. (`op` is `set` for created-or-updated; see
[the trigger model](trigger-model.md).)

The second transition is the point: you did not drive "reviewing → reviewed" by hand. The
review process finishing — its `process:instance` reaching `status=done` — fired the
`review` trigger's `onDone` hook, which advanced the subject, so the process step and its
follow-up read top-to-bottom in one rule.

```sh
verse entity get demo:pr:1
# payload: {
#   stage: reviewed
# }
# ref: demo:pr:1
```

---

## 3. Processes

A trigger's `spawn` runs a process, but you can run one directly too. `run` prints the
process id (a `run-…`):

```sh
verse process run "echo summarizing; sleep 2"          # scripted: runs the task as a shell command
verse process run --wait "echo done"                   # …and block for the final output
verse processes
verse process get <id>        # snapshot: status, say, lastOutput, result, pending gates
verse process say <id> "also mention the dial"        # say to the agent mid-run (re-prompt)
verse process vector <id> ask --as human:scott        # slide autonomy (auto|ask|park); --as attributes the slide
verse process vector <id> --file vector.tony          # …or install a full gate vector
verse process cancel <id>
```

`say` and `vector` are different levers: `say` re-prompts the agent (new instructions);
`vector` re-vectors it — swapping the gate vector that governs its autonomy, applied at the
next boundary with no restart. It works wherever the process runs: the local runtime slides it
directly, a brokered/remote worker gets it relayed through its inbox.

A direct run is mirrored as a `process:instance` entity like any other process, so
`verse entity get process:instance:<id>` shows what it produced — the latest `say`, last
tool output, and full `result` — even after it finishes.

The two drivers carry different vectors — §1 sets which one is the default — and the
difference is the whole point of a gate: it asks about what you have *not* already
authorized.

`scripted` runs ungated: no gates, everything auto. Its task *is* the shell command, so you
authorized it by typing it, and asking "run a shell command?" after
`verse process run "go test ./..."` would be asking you to confirm the sentence you just
wrote. Spawned from a trigger the same holds one level up, since the trigger's own gates
already ran, per firing, before the action fired.

`llm` asks before effects, because the agent *chose* them. Approving a command the agent
thought up is a real decision.

There is a rough edge here, worth stating plainly. Which rule applies depends on how you
addressed the runtime, and today the runtime cannot tell. Ask an *agent* to do something —
the `--attach` example below — and being asked before the write is the convention you
expect; you opted into a loop and you are watching it. Hand it a *command* —
`verse process run "go test ./..."` — and you addressed a runner, not an agent: with creds
configured the default driver is llm, so the command you just typed comes back as
`⏸ gate "shell" (run_bash) — approve?`. Nothing was decided that you had not already
decided; the prompt only reveals that verse chose to satisfy your command with an agent.

The vector is fixed by the driver and nothing overrides it at spawn (`run` takes only
`--wait/--attach/--driver`; the `policy` axis is one of the launch spec's "later" fields). So
today: pass `--driver scripted` when your task *is* a command — that is exactly what it is for —
or slide the vector once it starts (`process vector <id> auto`). A trigger hits the same edge from
the other side: its gates approve *the firing*, not the actions the spawned agent then chooses,
and it has no way to say it already decided. Where the permission *should* come from is
[authority](authority.md).

An LLM agent asks permission. Its reads run freely, but writing a file, running a shell
command, and delegating to a sub-agent are gated: the agent blocks until you approve.
`run --attach` spawns *and* streams the transcript, and on a terminal it prompts you inline
when the agent blocks:

```sh
verse process run --attach 'Write a file hello.txt containing "hi".'
# the agent narrates, then blocks and asks:
#   ⏸ gate "write" (write_file) — approve? [y/N] y
# → you answer, the write runs (wrote 2 bytes), and the process completes — one stream.
```

That is the llm driver's transcript; with no creds configured the same command runs the
sentence as a shell command and fails, which is the §1 default at work. The file itself lands
under the agent's root, not your cwd — `--root` again.

That's the autonomy dial at the process level: reads are auto, effects ask. Answer `y` to
allow or anything else to deny (a denied gate comes back to the agent as an error it re-plans
around). Piped (non-tty), `attach` just streams without prompting.

You can also resolve out-of-band from anywhere. The gate shows up in `verse gates` alongside
trigger gates, and in `process get <id>`, resolvable by its id — the `~` that separates the
gate from its process needs no quoting and no escaping, in a shell or in a URL:

```sh
verse gates                                       # engine + process gates, unified
verse gate approve run-…~d1                       # or: process approve run-… run-…~d1
```

Because processes are entities, you can watch the whole fleet's lifecycle through the
verse:

```sh
verse watch --system process
# process:instance:review-1  running → … → done
```

---

## 4. Steer flows

Steering is the control plane — out-of-band from observation, honored at a process's
next boundary, never a store write. Each command acts on a *flow*: the in-flight
processes tagged with a subject, whether that is a PR, a change, or a release.

A flow is not a stored thing. It is a query over the subjects of live processes, which is
why steering takes an address rather than just a ref, and why steering a whole set needs no
new machinery: it is the same selector at a wider setting.

A flow exists only while its processes run, and a scripted review is brief. So give yourself a
durable flow to steer: add a `hold` trigger — once a PR reaches `reviewed`, it runs a 60-second
process about that PR — then drive a change through review to trip it:

```sh
verse trigger add -f - <<'TONY'
- name: hold
  condition:
    when: { system: demo, kind: pr, fields: { stage: reviewed } }
  action:
    spawn: { driver: scripted, task: "sleep 60" }
TONY
verse entity put demo:pr:2 '{stage: open}'      # open a change…
verse gates                                     # …find its review gate, then:
verse gate approve <that-id>                    # review runs, the PR reaches `reviewed`, hold fires
verse flows
# - demo:pr:1
# - demo:pr:2                                   ← a live 60s flow to steer
```

That heredoc is stdin, which names no format the way a `.tony` file does, so it too is
read in the default — `--ifmt json` if you'd rather paste JSON.

The hold process's own completion re-sets the PR to `reviewed`, but that write changes nothing,
and the engine reacts to real changes rather than to writes, so `hold` fires exactly once. No
loop, no latch: a set-to-the-same-value is inert (see [change events](change-events.md)).

Now steer that subject's flow:

```sh
verse flow pause  demo:pr:2           # freeze its agents at their next boundary
verse flow resume demo:pr:2           # release them
verse flow say    demo:pr:2 "hurry"   # inject an instruction
verse flow cancel demo:pr:2           # stop its processes (destructive)
```

…or steer a whole set of subjects with the same verbs, since the address is a selector and
`*` is a wildcard per segment:

```sh
verse flow pause 'demo:pr:*'          # every PR's in-flight work
verse flow say   'demo:*:*' "hurry"   # everything in the demo system
verse flows                           # the distinct subjects with live work
```

`verse flows` answers which subjects are busy. To see what is happening to *one* — and what
comes next — ask for its shape:

```sh
verse flow show demo:pr:1
# - roots:
#   - chain: hold
#     id: hold@demo:pr:1
#     kind: root
#     lastAction: run_bash
#     status: running
#     task: "sleep 60"
#   - chain: review
#     children:
#     - chain: review.onDone
#       kind: onDone
#       status: applied
#     id: review@demo:pr:1
#     kind: root
#     lastAction: run_bash
#     status: done
#     task: "echo reviewing demo:pr:1; sleep 1"
#   subject: demo:pr:1
```

Two roots, because two triggers acted on this PR: `hold` is still running its 60 seconds, and
`review` finished.

Each node is one action in the charter, addressed by its `chain` — the
[kpath](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/ir/kpath) of that action
inside its trigger. A kpath is tony's way of addressing a position *inside* a document:
dotted field names, with brackets for list positions. So `review` is that trigger's action,
`review.onDone` is its done-hook, and a sequence step's failure hook would be
`review.seq[2].onFailed`. The same string is the id the action spawns under
(`review@demo:pr:1`), which is what makes the tree not a second model of the work: it is the
charter and the runtime read through one address. See
[completion hooks](completion-hooks.md#addressing-the-chain-is-a-kpath).

Some statuses come from no process at all, because a charter position is not a process.
`review.onDone` above is `applied`: it fired — that is what set `reviewed` — but it only sets
fields, so it left no process to observe. A branch nothing has reached yet is `pending` while
it may still fire, and `skipped` once its parent has ENDED on a status that branch does not
answer to: give `review` an `onFailed` and it reads `skipped` here, because the review
completed instead.

The tree is derived, never stored — assembled per request from the charter (the shape,
including what has not run), the mirrored `process:instance` entities (the history, which
outlives the live registry), and the live registry (what is still steerable).

A pattern works here too, giving one tree per subject:

```sh
verse flow show 'demo:pr:*'
```

Each command reports how many processes it reached, echoes the address it reached them by so
you can see what a wildcard actually selected, and names the verb — so the answer says what
was done without reference to the command that did it:

```sh
verse flow pause demo:pr:2
# affected: 1
# subject: demo:pr:2
# verb: pause
```

All eight steer routes (four flow verbs, four goal verbs) answer in that one shape. A goal
reports `goal:` instead of `subject:`, since it is addressed by name rather than by an entity
address.

They fan out to the addressed processes and land at each one's next boundary, so a flow can
be held, redirected, or stopped mid-flight while every *other* flow keeps reacting. Because a
scripted process is a single command, `verse flows` sees the `sleep` above and `flow cancel`
stops it at once by killing the command, while `pause` and `say` land at its next boundary —
which for a one-command process is when it ends. They take hold mid-run on a multi-step
process: the `llm` driver, whose every tool call is a boundary.

`pause` is the operator's ad-hoc freeze — "hold this flow, I want to look" — distinct from
`cancel`, which ends the work, and from a `park` gate, which is a hold the *charter* declares
per action. `pause` is you, live, holding this one subject's work.

Why "out-of-band, with priority" matters: an intervention that has to queue behind a backlog
of observations is an intervention defeated. Control never rides the change stream, so
cancel, say and pause reach the process directly and are serviced ahead of observation. See
[deployment and steering](deployment-and-steering.md) for the invariant and the engine's
priority servicing.

---

## 5. Human-executed processes (dial-in)

Most processes are run by an agent driver, but a process can be run by a human, or by any
external, firewalled worker, who dials in to do the work. verse hosts the process behind a
*broker* and publishes where to dial in; the executor claims it and reports back. Nothing
inverts: both verse and the worker are clients of the broker, so a laptop or a pod behind a
firewall works without verse reaching in.

Start verse with a broker as the run policy (a sibling port serves the dial-in face):

```sh
verse up --broker --broker-addr :9090
```

Now spawn a process. With a broker policy it lands on the broker awaiting an executor
instead of running itself, and `verse processes` shows its policy and where to dial in. You
never type that URL; verse resolves it for you:

```sh
ID=$(verse process run "review the auth change")
verse processes
# - dialURL: "http://localhost:9090/broker/<nonce>"
#   id: run-…
#   policy: broker
#   status: running
#   task: "review the auth change"
```

You are the executor. Do the work with `verse process do`. Every action takes
`--as <identity>`, because a claim must be attributable, and the broker URL is resolved for
you from the process, so there is no URL to type:

```sh
verse process do note "$ID" "reviewing the auth change" --as human:scott
```

Mid-work you reach a decision you shouldn't make alone. Raise it *up* to verse rather than
deciding — the verse governs who resolves it: an operator, a reviewer, a vote, or, if the
dial allows, you. The executor never resolves its own gate. `--wait` blocks for the outcome:

```sh
verse process do gate "$ID" deploy-approval --as human:scott --reason prod --wait
# gate raised: deploy-approval-a1b2c3d4
# awaiting resolution…
```

In another terminal, someone with the authority resolves it — your `--wait` unblocks:

```sh
verse process approve "$ID" deploy-approval-a1b2c3d4
```

Then finish the work (or `do fail "$ID" <reason>` to end it the other way):

```sh
verse process do complete "$ID" "LGTM — one nit on the token TTL" --as human:scott
```

The process is now `done`, with your result on its `process:instance` entity, so a
completion trigger reacts to it exactly as if an agent had finished (§2). A *human* did the
work; the flow moves on.

So a process has two vantages, and every verb works whether it runs locally or behind a
broker. From outside, the operator drives and watches: `list`, `get`, `attach`, `cancel`,
`say`, `approve`/`deny`. From inside, on the worker leg, the executor acts:
`do complete|fail|note|gate`, plus `inbox` to see the control queued for the worker.

The broker is where a human sits on the dial of autonomy — the same process the engine drives
and steers, done by a person, observed and governed through the one verse. See
[the controlPolicy](control-policy.md) for the seam and the dial-in protocol.

---

## 6. Gates the group decides — *whether*, and *who*

So far a gate was `auto`, `ask`, or `park` — one answerer, one click, deciding *whether* an
action proceeds. But a gate is really just a question the charter poses, and some questions
aren't one person's to answer in one click: a merge that needs *two* reviewers, or a doer
*chosen* from several. verse resolves those the same way. It puts the open question to the
group as a *motion* in the store, watches ballots accumulate, and commits an outcome when a
tally reaches the bar. One machinery, two questions: *whether* an action fires, whose answer
is `proceed` or `deny`, and *who* does the work, whose answer is a choice of doer.

Both are answered the same three ways — `auto`, where verse picks by a rule, by one person,
or by the group — over the one votable, attributable surface, `verse docket` and
`verse vote`. A one-click `ask` still lives in `verse gates` and `gate approve`; a motion is
what an answer that accumulates over time needs.

A motion is an ordinary entity, `motion:channel:<id>`, so the docket is a view of the store:
you can `watch` it, `get` one, and write a trigger that reacts to it, exactly as with a PR.
It is not a side-channel or a UI queue.

### Whether: a vote

Say a merge needs two reviewers. Give the gate a `formula` instead of a mode, and the verse
watches and resolves it itself:

```tony
- name: merge
  condition:
    when: { system: change, fields: { stage: ready } }
  gates:
  - { name: approvals, formula: !quorum 2 }
  action:
    patch: { stage: merged }
```

`!quorum` is a tag — tony's way of naming what a value is, right where the value sits. The
formula language below is built out of them, which is why a charter can say
`!until [ !quorum 2, !deadline "4h" ]` on one line and still be a structure the engine
walks rather than a string it has to re-parse.

```sh
verse trigger add -f merge.tony
verse entity put change:pr:42 '{stage: ready}'
```

The trigger fired, but the action is waiting — not on a person, on a tally. The open
question is now a motion on the docket, where ballots land and the count is watched:

```sh
verse docket
# ec9856c1  whether open  options=[proceed=approve(2)]  subject=change:pr:42
```

The options are what can win — here just `proceed`, at a bar of 2. Cast ballots, each naming
its voter, since a motion must be settled attributably:

```sh
verse vote ec9856c1 approve --as alice   # 1 of 2 — still open
verse vote ec9856c1 approve --as bob     # 2 of 2 → carries
verse docket
# ec9856c1  whether closed  options=[proceed=approve(2)]  outcome=proceed tally=2  subject=change:pr:42
verse entity get change:pr:42
# payload: {
#   stage: merged
# }
# ref: change:pr:42
```

Each `verse vote` writes a ballot into the motion's log; verse *watches the log* and
commits `proceed` when the count reaches the bar. It stays live: if alice retracts before it
commits, the tally drops and the gate re-blocks. Nobody owns the motion; the group carries
it.

A vote you can only win isn't a vote. Give the motion a second option, and read `until` as
*approvals, unless the rejections get there first*:

```tony
formula: !until [ !quorum 2, !quorum { choice: reject, n: 2 } ]
```

Now the motion has two options, `proceed` and `deny`, and the first to *its* bar wins. Two
rejects close it `outcome=deny` and the action never runs. "No" is a real outcome, not the
absence of "yes."

Votes stall, and you don't want a motion open forever. Make the second option a deadline —
the clock is what satisfies it:

```tony
formula: !until [ !quorum 2, !deadline "4h" ]
```

Quote the duration. A tony literal may not start with a digit, and commas between flow
elements are optional, so `!deadline 4h` reads as the number `4` beside a separate literal
`h` — a perfectly well-formed document that does not mean what you wrote. verse rejects a
numeric deadline and tells you to quote it, because a deadline has no threshold for a bare
number to be, and nothing earlier in the pipeline can catch it.

```sh
verse docket
# ec9856c1  whether open  options=[proceed=approve(2)]  closes=2026-07-23T08:15:36  subject=change:pr:42
```

Two approvals first gives `proceed`; the clock first defaults the motion to `deny`, and the
action is skipped. The deadline is per-motion and durable — a restart re-reads `closes` and
honors the elapsed wall time. For a real external signal instead of a clock, the right side
can be a `fact`: `!fact ci:status:green` waits for CI, a tag, or a posted review.

That is the whole formula language. A leaf is a requirement — `!quorum`, `!fact`,
`!deadline` — and operators compose them. `!until` is the operator that exists today;
temporal `and`/`or` are still to come. Don't confuse those with the `!and`/`!or` you can
already write inside a *match*: those compose predicates about the verse right now, and they
work, while these would compose requirements over time. The tags are the grammar. In JSON,
which has no tags, each one spells as a single-key object —
`{"until": [{"quorum": 2}, {"deadline": "4h"}]}` — and means exactly the same thing, so the
charter you write in one dialect reads back in either.

### Who: a selection

The second question hides in every `spawn`: *who* does it. So far a spawn named a fixed
`driver`. That's a motion too, over a different option set. Name the eligible doers and how
to choose, instead of one driver:

```tony
- name: review
  condition:
    when: { system: change, fields: { stage: open } }
  action:
    spawn:
      task: "review $subject"
      provider: { eligible: [gpt, claude], select: vote, need: 2 }
```

`select` is the same dial on the *who* axis: `auto` picks the first eligible by a rule, `ask`
lets one person pick, `vote` lets the group pick. It raises the same kind of motion, except
that the options are now the doers:

```sh
verse docket
# d4e5f6  select open  options=[gpt(2) claude(2)]  subject=change:pr:1
```

Same docket, same ballots, but the answer is a *choice*:

```sh
verse vote d4e5f6 claude --as x
verse vote d4e5f6 claude --as y
verse docket
# d4e5f6  select closed  options=[gpt(2) claude(2)]  outcome=claude tally=2
```

The process spawns on the chosen doer. The pick is fixed for that run; reassigning means
cancel-and-respawn, not a live swap mid-flight. (A `within` deadline works here too — if no
doer reaches the bar in time, the motion closes with no pick and nothing spawns.)

Listing driver names inline gets old, and real doers have attributes — a tier, a cost, a
specialty. Register them once, since they are just entities, and select by what they are:

```sh
verse entity put provider:review:claude '{driver: llm, tier: premium}'
verse entity put provider:review:gpt    '{driver: llm, tier: premium}'
verse entity put provider:review:cheapo '{driver: scripted, tier: basic}'
verse providers
# review     claude         driver=llm tier=premium
# review     gpt            driver=llm tier=premium
# review     cheapo         driver=scripted tier=basic
```

Now the action selects by capability plus a match over attributes, not by name:

```tony
provider: { capability: review, match: { tier: premium }, select: vote, need: 2 }
```

```sh
verse docket
# … select open  options=[claude(2) gpt(2)] …
```

`cheapo` never made the ballot, because it isn't `premium`. That `match` is a real verse
match pattern over each provider's attributes, so "premium reviewers" — or later "anything
under $5" — is just a pattern. The eligible set is discovered at fire time from the registry,
so you add or retire doers by writing or deleting entities, with no charter change.

Two questions, *whether* and *who*, over one votable and attributable machinery.

---

## 7. Goals — give a flow a destination

Everything so far pushes the world forward: a change fires a trigger, a gate decides whether
and who, a process runs, and its completion fires the next trigger. Nothing yet says where a
flow is *going*, or when it is *done*. A goal adds that — an explicit target — without giving
up the emergent model. The move worth holding onto is that a goal drives by *owning charter*,
not by reconciling. The triggers still do the doing; the goal owns them, watches for success
or failure, and holds a live status.

A goal is three things: a `doneWhen` predicate, which is the target, an optional `deadline`,
which is a fail-latch, and a bundle of owned triggers, which is the doing. `doneWhen` is the
very formula language you gate with in §6, but here it is a target rather than a gate:

```sh
verse goal add ship --subject change:pr:42 \
  --done-when '!fact ci:status' --deadline 4h
```

`--done-when` takes a formula in the input format, so it's the same `!fact` / `!quorum` /
`!until` grammar as a charter's gate — one language, whether it's gating an action or
naming a target. (`--deadline` is an ordinary flag, not tony, so `4h` is fine unquoted
there.)

```sh
verse goals
# - deadline: "4h"
#   name: ship
#   status: blocked
#   subject: change:pr:42
#   terminal: false
#   waitingOn:
#   - fact:ci:status
```

The goal is `blocked`, and it names what it is waiting on — the unmet leaves of `doneWhen` —
recomputed live as the world changes. It has spawned nothing; it is watching. Make the target
true, however that happens: a trigger, a source, or a person.

```sh
verse entity put ci:status:build '{state: green}'
verse goals
# - deadline: "4h"
#   name: ship
#   outcome: done
#   status: done
#   subject: change:pr:42
#   terminal: true
#   waitingOn: []
verse entity get goal:done:ship
# payload:
#   flow: change:pr:42
#   goal: ship
#   subject: change:pr:42
#   witness:
#   - ci:status:build
# ref: goal:done:ship
```

When `doneWhen` holds, the goal latches — once, and it stays latched, so retracting the fact
does not un-finish the goal. Two outcomes are first-class: `done` and `unachievable`. The
deadline is the fail side, exactly like §6's deny-quorum, where "no" is a real outcome. Let a
deadline pass with the target still unmet and the goal latches `goal:failed` with
`status=unachievable`.

Here is the distinction worth holding. The latch is an ordinary fact — `goal:done:ship` is
just an entity — so a goal *recognizes* and never *advances* anything itself. If you want the
finish to carry a subject onward, a trigger reacts to the latch, the same emergent way
everything else composes:

```tony
- name: on-ship
  condition:
    when:    { system: goal, kind: done }
    subject: change:pr:*
  action:
    patch: { stage: shipped }
```

And because a goal's owned triggers spawn processes tracked as flows, a goal *is* a flow with
a destination — steer it with the very same verbs from §4, keyed by the goal:

```sh
verse goal pause  ship          # freeze its in-flight work
verse goal say    ship "hurry"  # inject an instruction
verse goal cancel ship          # stop it
```

To ship the owned triggers *with* the goal instead of hand-authoring them, hand `add` a full
record — `verse goal add -f ship.tony` — which carries `triggers: [ … ]` alongside
`doneWhen`. On a terminal, done or unachievable, the goal reaps its owned triggers, so a
finished goal leaves no orphaned charter.

*(v0: `doneWhen` is a single fact/quorum atom; multi-condition targets, an optional plan that
generates the owned triggers, and scope-isolated reap are next — §10.)*

---

## 8. Federate your local working tree

`verse connect` federates your working tree into the verse under a private, per-session
*scope*, and wires a validator to every file — the local-dev loop. It is its own process, so
give it a second terminal and leave it running. In one command it does three things.

It writes each file's rich metadata — full fstat plus a content hash — to the store as
`local:dir-status-<scope>:<path>` entities. That is an index, not a warehouse: the stat and
the hash, not the bytes.

It serves file contents on demand through docd, from a content controller at
`local:dir-content-<scope>:*`, so a validator reads them through the verse with no shared
filesystem required.

And it installs a scoped trigger that spawns a validator per file.

It all lives in a nonce scope, isolated from baseline and from other devs, and on exit
`connect` drops it. The mechanics are in [connect](connect.md).

In terminal A, start the daemon. Configure LLM creds first (§1) so the validators can run
as real agents:

```sh
verse up
```

In terminal B, connect your tree, run the validator on the `llm` driver, and give it a
task (`$path` binds to each file's relative path). `connect` prints the scope it chose
(e.g. `scope "a1b2c3"`):

```sh
verse connect . --driver llm \
  --task 'Read the file at path "$path" and report any problems in one sentence.'
```

In a third terminal (use the scope `connect` printed):

```sh
verse processes                     # a validator per file — local-dev feedback!
verse process get <id>                 # its result: the agent's read-and-validated report
verse entities --system local --scope <scope>       # your files, as dir-status entities
verse entity get local:dir-content-<scope>:README.md   # a file's bytes, served on demand
```

Touch or add a file and its validator fires, reading the file through the verse. Ctrl-c
`connect`, or run `verse disconnect --scope <scope>`, to tear the scope down: trigger,
overlay, and content mount all go. For a credential-free smoke test, drop the creds and
`--driver llm` — a validator still fires per file, on the scripted driver, it just doesn't
read.

Because contents flow through docd rather than a server-side `--root`, the runtime need not
share a filesystem with your tree, so the validator can run elsewhere. `--root` remains only
for a non-federated single-machine agent; see [content federation](content-federation.md).
File reads are still truncated today, so an agent validates a large file from a partial view.

---

## 9. Scopes

Every client command takes `--scope <name>`, with baseline as the default. A scope is a
copy-on-write overlay: reads see the scope layered on the shared baseline, and writes land in
the scope without touching baseline, so your local changes and flows stay isolated.

```sh
verse connect . --scope me
verse watch --scope me
verse entity put demo:x:1 '{k: v}' --scope me      # in the scope only
verse entity get demo:x:1                          # baseline: not there
verse disconnect --scope me                          # drop the whole overlay
```

`disconnect --scope me` tears the overlay down in one shot — every scope write, not just
`local:*` files — via logd's `DeleteScope`.

Which store you are on matters here. Real copy-on-write scopes require the docd/logd store,
which is now `up`'s default. On `--store memory`, `--scope` is a passthrough with no
isolation, and `disconnect --scope` falls back to removing `local:*` entities. Making a scope
a first-class, listable thing is [still a design note](scope-lifecycle.md).

---

## 10. What this is (and isn't) yet

### The store

The store defaults to [docd](https://signadot.github.io/tony-format/docd/) — durable,
observable, controller-federated. You never have to touch docd to *use* verse, since `up`
brings it up for you, but it is where verse's deeper properties come from, and they are hard
to see spelled out anywhere else: copy-on-write scopes (§9); conditional and coordinated
writes, meaning atomic CAS, so two writers advancing one entity cannot lose each other's
fields; edge-faithful watches, where the change stream *is* each writer's applied patch, so
no reader reconstructs, reorders, or drops an edit; and compactions, which keep the log
bounded while preserving current state.

With no store flags, `up` brings up logd and docd on a fresh temp data dir via `o sys up` and
points the store at docd's client face. `--data <dir>` uses a durable dir,
`--docd-addr`/`--docd-mount-addr` attach to an already-running docd, and `--store memory`
opts out entirely, with no daemons and no persistence.

The store maps each entity to a kinded path `verse.<system>.<kind>.<id>`, reads and writes
via Match/Patch, and drives the change stream and revision cursor from a single logd watch.
`Since` is served natively by a `FromCommit` replay watch, so a long hold costs a cursor
rather than a buffer. Commit numbers are monotonic but sparse, so the engine treats the
revision as an opaque cursor and reconciles via `Since`. Writes are atomic via CAS
(`PatchIf`), so concurrent modifies don't lose updates. `Op` is `set` or `delete`, with
create identical to update, since logd delivers the writer's applied patch verbatim.

### The reactive grain is the entity, not the transaction

This one is subtle, and may need cleaning up. A single docd commit can atomically patch many
entities, but the store normalises each commit into one change event per entity —
`walkEntities`, at path-depth `system/kind/id` — all sharing that commit's revision. That
normalisation is exactly what guarantees a trigger's subject is always one entity: dispatch
is per-entity-edge, so `$subject`, and the flow or goal keyed on it, is never ambiguous even
under a broad match, since a broad pattern just fans out to one firing per matching change.

The cost is that transaction atomicity is flattened. The engine cannot react to "these two
changed together in one tx" as a unit — the shared revision is the only trace, and nothing
groups by it. It is a load-bearing assumption worth naming, and worth revisiting if
cross-entity transactional reactions are ever needed. See [change events](change-events.md).

### Where the rest stands

Scopes are real copy-on-write overlays on the docd/logd store: writes isolated, reads layered
on baseline, and `disconnect --scope` drops the overlay via `DeleteScope`. On `--store memory`
they remain a passthrough.

Local federation works as §8 describes: scoped `local:dir-status-<scope>:*` metadata writes,
a content controller serving `local:dir-content-<scope>:*` bytes on demand, and a scoped
trigger spawning a validator per file. A scoped agent's `read_file` resolves content through
the verse, so the runtime need not share a disk with the tree. The scope's engine observes
via per-trigger subtree watches, each watching exactly the `system.kind` it matches rather
than a root. See [content federation](content-federation.md).

Sources — the generic pattern for reflecting external systems like git, GitHub or Linear into
an owned verse slice through a docd controller, on webhook-as-hint plus poll-reconcile — are
designed but not built. The first, a git Source, is next. See [sources](sources.md).

Deployment today runs the engine and proc runtime in one process, spawning logd and docd as
children or attaching to a running docd. The pieces — store, engine (`versed`), process
runtime (`proc`) — are separable services behind the same interfaces. See
[deployment and steering](deployment-and-steering.md).

The controlPolicy makes how a process runs a seam. The default is the in-process supervisor,
`up --policy <url>` hosts processes on a remote verse, and `up --broker` hosts them behind a
broker that a firewalled worker or a human dials in to. verse drives all three through the
identical handle, so `verse processes` and `process get`/`attach`/`cancel`/`say`/`approve` /
`deny` work across all of them, and a human or external worker executes via `verse process do`
(§5). Left to do: the inbox long-poll. See [the controlPolicy](control-policy.md).

Governed decisions are one machinery answering two questions. *Whether* an action fires is a
gate whose formula the verse resolves itself: a quorum vote, with a deny-quorum for "no", a
deadline against the clock, or an external fact such as CI going green. *Who* does the work is
the same motion over a different option set — the doer, chosen from an inline set or a
`provider:*` registry filtered by a match. Both raise one motion, where options accumulate
ballots, the first to its threshold wins, and the winner commits terminal-once behind a live
retract guard, observed through `verse docket` and `verse vote` (§6). Left to do: richer
disciplines such as ranked or weighted votes and review panels, and secret ballots via scopes.

Goals put an explicit target over the reactive substrate. A goal owns a bundle of triggers,
a `doneWhen` formula, and a deadline; it latches `goal:done` or `goal:failed`, with success
and "unachievable" both first-class; it holds a live status with a `waitingOn` list, and
steers as a flow — yet it drives by owning charter rather than by reconciling (§7). Left to
do: temporal `doneWhen` (eventually, always, sequence), an optional plan that generates the
owned triggers, and `DeleteScope`-based reap of a goal's owned charter.

That's the whole thing. Install a charter; react to events, external and local-dev, with
gated agent processes — gates a person, the group, or the clock can answer, and that choose
who acts; advance state emergently on completion; steer flows out-of-band; and observe
everything through one verse.
