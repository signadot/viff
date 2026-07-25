# Verse — a hands-on tutorial

Verse is a small runtime for **emergent SDLC flows on a dial of agentic autonomy**.
You describe a *charter* of trigger rules; the runtime reacts to changes in an
**entity-verse** and does the work with governed **agents**. Every reaction passes a
**gate** — *auto*, *ask* (a human or agent approves), or *park* — so autonomy is
turned up or down per action, and the agents stay observable and steerable. Nobody
authors the flow — it emerges from triggers reacting to state that other actions
write; you set how much runs on its own.

The whole system is one loop:

```
entity change ──▶ trigger matches ──▶ gate resolves ──▶ action fires
     ▲                                                       │
     └───────────── actions write entities ◀────────────────┘
```

This tutorial gets you from zero to a running, steerable, self-driving flow — plus
federating your local working tree into it.

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

By default `up` is **docd-backed**: it brings up logd + docd on a fresh temp data dir
(printed, and kept) via `o sys up`, and points the store at
[docd](https://signadot.github.io/tony-format/docd/) — the tony-format document store.
You don't need to know anything about docd to *play* with verse — `up` stands it up for
you — but it's where verse's deeper properties come from (COW scopes, conditional +
coordinated writes, edge-faithful watches, compactions), and §13 draws that out. For a
zero-dependency run — no daemons, no persistence — add `--store memory`. To use a
durable dir, `--data <dir>`; to attach to an already-running docd,
`--docd-addr <addr> --docd-mount-addr <addr>`.

Leave it running and open a second terminal for the client commands below; you'll
install a charter and drive it in §3. (Everywhere below, `verse` means the binary you
just built.)

Prefer `verse` on your `PATH`, or use `./verse`. To point at a non-default address,
`export VERSE_ADDR=host:port` or pass `--addr host:port` to any command.

---

## 2. The vocabulary (30 seconds)

- **Entity** — anything the SDLC refers to, named by a `system:kind:id` triple
  (`demo:pr:1`, `github:pr:signadot/x#42`, `local:dir-status-<scope>:src/main.go`) plus a small
  payload. The entity-verse is the shared, observable store of these.
- **Trigger** — one rule of the charter: *when a matching change happens and a gate
  resolves, an action fires.*
- **Gate** — whether an action proceeds: `auto` (fire) · `ask` (a human/agent approves)
  · `park` (hold) — or a **formula** the verse resolves itself (a vote, a deadline). The
  autonomy dial.
- **Motion** — a governed question put to a group, when the answer isn't one inline
  click: a **vote** on a gate (a quorum, with a deadline or a "no"), or a 1-of-N
  **selection** of a doer (§8) — the same machinery, two option-sets. verse reifies it in
  the store as something you watch (`verse docket`) and add to (`verse vote`); ballots
  accumulate on it, and when a tally reaches the bar the motion **carries**, its outcome
  resolving the question. (A plain `ask` gate raises none — one person answers it directly
  via `gate resolve`.)
- **Docket** — the open motions, collectively: what the verse is currently waiting on a
  group to settle (`verse docket`).
- **Action** — declarative data: `set` fields on an entity (a state transition) and/or
  `spawn` a process (a `launch.Spec`) — whose *doer* can itself be a governed motion
  (§8), not a fixed one.
- **Process** — a governed unit of work (an agent), hosted by the runtime,
  observable and steerable. Processes are themselves entities in the verse.
- **Subject** — the entity a piece of work is *about*: the thing moving through the
  lifecycle (a PR, a change, a release), named by its ref (e.g. `demo:pr:1`). When a
  trigger spawns a process, that process is tagged with its subject.
- **Flow** — a subject's in-flight lifecycle: all the processes spawned for that
  subject, steerable as a unit. It is a *query* over live processes' subjects, not a stored
  index, so one address can steer a subject, a kind, or a system (§6).

Two planes, and they stay apart: **observation** (`watch`, `get`, `list`) is read;
**steering** (`gate resolve`, `flow pause/cancel`) is control — out-of-band and
serviced with priority, so a runaway flow can always be stopped in time.

---

## 3. Install a charter and drive it

A **charter** is JSON — one trigger, or an array of them. Here's a tiny two-rule one; save
it as `charter.json`:

```json
[
  {
    "name": "review",
    "condition": { "when": { "system": "demo", "fields": { "stage": "open" } } },
    "gates":     [ { "name": "review", "mode": "ask" } ],
    "action": {
      "set":   { "stage": "reviewing" },
      "spawn": { "driver": "scripted", "task": "review $subject" }
    }
  },
  {
    "name": "reviewed",
    "condition": { "when":    { "system": "process", "kind": "instance", "fields": { "status": "completed" } },
                   "subject": "$flow" },
    "action": { "set": { "stage": "reviewed" } }
  }
]
```

Two rules: `review` (on an opened `demo:pr`, ask, then mark *reviewing* and spawn a review
process) and `reviewed` (when that process completes, advance the subject to *reviewed*).
Install it — §4 dissects each field:

```sh
verse trigger add -f charter.json     # or: cat charter.json | verse trigger add -f -
```

**Terminal A** — watch the `demo` entities change:

```sh
verse watch --system demo
```

**Terminal B** — open a change and drive it:

```sh
verse entity put demo:pr:1 '{"stage":"open"}'
```

The `review` trigger fires but its gate is `ask`, so it blocks:

```sh
verse gate list
# [ { "id": "trigger:review:review#1", "gate": "review", "mode": "ASK",
#     "origin": "review", "reason": "trigger review gate review on demo:pr:1", ... } ]
```

Approve it, using the `id` from `gate list` (the flag can come after the id — the
CLI handles that). The id is `trigger:<trigger>:<gate>#n` — a trigger can have
several named gates, so the gate name is part of the id:

```sh
verse gate resolve trigger:review:review#1 --approve
```

Now watch it self-drive. In Terminal A you'll see the stage advance **twice**:

```
{"op":"create","ref":"demo:pr:1","rev":1,"to":{"stage":"open"}}
{"op":"update","ref":"demo:pr:1","rev":2,"to":{"stage":"reviewing"}}   ← review action
{"op":"update","ref":"demo:pr:1","rev":N,"to":{"stage":"reviewed"}}    ← reviewed trigger, after the process finished
```

The second transition is the point: **nobody wrote "reviewing → reviewed."** The
review process completing became a `process:instance` entity change, which the
`reviewed` trigger reacted to. The flow emerged.

```sh
verse entity get demo:pr:1     # → { "stage": "reviewed" }
```

---

## 4. Anatomy of a trigger

You installed that two-rule charter in §3. Inspect and manage it:

```sh
verse trigger list
verse trigger show review
verse trigger remove <name>           # uninstall one (`add` rejects a duplicate name)
```

Now, what each field of a trigger means:

- **`condition`** — the *cause*, in two halves: **`when`** fires the trigger, and
  **`subject`** says what the firing is *about*. They are two different grammars, and
  that is the point:
  - **`when`** is a **match** — a point-in-time predicate over the verse. It ANDs its
    dimensions: `system` / `kind` / `id` / `op` (`set` for created-or-updated, or
    `delete`) / `fields` (key=value in the changed payload); empty dimensions are
    wildcards, and `{}` matches everything. The address string is sugar for the identity
    dimensions, so `"demo:pr:*"` means `{"system":"demo","kind":"pr"}`. *Keep it small* — the "when" is where
    complexity hides. (Create and update are one `set` — to fire "only the first
    time," match a state field your action then advances, a latch, rather than an
    edge. See `docs/change-events.md`.) A `when` may also grow *temporal* operators
    (`until`, `deadline`, … — the same language a gate formula uses, §8), at which
    point it is watched over the verse's evolution rather than matched against one
    change.
  - **`subject`** is the entity the firing acts on, named at the instant `when` fires.
    It resolves four ways, all the same match grammar:
    - *omitted* — the matched entity (`review`, above);
    - an **address** — `"demo:pr:1"` is that one entity (and an action's `set` will
      *create* it if it doesn't exist, which is how a trigger publishes a fact nothing
      has written yet). `*` is a wildcard per segment and an id may glob, so
      `"demo:pr:*"` is the whole kind, `"demo:pr:hotfix-*"` a family in it, and
      `"demo:*:*"` the whole system — the same address grammar as `verse flow <addr>`
      (§6). A glob **selects** what exists rather than naming, so it can never conjure
      an entity called `*`;
    - a `system`+`kind` selector (with an optional payload predicate) — the whole
      **set** of matching entities, and the action fans out, once per member. The
      object form is what an address glob parses to; reach for it when you also want a
      payload predicate;
    - a **binding** (`"$flow"`) — deferred: the address isn't known until the firing
      expands it against the changed payload. This is how the `reviewed` rule advances
      the PR a completed process carried. (An expansion is held to the same grammar, so
      a half-written ref is a no-op rather than a wildcard that fans out.)

    A firing can therefore act on 0, 1, or N entities — but every *process, flow, and
    goal* is still about exactly one subject. Broadcast, in other words, is a wider
    setting of the same selector, not a new mechanism:

    ```json
    { "name": "ship-reviewed",
      "condition": { "when":    { "system": "ci", "kind": "status" },
                     "subject": { "system": "demo", "kind": "pr",
                                  "fields": { "stage": "reviewed" } } },
      "gates":     [ { "name": "ship", "mode": "ask" } ],
      "action":    { "set": { "stage": "shipped" } } }
    ```

    One CI change fires this once, one `ask` gate is raised for the *firing* (not one
    per PR), and then every reviewed PR advances. A spawn would give each member its
    own process, tagged with that member as its subject — so each is its own flow (§6).
    Re-firing is idempotent per member: a spawn's id is derived from its subject, so a
    second broadcast reuses it rather than starting a duplicate.
- **`gates`** — a list of gates, *all* of which must resolve to proceed
  (conjunctive) before the action fires. Gates are per *firing*, so a broadcast asks
  once, not once per subject. Each is `{ "name": …, "mode":
  auto|ask|park }`. An empty list fires with no approval. Multiple gates model
  multi-approval — e.g. a deploy needing both a security and a release-manager
  sign-off is two `ask` gates. A gate can also carry a **`formula`** the verse
  resolves itself — a vote, a deadline, a threshold; see §8.
- **`action`** — data, not code, and it needs no target of its own: an action acts on
  the condition's **subject**.
  - `set` writes fields on the subject (a state transition);
  - `spawn` launches a process from a `launch.Spec` (`driver`, `task`, and later
    `policy`/`scope`/`environment`) — or, instead of a fixed `driver`, a governed
    **`provider`** selection of the doer (§8). The spawned process is tagged with the
    subject, which is what makes it part of that subject's flow (§6).
- **Bindings** in templates: every field of the changed payload is bound by name
  (`$flow`, `$stage`, `$path`, …), plus `$subject` — the *resolved* subject. So in
  `reviewed`, `subject: "$flow"` names the PR, and `$subject` inside the action is
  that PR, not the process that fired.

**Where `$flow` comes from.** It is an ordinary payload field, not a magic variable.
`review`'s `spawn` tags its process with the condition's subject (`demo:pr:1`), and the
process is mirrored into the verse as a `process:instance` entity carrying that subject
under the key **`flow`** — so when the process completes, the change event `reviewed`
matches has `flow: "demo:pr:1"` in its payload, and `$flow` binds to it like any other
field. It can't be spelled `$subject`: that binding is reserved for the firing's *own*
resolved subject, which for a `process:instance` change is the process, not the PR.

The `reviewed` rule is the **completion pattern**: *when* a `process:instance` reaches
`status=completed`, *about* the `$flow` it carried, advance that subject. It's how multi-step flows
close through the verse. (Triggers must opt in to `process:*` — the engine keeps
reflected process state out of the default match set so the loop can't ignite on its
own machinery.)

---

## 5. Processes

A trigger's `spawn` runs a process, but you can run one directly too. `run` prints the
process id (a `run-…`):

```sh
verse process run "summarize what verse is"           # runs an agent; prints the id
verse process run --wait "summarize what verse is"    # …and block for the final answer
verse process list
verse process get <id>        # snapshot: status, say, lastOutput, result, pending gates
verse process say <id> "also mention the dial"        # say to the agent mid-run (re-prompt)
verse process vector <id> ask --as human:scott        # slide autonomy (auto|ask|park); --as attributes the slide
verse process vector <id> --file vector.json          # …or install a full gate vector
verse process cancel <id>
```

`say` and `vector` are different levers: `say` re-prompts the agent (new instructions);
`vector` re-vectors it — swapping the gate vector that governs its autonomy, applied at the
next boundary with no restart. It works wherever the process runs: the local runtime slides it
directly, a brokered/remote worker gets it relayed through its inbox.

A direct run is mirrored as a `process:instance` entity like any other process, so
`verse entity get process:instance:<id>` shows what it produced — the latest `say`, last
tool output, and full `result` — even after it finishes.

**Which agent runs it.** With LLM creds configured, the default driver is a real **LLM
agent**; with no creds it falls back to **scripted** (a few canned steps, no credentials — so
the runtime is demoable anywhere). Choose explicitly per spawn with `--driver scripted|llm`.
The creds live with the **server** — the `up` process hosts the runtime, so it reads *its*
environment (or `~/.config/verse.tony`), not the `process run` client's; set them before `up`
(§1). Check with `verse status` — the `llm` block shows `available` and a masked key.

**Gates — an agent asks permission.** An agent's *reads* run freely, but *writing a file*,
*running a shell command*, and *delegating to a sub-agent* are **gated**: the agent blocks
until you approve. `run --attach` spawns *and* streams the transcript, and on a terminal it
**prompts you inline** when the agent blocks:

```sh
verse process run --attach --driver llm 'Write a file hello.txt containing "hi".'
# the agent narrates, then blocks and asks:
#   ⏸ gate "write" (write_file) — approve? [y/N] y
# → you answer, the write runs (wrote 2 bytes), and the process completes — one stream.
```

That's the autonomy dial at the process level: reads are auto, effects ask. Answer `y` to
allow or anything else to deny (a denied gate comes back to the agent as an error it re-plans
around). Piped (non-tty), `attach` just streams without prompting.

**Where the file went.** The agent reads and writes under its **root** — an isolated temp dir
by default (printed at `up` start: `agents rooted at …`), *not* your cwd. So `hello.txt` is at
`<that root>/hello.txt`. To have agents act in a directory you choose, start the server with
`verse up --root <dir>` (e.g. `--root .`) — then check `ls <dir>/hello.txt`. (Federating a
tree so an agent reads *through* the verse, wherever it runs, is `verse connect` — §10.)

You can also resolve out-of-band from anywhere — the gate shows up in **`gate list`**
alongside trigger gates (and in `process get <id>`), resolvable by its id (it has a `#`, so
quote it in zsh):

```sh
verse gate list                                       # engine + process gates, unified
verse gate resolve "run-…#d1" --approve               # or: process resolve run-… "run-…#d1" --approve
```

Because processes are entities, you can watch the whole fleet's lifecycle through the
verse:

```sh
verse watch --system process
# process:instance:review-1  running → step → step → completed
```

---

## 6. Steer flows

Steering is the control plane — out-of-band from observation, honored at a process's
next boundary, never a store write. Each command acts on a **flow**: the *in-flight*
processes tagged with a subject (a PR, a change, a release).

A flow is **not a stored thing** — it is a *query* over the subjects of live processes. That
is why steering takes an **address**, not just a ref, and why steering a whole set needs no
new machinery: it is the same selector at a wider setting.

A flow exists only while its processes run, so give yourself one to steer — open another
change and approve its review, so a review process spawns under `demo:pr:2`:

```sh
verse entity put demo:pr:2 '{"stage":"open"}'
verse gate list                       # find the new review gate's id, then:
verse gate resolve <that-id> --approve
verse flow list                       # subjects with in-flight processes
```

Now steer that subject's flow:

```sh
verse flow pause  demo:pr:2           # freeze its agents at their next boundary
verse flow resume demo:pr:2           # release them
verse flow say    demo:pr:2 "hurry"   # inject an instruction
verse flow cancel demo:pr:2           # stop its processes (destructive)
```

…or steer a whole **set** of subjects with the same verbs, since the address is a selector
(`*` is a wildcard per segment):

```sh
verse flow pause 'demo:pr:*'          # every PR's in-flight work
verse flow say   'demo:*:*' "hurry"   # everything in the demo system
verse flow list                       # the distinct subjects with live work
```

Each command reports how many processes it reached, so `{"paused": 2}` tells you the address
selected two.

They fan out to the addressed processes and land at each one's next boundary, so a flow can
be held, redirected, or stopped mid-flight — while every *other* flow keeps reacting.
A `scripted` review is brief, so steering matters most for a real, longer-running
agent — the `llm` driver, or a longer task — where `pause`/`say`/`cancel` take hold
mid-run.

`pause` is the operator's ad-hoc freeze ("hold this flow, I want to look"), distinct from
`cancel` (which ends the work) and from a `park` gate (a hold the *charter* declares, per
action). `pause` is you, live, holding **this** subject's work.

Why "out-of-band, with priority" matters: an intervention that has to queue behind a
backlog of observations is an intervention defeated. Control never rides the change
stream — cancel/say/pause reach the process directly and are serviced ahead of
observation. (See `docs/deployment-and-steering.md` for the invariant and the
engine's priority servicing.)

---

## 7. Human-executed processes (dial-in)

Most processes are run by an agent driver — but a process can be run by a **human**, or by
any external, firewalled worker, who **dials in** to do the work. verse hosts the process
behind a *broker* and publishes where to dial in; the executor claims it and reports back.
Nothing inverts: both verse and the worker are clients of the broker, so a laptop or a pod
behind a firewall works without verse reaching in.

Start verse with a broker as the run policy (a sibling port serves the dial-in face):

```sh
verse up --broker --broker-addr :9090
```

Now spawn a process. With a broker policy it **lands on the broker awaiting an executor**
instead of running itself — `process list` shows its policy and where to dial in (you never
type that URL; verse resolves it for you):

```sh
ID=$(verse process run "review the auth change")
verse process list
# { "id": "run-…", "policy": "broker",
#   "dialURL": "http://localhost:9090/broker/<nonce>", "status": "running" }
```

You are the executor. Do the work with `verse process do` — every action takes `--as
<identity>` (a claim must be attributable), and the broker URL is resolved for you from the
process (no URL to type):

```sh
verse process do note "$ID" "reviewing the auth change" --as human:scott
```

**Raising a gate.** Mid-work you reach a decision you shouldn't make alone. Raise it *up* to
verse rather than deciding — the verse governs who resolves it (an operator, a reviewer, a
vote, or, if the dial allows, you); the executor never resolves its own gate. `--wait` blocks
for the outcome:

```sh
verse process do gate "$ID" deploy-approval --as human:scott --reason prod --wait
# gate raised: deploy-approval-a1b2c3d4
# awaiting resolution…
```

In another terminal, someone with the authority resolves it — your `--wait` unblocks:

```sh
verse process resolve "$ID" deploy-approval-a1b2c3d4 --approve
```

Then finish the work (or `do fail "$ID" <reason>` to end it the other way):

```sh
verse process do complete "$ID" "LGTM — one nit on the token TTL" --as human:scott
```

The process is now `completed`, with your result on its `process:instance` entity — so a
completion trigger reacts to it exactly as if an agent had finished (§3–4). A *human* did the
work; the flow moves on.

So a process has **two vantages**, and every verb works whether it runs locally or behind a
broker:

- **operator** (from outside — drive/watch): `list` · `get` · `attach` · `cancel` · `say` ·
  `resolve`
- **executor** (from inside — the worker leg): `do complete|fail|note|gate`, plus `inbox` to
  see the control queued for the worker.

The broker is where a human sits on the dial of autonomy: the same process the engine drives
and steers, done by a person — observed and governed through the one verse. (See
`docs/control-policy.md` for the seam and the dial-in protocol.)

---

## 8. Gates the group decides — *whether*, and *who*

In §4 a gate was `auto` · `ask` · `park` — one answerer, one click, deciding *whether* an
action proceeds. But a gate is really just **a question the charter poses**, and some
questions aren't one person's to answer in one click: a merge that needs *two* reviewers;
a doer *chosen* from several. verse resolves those the same way — it puts the open
question to the group as a **motion** in the store, watches ballots accumulate, and
commits an **outcome** when a tally reaches the bar. One machinery, two questions:

- **whether** an action fires — the answer is `proceed` or `deny`.
- **who** does the work — the answer is *a choice of doer*.

Both are answered the same three ways — `auto` (verse picks by a rule), by **one person**,
or by the **group** — over the one votable, attributable surface (`verse docket` /
`verse vote`). (A one-click `ask` still lives in `gate list` / `gate resolve`; a *motion*
is what an answer that accumulates over time needs.)

### Whether: a vote

Say a merge needs **two reviewers**. Give the gate a **formula** instead of a mode — the
verse watches and resolves it itself:

```json
{ "name": "merge",
  "condition": { "when": { "system": "change", "fields": { "stage": "ready" } } },
  "gates":     [ { "name": "approvals", "formula": { "quorum": 2 } } ],
  "action":    { "set": { "stage": "merged" } } }
```

```sh
verse trigger add -f merge.json
verse entity put change:pr:42 '{"stage":"ready"}'
```

The trigger fired, but the action is waiting — not on a person, on a **tally**. The open
question is now a motion on the docket, where ballots land and the count is watched:

```sh
verse docket
# a1b2c3  whether open  options=[proceed(2)]  subject=change:pr:42
```

The **options** are what can win — here just `proceed`, at a bar of 2. Cast ballots (each
names its voter, since a motion must be settled attributably):

```sh
verse vote a1b2c3 approve --as alice     # 1 of 2 — still open
verse vote a1b2c3 approve --as bob       # 2 of 2 → carries
verse docket
# a1b2c3  whether closed  options=[proceed(2)]  outcome=proceed tally=2  subject=change:pr:42
verse entity get change:pr:42            # → { "stage": "merged" }
```

Each `verse vote` writes a ballot into the motion's log; verse *watches the log* and
commits `proceed` when the count reaches the bar. It stays **live**: if alice retracts
before it commits, the tally drops and the gate re-blocks. Nobody owns the motion; the
group carries it.

**Saying no.** A vote you can only win isn't a vote. Give the motion a *second* option —
read `until` as *approvals, unless the rejections get there first*:

```json
"formula": { "until": [ {"quorum": 2}, {"quorum": {"choice":"reject","n":2}} ] }
```

Now the motion has two options, `proceed` and `deny`, and the first to *its* bar wins.
Two rejects close it `outcome=deny` and the action never runs. "No" is a real outcome, not
the absence of "yes."

**A deadline.** Votes stall, and you don't want a motion open forever. Make the second
option a **deadline** — the clock is what satisfies it:

```json
"formula": { "until": [ {"quorum": 2}, {"deadline": "4h"} ] }
```

```sh
verse docket
# a1b2c3  whether open  options=[proceed(2)]  closes=2026-07-23T08:15:36  subject=change:pr:42
```

Two approvals first ⇒ `proceed`; the clock first ⇒ the deadline **defaults** the motion
to `deny` (action skipped). The deadline is per-motion and durable (a restart re-reads
`closes` and honors the elapsed wall time). For a real external signal instead of a clock,
the right side can be a `fact` — `{"fact": "ci:status:green"}` waits for CI, a tag, a
posted review.

(The compact `{"quorum": 2}` / `{"until": […]}` form is a small formula language: a leaf is
a requirement — `quorum`, `fact`, `deadline` — and operators compose them (`until` today;
`and`/`or` coming). It's tony's tags in JSON; a tony charter reads `!until [ !quorum 2, … ]`.)

### Who: a selection

The second question hides in every `spawn`: **who** does it. In §4–5 a spawn named a fixed
`driver`. That's a motion too — over a different option set. Name the eligible doers and
how to choose, instead of one driver:

```json
{ "name": "review",
  "condition": { "when": { "system": "change", "fields": { "stage": "open" } } },
  "action": { "spawn": { "task": "review $subject",
    "provider": { "eligible": ["gpt", "claude"], "select": "vote", "need": 2 } } } }
```

`select` is **the same dial, on the *who* axis**: `auto` (a rule picks the first eligible),
`ask` (one person picks), `vote` (the group picks). It raises the *same kind* of motion —
the options are now the doers:

```sh
verse docket
# d4e5f6  select open  options=[gpt(2) claude(2)]  subject=change:pr:1
```

Same docket, same ballots — but the answer is a *choice*:

```sh
verse vote d4e5f6 claude --as x
verse vote d4e5f6 claude --as y
verse docket
# d4e5f6  select closed  options=[gpt(2) claude(2)]  outcome=claude tally=2
```

The process spawns on the chosen doer. The pick is fixed for that run; reassigning means
cancel-and-respawn, not a live swap mid-flight. (A `within` deadline works here too — if no
doer reaches the bar in time, the motion closes with no pick and nothing spawns.)

**A roster of doers.** Listing driver names inline gets old, and real doers have attributes
— a tier, a cost, a specialty. Register them once (they're just entities) and select by
**what they are**:

```sh
verse entity put provider:review:claude '{"driver":"llm","tier":"premium"}'
verse entity put provider:review:gpt    '{"driver":"llm","tier":"premium"}'
verse entity put provider:review:cheapo '{"driver":"scripted","tier":"basic"}'
verse providers
# review  claude  driver=llm      tier=premium
# review  gpt     driver=llm      tier=premium
# review  cheapo  driver=scripted tier=basic
```

Now the action selects by **capability + a match over attributes**, not by name:

```json
"provider": { "capability": "review", "match": { "tier": "premium" },
              "select": "vote", "need": 2 }
```

```sh
verse docket
# … select open  options=[claude(2) gpt(2)] …
```

`cheapo` never made the ballot — it isn't `premium`. That `match` is a real verse match
pattern over each provider's attributes, so "premium reviewers" (or later "anything under
$5") is just a pattern. The eligible set is discovered *at fire time* from the registry —
add or retire doers by writing or deleting entities, no charter change.

Two questions — *whether* and *who* — one votable, attributable machinery.

---

## 9. Goals — give a flow a destination

Everything so far pushes the world *forward*: a change fires a trigger, a gate decides
whether and who, a process runs, its completion fires the next trigger. Nothing yet says
where a flow is *going*, or when it is *done*. A **goal** adds that — an explicit target —
without giving up the emergent model. The move worth holding onto: **a goal drives by
*owning charter*, not by reconciling.** The triggers still do the doing; the goal owns them,
watches for success or failure, and holds a live status.

A goal is three things: a **`doneWhen`** predicate (the target), an optional **`deadline`**
(a fail-latch), and a bundle of **owned triggers** (the doing). `doneWhen` is the very
formula language you gate with (§8) — but here it's a *target*, not a gate:

```sh
verse goals add ship --subject change:pr:42 \
  --done-when '{"fact":"ci:status"}' --deadline 4h
```

```sh
verse goals list
# ship  status=blocked  waitingOn=[fact:ci:status]  deadline=…  subject=change:pr:42
```

The goal is `blocked` — it names what it is **waiting on** (the unmet leaves of `doneWhen`),
recomputed live as the world changes. It has spawned nothing; it is watching. Make the
target true — however that happens, a trigger, a source, or a person:

```sh
verse entity put ci:status:build '{"state":"green"}'
verse entity get goal:done:ship
# { "goal": "ship", "subject": "change:pr:42", "witness": ["ci:status:build"] }
```

When `doneWhen` holds, the goal **latches** — once, and it stays latched (retract the fact
and the goal does not un-finish). Two outcomes are first-class: **done**, and
**unachievable** — the deadline is the fail side, exactly like §8's deny-quorum ("no is a
real outcome"). Let a deadline pass with the target still unmet and the goal latches
`goal:failed` with `status=unachievable`.

Here is the distinction worth holding. The latch is an **ordinary fact** — `goal:done:ship`
is just an entity. So a goal *recognizes*; it never *advances* anything itself. If you want
the finish to carry a subject onward, a **trigger** reacts to the latch — the same emergent
way everything else composes:

```json
{ "name": "on-ship",
  "condition": { "when": { "system": "goal", "kind": "done" }, "subject": "$flow" },
  "action":    { "set": { "stage": "shipped" } } }
```

And because a goal's owned triggers spawn processes tracked as **flows**, a goal *is a flow
with a destination* — steer it with the very same verbs (§6), keyed by the goal:

```sh
verse goals pause  ship          # freeze its in-flight work
verse goals say    ship "hurry"  # inject an instruction
verse goals cancel ship          # stop it
```

To ship the owned triggers *with* the goal instead of hand-authoring them, hand `add` a full
record — `verse goals add -f ship.json` — whose JSON carries `triggers: [ … ]` alongside
`doneWhen`. On a terminal, done or unachievable, the goal reaps its owned triggers, so a
finished goal leaves no orphaned charter.

*(v0: `doneWhen` is a single fact/quorum atom; multi-condition targets, an optional plan that
generates the owned triggers, and scope-isolated reap are next — §13.)*

---

## 10. Federate your local working tree

`verse connect` federates your working tree into the verse under a private,
per-session **scope**, and wires a validator to every file — the local-dev loop. It
is its own process (a second terminal); leave it running. In one command it:

- **writes** each file's rich metadata (full fstat + a content hash) to the store as
  `local:dir-status-<scope>:<path>` entities — an *index, not a warehouse*: the stat
  and hash, not the bytes;
- **serves** file **contents on demand** through docd (a content controller at
  `local:dir-content-<scope>:*`), so a validator reads them *through the verse* — no
  shared filesystem required;
- **installs a scoped trigger** that spawns a validator per file.

It all lives in a nonce scope, isolated from baseline and from other devs; on exit
`connect` drops it.

In terminal A, start the daemon. Configure LLM creds first (a `~/.config/verse.tony`
`llm` block, or `PORTKEY_*`/`OPENAI_MODEL` in the environment) so the validators can run
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
verse process list                     # a validator per file — local-dev feedback!
verse process get <id>                 # its result: the agent's read-and-validated report
verse entity list --system local --scope <scope>       # your files, as dir-status entities
verse entity get local:dir-content-<scope>:README.md   # a file's bytes, served on demand
```

Touch or add a file → its validator fires and reads it **through the verse**. Ctrl-c
`connect` (or `verse disconnect --scope <scope>`) tears the scope down — trigger,
overlay, and content mount all gone. (For a credential-free smoke test, drop the creds
and `--driver llm`: a validator still fires per file — on the scripted driver — it just
doesn't read.)

Because contents flow through docd rather than a server-side `--root`, the runtime
need not share a filesystem with your tree — the validator can run elsewhere. (`--root`
remains only for a non-federated single-machine agent; see
`docs/content-federation.md`. Related: file reads are still truncated today, so agents
validate large files from a partial view.)

---

## 11. Scopes

Every client command takes `--scope <name>` (baseline by default). A scope is a
**copy-on-write overlay**: reads see the scope layered on the shared baseline, and
writes land in the scope without touching baseline — your local changes and flows,
isolated:

```sh
verse connect . --scope me
verse watch --scope me
verse entity put demo:x:1 '{"k":"v"}' --scope me   # in the scope only
verse entity get demo:x:1                           # baseline: not there
verse disconnect --scope me                          # drop the whole overlay
```

`disconnect --scope me` tears the overlay down in one shot (every scope write, not
just `local:*` files) via logd's `DeleteScope`.

**On which store:** real COW scopes require the **docd/logd** store — which is now
`up`'s default. On `--store memory` `--scope` is a *passthrough* (no isolation), and
`disconnect --scope` falls back to removing `local:*` entities.

---

## 12. Command reference

```
verse
  up   [--root <dir>]                        start the monolith (engine + store + proc)
       [--llm]                               make `llm` the default driver (per-spawn --driver still wins)
       [--store memory]                      opt out of docd (no daemons, no persistence)
       [--data <dir>]                        bring logd+docd up on <dir> (default: fresh temp dir)
       [--docd-addr host:port]               attach to a running docd (client face) …
       [--docd-mount-addr host:port]         … and its controller/MOUNT face
       [--o-bin path]                        the o binary to spawn daemons (default: go run pinned cmd/o)
       [--policy <url>]                      run processes on a remote controlPolicy base URL …
       [--broker      ]                      … or on an in-process dial-in broker (needs --broker-addr;
                                             publishes each process's dialURL from it) …
       [--broker-addr host:port]             … the sibling port serving this verse's broker face
                                             (/broker/<nonce>); all default off (local in-process
                                             supervisor). See docs/control-policy.md.
  status                                     engine/store snapshot
  down                                       stop the verse on --addr (it reaps its spawned daemons)

  watch  [--system --kind --since <rev>]     stream store changes (cursored)
  entity (e)  get <ref> · put <ref> <json> · list [--system --kind]

  gate    list · resolve <id> --approve|--deny [--say <text>]
  docket                                     list open motions (votes + provider selections)
  vote    <motion> <choice> --as <who>       cast a ballot (approve|reject, or a select-option)
  providers                                  list the provider registry (provider:<capability>:<id>)
  trigger add [-f <file>|-] [file] · list · show <name> · remove <name>   (add rejects a duplicate name)
  process (ps)  run <task…> [--attach] [--wait] [--driver scripted|llm]  (honors policy; mirrored)
                list · get <id> · attach <id> · inbox <id>       operator vantage (any backend):
                cancel <id> · say <id> <text…> · resolve <id> <decision> --approve|--deny
                do complete|fail|note <id> [text] --as <identity>   executor vantage (dial in):
                do gate <id> <name> --as <identity> [--reason <t>] [--wait]   raise a gate up
  flow    list · pause <addr> · resume <addr> · cancel <addr> · say <addr> <text…>
          <addr> addresses live processes by the subject they are about: demo:pr:1 (one),
          demo:pr:* (a kind), demo:*:* (a system) — a flow is a query, not a stored key
  goals   list · add <name> --done-when <formula> [--subject <ref>] [--deadline <dur>]
          add -f <file> (full record incl. owned triggers) · remove <name>
          pause|resume|cancel <name> · say <name> <text…>            (alias: goal)

  connect [path] [--task <t>] [--driver <d>]  federate a tree (own scope): scoped metadata +
          [--interval <dur>]                  content controller + a validator per file
  disconnect                                  drop the --scope overlay (else remove local:* entities)

  global:  --addr <host:port>   --scope <name>   -o <format> (tony-format; TODO)
```

Flags may appear before or after positionals (`gate resolve <id> --approve` works).

### Configuration

Defaults are resolved through layers, each overriding the one before:

1. `/etc/verse.tony` — system base
2. `~/.config/verse.tony` — per-user; it **patches** the system file (a tony merge, so
   you set only the keys you want to change; everything else falls through)
3. environment variables
4. CLI flags

The files are [tony](https://github.com/signadot/tony-format) documents; a missing file
is simply an absent layer. Example `~/.config/verse.tony`:

```tony
addr:  "localhost:9797"
scope: "me"
llm: {
  model:      "gpt-4o"
  apiKey:     "pk-…"
  virtualKey: "vk-…"
}
```

`addr` and `scope` are read by every command (they say which verse to talk to). The `llm`
block, however, is read by the **server** (`verse up`) — it hosts the process runtime, so
that's whose config/environment the agent driver reads for its model + creds; setting it for
a `process run` *client* does nothing. The equivalent env is `OPENAI_MODEL`,
`PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY`, `PORTKEY_BASE_URL` (which override the file).
Precedence for any value is, low → high:
`/etc/verse.tony` → `~/.config/verse.tony` → environment → CLI flag.

**The driver is chosen per spawn.** A trigger's `spawn.driver`, `connect --driver`, and
`process run --driver` each select `scripted` or `llm`; an empty driver uses the default
(`scripted`, or `llm` when `up --llm` is set). Requesting `llm` with no configured creds
fails **that** process (status `error`, with a message telling you to set the creds) —
the engine keeps running.

---

## 13. What this is (and isn't) yet

- **Store** defaults to **[docd](https://signadot.github.io/tony-format/docd/)**
  (durable, observable, controller-federated). You never have to touch docd to *use*
  verse — `up` brings it up for you — but it is where verse's deeper properties come
  from, and they're hard to see spelled out anywhere else: **COW scopes** (§11),
  **conditional + coordinated writes** (atomic CAS, so two writers advancing one entity
  can't lose each other's fields — see `PatchIf` below), **edge-faithful watches** (the
  change stream *is* each writer's applied patch, so no reader reconstructs, reorders,
  or drops an edit), and **compactions** (the log stays bounded while the current state
  is preserved). With no
  store flags, `up` brings up logd + docd on a fresh temp data dir via `o sys up` and
  points the store at docd's client face; `--data <dir>` uses a durable dir,
  `--docd-addr`/`--docd-mount-addr` attach to an already-running docd, and
  `--store memory` opts out entirely (no daemons, no persistence). The store maps each
  entity to a kinded path `verse.<system>.<kind>.<id>`, reads/writes via Match/Patch,
  and drives the change stream + revision cursor from a single logd watch; `Since` is
  served natively by a `FromCommit` replay watch, so a long hold costs a cursor, not a
  buffer. Commit numbers are monotonic but *sparse*, so the engine treats the revision
  as an opaque cursor and reconciles via `Since`. Writes are atomic via CAS
  (`PatchIf`), so concurrent modifies don't lose updates. (Op is `set`/`delete`;
  create≡update, since logd delivers the writer's applied patch verbatim.)
- **The reactive grain is the entity, not the transaction** — *subtle, and may need
  cleaning up.* A single docd commit can atomically patch many entities, but the store
  normalises each commit into one change event **per entity** (`walkEntities`, at
  path-depth `system/kind/id`), all sharing that commit's revision. That normalisation is
  exactly what guarantees a trigger's **subject is always one entity**: dispatch is
  per-entity-edge, so `$subject` — and the flow or goal keyed on it — is never ambiguous,
  even under a broad match (a broad pattern just fans out to one firing per matching
  change). The cost is that transaction atomicity is flattened: the engine cannot react to
  "these two changed together in one tx" as a unit — the shared revision is the only trace,
  and nothing groups by it. A load-bearing assumption worth naming; revisit if cross-entity
  transactional reactions are ever needed. See `docs/change-events.md`.
- **Scopes** are real copy-on-write overlays on the docd/logd store (writes isolated,
  reads layered on baseline, `disconnect --scope` drops the overlay via `DeleteScope`);
  on `--store memory` they remain a passthrough.
- **Local federation** — `verse connect` federates your working tree in a private
  scope: scoped `local:dir-status-<scope>:*` metadata writes + a content controller
  serving `local:dir-content-<scope>:*` bytes on demand, plus a scoped trigger that
  spawns a validator per file. A scoped agent's `read_file` resolves content *through
  the verse*, so the runtime need not share a disk with the tree. The scope's engine
  observes via **per-trigger subtree watches** — each trigger watches exactly the
  `system.kind` it matches, not a root watch. See `docs/content-federation.md`.
- **Sources** — the generic pattern for reflecting *external* systems (git, GitHub,
  Linear) into an owned verse slice through a docd controller (webhook-hint +
  poll-reconcile) is designed; the first, a **git Source**, is next. See
  `docs/sources.md`.
- **`-o`** accepts a format flag but currently prints JSON; tony-format formatters
  are the next step.
- **Deployment**: today `up` runs the engine + proc runtime in one process and spawns
  logd+docd as children (or attaches to a running docd). The pieces — store, engine
  (`versed`), process runtime (`proc`) — are separable services behind the same
  interfaces; see `docs/deployment-and-steering.md`.
- **controlPolicy**: how a process is *run* is a seam. The default is the in-process
  supervisor, but `up --policy <url>` hosts processes on a remote verse, and `up --broker`
  hosts them behind a broker a firewalled worker (or a human) dials *in* to — verse drives
  all three through the identical handle, and `process list`/`get`/`attach`/`cancel`/`say`/
  `resolve` work across all of them. A human or external worker executes via `verse process
  do` (§7). Left to do: inbox long-poll. See `docs/control-policy.md`.
- **Governed decisions** — one machinery answers two questions. *Whether* an action fires
  is a gate whose **formula** the verse resolves itself: a quorum **vote** (with a
  deny-quorum for "no"), a **deadline** against the clock, or an external **fact** (CI
  green, a tag). *Who* does the work is the same motion over a different option set — the
  doer, chosen from an inline set or a `provider:*` registry filtered by a match. Both
  raise one **motion**: options accumulate ballots, the first to its threshold
  wins, and the winner commits terminal-once (with a live retract guard), observed via
  `verse docket` / `verse vote` (§8). Left to do: richer disciplines (ranked/weighted
  votes, review panels), secret ballots via scopes.
- **Goals** — an explicit *target* over the reactive substrate: a goal owns a bundle of
  triggers (the doing), a `doneWhen` formula, and a deadline; it latches `goal:done` /
  `goal:failed` (success *and* "unachievable" are both first-class), holds a live status
  with a `waitingOn` list, and steers as a flow — yet drives by *owning charter*, not by
  reconciling (§9). Left to do: temporal `doneWhen` (eventually/always/sequence), an
  optional plan that generates the owned triggers, and `DeleteScope`-based reap of a goal's
  owned charter.

That's the whole thing: **install a charter, react to events (external and
local-dev) with gated agent processes — gates a person, the group, or the clock can
answer, and that choose who acts — advance state emergently on completion, steer
flows out-of-band, observe everything through one verse.**
