# Verse — a hands-on tutorial

Five sections, each one you can drive. Two things exist in a verse — **entities**, which are
what the world is, and **processes**, which are the work being done to it — and everything
after §3 is about relating the two.

Every section has a script that puts the world there in one command, so you spend your time
on what verse does rather than on typing fixtures. For the shape of the system read
[the overview](overview.md); for the flat command surface, the config layers and the
per-topic detail this tutorial skips, [the reference](reference/README.md).

---

## 1. Start

```sh
go build -o verse ./cmd/verse     # or: make bin
export VERSE_ADDR=localhost:9797  # where clients reach it (this is the default)
./verse up                        # engine + process runtime + store, in one process
```

Leave it running; every other subcommand is a client, so open a second terminal. Then, to
see it working before reading about it:

```sh
./scripts/seed-demo.sh     # a charter, the entities it reacts to, one gate waiting for you
```

`up` decides three things for the whole runtime, and the defaults are chosen so that none of
them need your attention now: the **store** is a fresh docd on a temp dir (`--data <dir>` to
keep it), the default **driver** is a real llm agent when creds are configured and `scripted`
— a task run as a shell command — when they are not, and an agent's **root** is an isolated
temp dir it prints at start. Which is why a file an agent writes is not in your cwd.

All three, plus the config layers, the `o` binary `up` resolves to host its daemons, and the
flags for pointing at a store or a process runner you already have, are in
[reference/up.md](reference/up.md).

---

## 2. Entities

An entity is anything the flow refers to — a PR, a build, a person, a CI status — addressed
by a `system:kind:id` triple and carrying a document. That is the whole data model.

```sh
./scripts/seed-demo.sh --clean   # if you ran it above: its rules react to what we seed next
./scripts/seed-entities.sh       # a dozen entities, three systems, nothing watching them
```

### Read

`--system` and `--kind` narrow by *where* an entity lives; `--if` narrows by *what it says*,
as a [tony match](https://signadot.github.io/tony-format/) against the payload:

```sh
verse entities                                        # everything
verse entities --system demo --kind pr                # by where it lives
verse entities --if '{stage: open}'                   # by what it says
verse entities --if '{stage: !or [open, merged]}'     # …an operator, not just equality
verse entities --if '{urgent: true}' --if-at labels   # …rooted inside the payload
verse entity get demo:pr:1
```

`--if-at` takes a dot-separated field path and roots `--if` there, so a predicate about a
subtree need not restate the shape above it: `--if-at labels --if '{urgent: true}'` is
`--if '{labels: {urgent: true}}'`, and it also reaches a subtree that is not an object. A
path that leads nowhere does not match.

The pattern language is tony's, not verse's — `!or`, `!glob`, `!not`, `!and` and the rest are
[tony match operators](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/mergeop),
and they mean the same thing here, in a charter's `value`, and in an answer contract. Learn
it once. What verse adds is *where* a match may be asked, which is [§4](#4-charters--relating-entities-to-processes).

### Write

A put **merges**. Fields you do not name are left alone, which is why the answer is the
entity as it now stands rather than an echo of what you sent:

```sh
verse entity put demo:pr:1 '{owner: dee}'              # changes the owner, keeps the stage
verse entity put --at labels.urgent demo:pr:1 true     # one field, deep
verse entity rm  --at labels.urgent demo:pr:1          # and gone again
verse entity rm demo:scratch:1                         # the whole entity
```

A payload typed on the command line is read as tony, which is why `{stage: open}` needs no
quotes around either half, and why commas are optional. Save it as `.json` or `.yaml` and
the same command reads that instead — they are dialects of one document model rather than
three supported formats ([representation](reference/representation.md)).

A write can also refuse to happen. Each condition is checked *with* the write, in one step at
the store, so nothing can change in between:

```sh
verse entity put --create demo:pr:1  '{stage: open}'   # only if nothing is there yet → refused
verse entity put --update demo:pr:99 '{stage: open}'   # only if something is        → refused
# write refused: a condition did not hold

verse entity put --if '{stage: merged}' demo:pr:4 '{owner: dee}'   # only if it still looks like this
```

That is how two writers advance one entity without losing each other's fields — CAS spelled
as a predicate, so you say what you believe about the entity rather than which revision you
read it at. It is the same predicate language `--if` reads with, and a refused write leaves
the entity alone and exits non-zero.

### Watch

```sh
verse watch --system demo --if '{stage: open}'
```

```
{op: set ref: demo:pr:1 rev: 1 to: {labels: {area: auth urgent: true} owner: ana stage: open …}}
{op: set ref: demo:pr:3 rev: 3 to: {labels: {area: store urgent: true} owner: ana stage: open …}}
{op: set ref: demo:pr:5 rev: 5 to: {labels: {area: engine} owner: cy stage: open …}}
```

Two things to read off that. It **replays** — a watch is level-triggered at the open, so what
already matches arrives before anything new does, and you are never watching a stream whose
beginning you missed. And each change is one line, so a watch stays greppable: `to` is the
entity's whole state as of that commit rather than the delta, and `op` is `set` (created *or*
updated) or `delete`.

Now, in another terminal, write to it and watch the line arrive:

```sh
verse entity put demo:pr:2 '{stage: open}'
```

Then advance it past the filter (`'{stage: reviewing}'`) and note that nothing arrives: the
filter is a predicate over the state, not a subscription to the entity.

More on refs, deep writes, cursors and `--since` in
[reference/entities.md](reference/entities.md).

---

## 3. Processes

A process is a governed unit of work — an agent, a shell command, a remote worker, a person.
Run one directly:

```sh
verse process run --driver scripted "echo summarizing; sleep 3"   # prints a run-… id
verse process run --driver scripted --wait "echo done"            # …and block for the result
verse processes
verse process get <id>                # status, task, driver, runner, result, pending gates
verse process say <id> "also mention the dial"        # re-prompt the agent mid-run
verse process vector <id> ask --as human:scott        # slide its autonomy (auto|ask|park)
verse process cancel <id>
```

`--driver` is explicit here because the default is not: with LLM creds configured a bare
`process run "echo done"` hands your sentence to an *agent* ([reference/up.md](reference/up.md#the-driver)).

`say` and `vector` are different levers: `say` gives the agent new instructions, `vector`
swaps the gate vector governing how much it must confirm, applied at its next boundary with
no restart.

### The dial

The two drivers carry different vectors, and the difference is the whole point of a gate: it
asks about what you have **not** already authorized.

`scripted` runs ungated — its task *is* the shell command, so you authorized it by typing it.
`llm` asks before effects, because the agent *chose* them. Reads run freely; writing a file,
running a command, and delegating to a sub-agent block:

```sh
verse process run 'Write a file hello.txt containing "hi".'
verse gates
# - gate: write
#   id: run-f3e31ed0390f~d1
#   mode: ASK
#   process: run-f3e31ed0390f
#   reason: "verb write"
#   tool: write_file
#   where: process
verse gate approve run-f3e31ed0390f~d1
```

`run --attach` spawns *and* streams the transcript, prompting you inline on a terminal
(`⏸ gate "write" (write_file) — approve? [y/N]`) — while the same gate stays resolvable from
anywhere else by its id, since it is the same question either way. There is a rough edge in
which of the two rules applies when you hand verse a *command* and it satisfies it with an
*agent*; [reference/processes.md](reference/processes.md#who-you-addressed) states it, and
[authority](roadmap/authority.md) is where it gets fixed.

### A process is an entity too

This is the join between the two halves of the verse. Every process is mirrored into the
store as `process:instance:<id>`, so observing the fleet is observing entities:

```sh
verse watch --system process              # the whole fleet's lifecycle
verse entity get process:instance:<id>    # its result, still there after it finished
```

```tony
payload:
  lastAction: run_bash
  lastOutput: summarizing
  runner: local
  status: done
  vector: ASK
  vectorBy: human:scott
```

Because a completion is an entity change, a *rule* can react to it — which is how multi-step
flows close, through the store rather than through a scheduler. That is §4.

### Steering

In-flight work is addressed by the subject it is about — a *flow* — and steering is
out-of-band from observation, so it never queues behind a backlog of changes:

```sh
verse flows                             # the distinct subjects with live work
verse flow pause  demo:latch:1          # freeze its processes at their next boundary
verse flow resume demo:latch:1
verse flow say    demo:latch:1 "hurry"
verse flow cancel demo:latch:1          # stop them (destructive)
verse flow say 'demo:*:*' "hurry"       # the address is a selector: * per segment
```

A flow is not a stored thing — it is a query over the subjects of live processes — so
`verse flows` is empty until something is running, and each verb reports how many processes
it actually reached. §4 gives you one to steer. See
[deployment and steering](deployment-and-steering.md) for why control never rides the change
stream.

### Where a process runs is a seam

It need not run here. The same verbs drive a process hosted on a remote verse
(`up --runner <url>`), one behind a dial-in broker that a firewalled worker claims
(`up --broker`), and one a **person** executes by hand (`verse process do …`). verse drives
all three through one handle, so nothing above changes. That is its own subject:
[process control](process-control.md) for the seam and the dial-in protocol,
[external processes](external-processes.md) for why it was cut there, and
[reference/processes.md](reference/processes.md) for the commands.

---

## 4. Charters — relating entities to processes

A **charter** is a list of rules. A rule says: when a matching entity changes and the gates
resolve, act on it — patch it, spawn a process about it, and react to what that process did.
Entities and processes, related declaratively; nothing else in verse authors a flow.

Every rule has the same four parts, and the whole firing rule is one sentence:

> At each commit, if the cause has risen and the condition holds, fire once for **every
> entity `subject` selects** — one firing per entity, each with its own gate, its own process
> id, its own budget.

What varies between rules is which parts are present. Those variations are the shapes worth
knowing, and each is one command:

```sh
./scripts/charter-shapes.sh                  # list them
./scripts/charter-shapes.sh show    <shape>  # the rule, in tony
./scripts/charter-shapes.sh run     <shape>  # install it, then make it fire
./scripts/charter-shapes.sh install <shape>  # …or the halves apart, which is its own lesson
./scripts/charter-shapes.sh drive   <shape>
./scripts/charter-shapes.sh clean            # remove every shape and what it acted on
```

Each shape acts on its own kind — `demo:latch:*`, `demo:seq:*` — so you can install several
at once without them arguing over an entity.

### patch — a rule need not spawn anything

```tony
- name: shape-patch
  condition:
    subject: { system: demo, kind: patch, value: { stage: open } }
  action:
    patch: { stage: triaged }
```

```sh
./scripts/charter-shapes.sh run patch
verse entity get demo:patch:1     # stage: triaged
```

The action has no target of its own: it acts on the condition's `subject`.

A rule also acts on the **backlog**, which is worth seeing rather than being told:

```sh
./scripts/charter-shapes.sh remove patch          # nothing is watching
verse entity put demo:patch:2 '{stage: open}'
verse entity get demo:patch:2                    # stage: open — nobody reacted
./scripts/charter-shapes.sh install patch
verse entity get demo:patch:2                    # stage: triaged — installing it acted
```

A trigger's observation replays what it reads when it opens, so a rule is a statement about
the world rather than a subscription to future changes: you never have to re-touch an entity
to announce a state it has held all along, and a restart re-reaches the same verdicts.

### latch — spawn a process, and react to it finishing

```tony
- name: shape-latch
  condition:
    subject: { system: demo, kind: latch, value: { stage: open } }
  action:
    patch:  { stage: reviewing }
    spawn:  { driver: scripted, task: "echo reviewing $subject; sleep 2" }
    onDone: { patch: { stage: reviewed } }
```

`$subject` — and every field of the subject's payload — is bound in the action. `onDone` is a
completion hook: when *that* process reaches `done`, the engine fires the hook about the same
subject, so the process step and its follow-up read top-to-bottom in one rule.
(`onFailed`, `onCancelled` and `onOrphaned` are the other branches.)

The shape that matters here is the **latch**: the action advances `stage` past `open`, so an
entity it has acted on is not selected again. A rule with no latch is considered at every
commit touching what it reads — and a spawning rule that cannot latch is *refused at install*,
because that is an infinite loop with a bill attached.

Two things keep that from being fragile. Writing the same value twice produces no commit, so
a set-to-the-same-value is inert; and a re-firing about a subject that already has work
running is not a second firing.

### ask — a human decides

```tony
  gates:
  - { name: approval, mode: ask }
```

```sh
./scripts/charter-shapes.sh run ask
verse gates
# - gate: approval
#   id: trigger:shape-ask:approval~093e7c94
#   mode: ASK
#   origin: shape-ask
#   reason: "trigger shape-ask gate approval on demo:ask:1"
#   spawns: shape-ask@demo:ask:1
#   subject: demo:ask:1
verse gate approve trigger:shape-ask:approval~093e7c94
```

`gate` is the gate's own name, `origin` the rule that raised it, `subject` what it is about,
and `spawns` what a "yes" will start — settled before the question is asked, even though
nothing is running yet. The id is a function of (trigger, gate, subject), so a rule
re-evaluated at every commit asks **once**: a firing that finds its question already standing
joins it instead of posting a copy. [§5](#5-gates-and-the-docket) is the rest of gates.

### seq — steps in order

```tony
  action:
    seq:
    - spawn: { driver: scripted, task: "echo lint $subject" }
    - spawn:    { driver: scripted, task: "echo test $subject" }
      onFailed: { patch: { stage: broken } }
    onDone:
      patch: { stage: built }
```

`seq` is the depth axis — each element starts when the previous one completes, `onDone` on
the action carrying it means "after the whole sequence", and a per-element `onFailed` is an
off-ramp. There is deliberately no `par`: breadth is what independent rules matching the same
condition already do.

Ask what actually happened, which is where the charter and the runtime are read through one
address:

```sh
./scripts/charter-shapes.sh run seq
verse flow show demo:seq:1
```

```tony
- roots:
  - chain: shape-seq
    children:
    - chain: shape-seq.seq[0]
      id: shape-seq.seq.0@demo:seq:1
      status: done
      task: "echo lint demo:seq:1"
    - chain: shape-seq.seq[1]
      children:
      - chain: shape-seq.seq[1].onFailed
        status: skipped
      status: done
    - chain: shape-seq.onDone
      kind: onDone
      status: applied
  subject: demo:seq:1
```

Each node is one action in the charter, addressed by its `chain` — the position of that
action inside its rule — and that same string is the id the process spawns under
(`shape-seq.seq.0@demo:seq:1`). So the tree is not a second model of the work; it is the
charter and the runtime read together. Statuses that come from no process are the point:
`applied` fired but only set fields, `skipped` is a branch its parent's outcome did not
answer to, `pending` may still fire.

### when — a condition on another entity

```tony
  condition:
    subject: { system: demo, kind: when, value: { stage: ready } }
    when:    { system: ci, kind: status, id: shapes, value: { state: green } }
```

The subject says what to act on; `when` says whether this is a moment to act. *While* CI is
green, every ready entity ships — and one arriving afterwards is picked up, because the rule
is re-evaluated at every commit touching either subtree.

### on — a cause, not a condition

```tony
    on: { system: ci, kind: status, id: release, value: { state: green } }
```

Same two entities, a different sentence: fire **at the moment** CI went green, over whatever
was waiting *then*. `on` is asked at the previous commit and at this one, and fires on the
rising edge:

```sh
./scripts/charter-shapes.sh run on
verse entity get demo:on:1                        # stage: released

verse entity put demo:on:2 '{stage: waiting}'     # CI is still green…
verse entity get demo:on:2                        # …and this one stays `waiting`
```

Two properties, and both are the point of choosing `on` over `when`. **An edge does not
re-fire while it stays true** — the predicate never fell, so `demo:on:2` waits for CI to go
red and green again. And an `on` rule **does not act on the backlog**, for the same reason: a
predicate that already holds never rose.

When you mean "whenever this holds", write `when`. The engine warns at install when a narrow
`on` leaves part of what the rule reads uncovered — which is exactly the case above.

### derive — writing something that is not the subject

Every rule so far acts on its `subject` and nothing else. That is deliberate — it is what
makes a firing checkable — but it means the only way a charter could bring an entity into
being *elsewhere* was to spawn an agent and let it write, which puts the write beyond anything
install-time analysis can see.

`derive` is that write, said in the charter. And because a charter that can write anywhere can
write in a circle, it comes with the thing that bounds it: a **stratum**, a declared level.

```tony
- name: shapes-source
  level: 0
  match: demo
- name: shapes-derived
  level: 10
  match: derived
---
- name: shape-derive
  stratum: shapes-source
  condition:
    subject: { system: demo, kind: derive, value: { stage: open } }
  action:
    patch: { stage: raised }
    derive:
      derived:
        feature:
          priority: urgent
          about: $subject
```

```sh
./scripts/charter-shapes.sh run derive
verse strata                                                  # the ordering
verse entity get derived:feature:shape-derive@demo:derive:1
```

Two rules, checked at install and neither reading the store: a rule's **subject** must be
inside its own stratum, and a **derive** must land in a strictly higher one. A charter file may
hold both strata and rules — `verse trigger add` installs the ordering first.

The derived entity's id is the *engine's*, a function of (rule, subject), so the rule name and
the subject are both in it: no two rules can ever address the same derived entity, and a
re-fire writes the same address rather than a second thing. The engine also records what it is
about, at `verse.meta.derivedFrom`.

Declaring nothing is a complete answer — every other shape here has no `stratum:` and is
checked against nothing, which is exactly how charters behaved before strata existed. The one
thing an unstratified rule may not do is `derive`, because "strictly above" has no answer for a
rule with no level. See [strata](strata.md).

### The rest of the grammar

Rules install and read back in tony, so inspecting one is a round trip rather than a second
format:

```sh
verse trigger add -f charter.tony    # or: cat charter.tony | verse trigger add -f -
verse triggers
verse trigger show shape-latch
verse trigger remove shape-latch
verse strata                         # and `verse stratum show|remove` for one of them
```

A charter is durable — each rule is an entity (`trigger:rule:<name>`), and each stratum is one
too (`trigger:strata:<name>`), so the program a verse comes back up running is the one it went
down with. Every field, both grammars, the answer
contract a spawn can demand of an agent, chains and ids, and what a match can say are in
[the trigger model](trigger-model.md).

---

## 5. Gates and the docket

A gate is the autonomy dial, per action. Three settings answer immediately — `auto` fires,
`ask` asks one person ([§4](#ask--a-human-decides)), `park` holds — and a rule's gates are
conjunctive, so a deploy needing both a security and a release-manager sign-off is two `ask`
gates.

But some questions are not one person's to answer in one click. A gate can instead carry a
**formula**, which the verse resolves itself by putting the open question to the group as a
*motion* and watching ballots accumulate:

```tony
  gates:
  - { name: approvals, formula: !quorum 2 }
```

```sh
./scripts/charter-shapes.sh run vote
verse docket
# 8f306077  whether open  options=[proceed=approve(2)]  subject=demo:vote:1
```

The rule fired; the action is waiting — not on a person, on a tally. Cast ballots, each
naming its voter, since a motion must be settled attributably:

```sh
verse vote 8f306077 approve --as alice    # 1 of 2 — still open
verse vote 8f306077 approve --as bob      # 2 of 2 → carries
verse docket
# 8f306077  whether closed  options=[proceed=approve(2)]  outcome=proceed tally=2  subject=demo:vote:1
verse entity get demo:vote:1              # stage: merged
```

A motion is an ordinary entity (`motion:channel:<id>`), so the docket is a view of the store:
you can watch it, get one, and write a rule that reacts to it. It stays live until it
commits — if alice retracts first, the tally drops and the gate re-blocks.

### "No" is a real outcome

A vote you can only win isn't a vote. `!until` reads as *this, unless that gets there first*:

```tony
formula: !until [ !quorum 2, !quorum { choice: reject, n: 2 } ]   # two rejects → deny
formula: !until [ !quorum 2, !deadline "4h" ]                     # the clock defaults it to deny
formula: !until [ !quorum 2, !fact ci:status:green ]              # an external fact settles it
```

Quote the duration. A tony literal may not start with a digit and commas are optional, so
`!deadline 4h` reads as the number `4` beside a separate literal `h` — well-formed, and not
what you wrote. verse rejects a numeric deadline and says so.

That is the whole formula language: leaves are requirements (`!quorum`, `!fact`,
`!deadline`), and `!until` composes them. Don't confuse it with the `!and`/`!or` inside a
*match*, which compose predicates about the verse right now rather than requirements over
time.

### The same machinery answers *who*

The second question hides in every `spawn`: who does the work. Replace a fixed `driver` with
a `provider` selection and the eligible doers become the options on the same kind of motion,
answered the same three ways (`auto` picks by rule, `ask` lets one person pick, `vote` lets
the group). Doers are ordinary entities, so you add or retire them by writing entities rather
than by editing the charter — and a doer may only ever *narrow* the authority the spawn
granted. That is its own subject: [provider selection](provider-selection.md).

---

## 6. Where to go next

The tutorial stops here because everything below is a subject rather than a step. Each of
these runs today, and each says where its own corners are.

**Choosing who acts** — [provider selection](provider-selection.md). A registry of doers as
entities, selection by capability and a match over attributes, and the narrowing rule that
makes a registry anyone can write safe to select from. *Left to do: richer voting
disciplines — ranked, weighted, secret.*

**Isolation** — [reference/connect.md](reference/connect.md#scopes) and
[scope lifecycle](roadmap/scope-lifecycle.md). Every command takes `--scope`; a scope is a
real copy-on-write overlay, and `verse disconnect --scope` drops the whole thing. *Left to
do: scopes as first-class, listable, durable things.*

**Your working tree, in the verse** — [reference/connect.md](reference/connect.md) and
[content federation](content-federation.md). `verse connect` federates a tree into its own
scope and spawns a validator per file, serving contents through the store so the agent need
not share a filesystem with you. *Left to do: file reads are still truncated.*

**Remote runners, brokers, and human-executed work** —
[process control](process-control.md), [external processes](external-processes.md),
[reference/processes.md](reference/processes.md). *Left to do: the inbox long-poll.*

**Goals** — [reference/goals.md](reference/goals.md). An explicit target over the reactive
substrate: a `doneWhen` formula, a deadline, and a bundle of owned triggers, latching `done`
or `unachievable`. *Being redesigned — read it as what runs today, not as where it lands.*

**Reflecting external systems** — [sources](sources.md). Git, GitHub, Linear into an owned
slice of the verse, on webhook-as-hint plus poll-reconcile. *Designed, not built; the git
Source is next.*

**Ordering what a charter writes** — [strata](strata.md). Declared levels, a `derive` that may
only climb, and the two install checks that follow — plus a plain statement of what it does not
buy, since a charter under strata still does not converge and was never going to. *Left to do:
fencing a spawned agent's writes to its own level and above, which is
[authority](roadmap/authority.md)'s.*

**The store underneath** — [reference/store.md](reference/store.md) and
[change events](change-events.md). Where verse's deeper properties come from: copy-on-write
scopes, CAS writes, edge-faithful watches, and the entity — not the transaction — as the
reactive grain.

**Running it somewhere** — [deployment and steering](deployment-and-steering.md) and the
[Kubernetes deploy](../deploy/README.md).

And the two flat references you will come back to:
[the command surface](reference/README.md) and [the trigger model](trigger-model.md).
