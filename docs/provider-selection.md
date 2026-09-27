# Provider selection

Every `spawn` answers two questions. *What* is the work: the prompt, the frame, the contract.
*Who* does it. This is the second one.

A spawn can answer it outright by naming a `driver`, or it can leave the answer to the verse.
Declare a **provider selection** and the engine picks a doer at fire time from a registry of
entities, through the same question a gate poses. The doer that wins contributes what it
*is* — its driver, its standing instructions, the tools it works with, the authority it runs
under — overlaid onto the spawn that selected it.

For the walk, see [tutorial §5](tutorial.md#the-same-machinery-answers-who). For where `spawn`
sits inside a rule, see [the trigger model](trigger-model.md).

## The registry

A doer is an entity: `provider.<capability>.<id>`. The capability is what it can do; the id is
which one it is.

```sh
verse data provider treat fixer -put '{
  driver: llm, risk: low,
  tools: [read_file, list_dir, write_file, run_bash],
  grant: no-delegate,
  frame: "You apply a confirmed one-line fix, then build and run the named test."
}'
verse provider
# treat      fixer          driver=llm risk=low grant=no-delegate tools=[read_file …]
```

Its payload is one namespace holding two kinds of key, and they are deliberately not separated:

- **attributes** — `risk`, `tier`, `cost`, anything: what a selection *matches on*;
- **fragment fields** — `driver`, `frame`, `tools`, `grant`, `virtualKey`: what the winner
  *contributes*.

`driver` is both, matchable and bound. Splitting the two would make a provider entity a schema
rather than the free-form thing that lets a new attribute be invented without a code change.

## Selecting

```tony
action:
  spawn:
    prompt: "treat $subject"
    grant: no-delegate
    provider: { capability: treat, match: { risk: low }, select: auto }
```

`capability` names the subtree; `match` is a real verse match pattern over each provider's
attributes, so "premium reviewers" or "anything under $5" is just a pattern. The eligible set
is derived at fire time, so doers are added and retired by writing and deleting entities, with
no charter change. An inline `eligible: [gpt, claude]` is the simpler form: a list of names,
where the name is the driver and contributes nothing else, because a name has no attributes.

`select` is the who-dial, and it is the same dial a gate has:

| `select` | who decides | surface |
|---|---|---|
| `auto` (default) | a rule — the first eligible, by id | none |
| `ask` | one party | a question on the docket, every option at one |
| `vote` | the group, at `need` | a question on the docket, every option at `need` |

`within` is the question's deadline on `ask`/`vote`, with no default: no pick by then settles it
on `none` and nothing spawns.

`auto` poses no question at all, which is deliberate rather than an optimisation. The docket is
the answerable, attributable surface. An auto pick has no answerer and casts no ballot, so a
record for it would put a decision on the docket reported as the group's that nobody made. A
gate is the same rule from the other side: a question is posed for `ask`, never for `mode: auto`.

A vote under an `exclusive` resolver is refused at install: an exclusive resolver answers alone,
and a vote is a room. `ask` composes with it.

Nor is an auto pick latched. Re-deriving per firing is deterministic given the same registry, since
the eligible set sorts, and it follows the registry when that changes, where a committed pick would
outlive the doer it named.

## What the winner contributes

The winner is bound as a `launch.Fragment` — `driver`, `frame`, `tools`, `grant`, `virtualKey`,
plus its own id — and the spawn is the base it overlays. That is the shape that matters: the charter's
spawn block is what a doer varies *from*, not a set of fields every doer must satisfy. One
trigger can route to a fixer and a memo-writer that share `driver: llm` and differ in
everything else.

The overlay only ever narrows.

| field | rule |
|---|---|
| `driver` | the doer's replaces |
| `frame` | replaces when set; absent inherits the spawn's |
| `tools` | **intersect** — never replace |
| `grant` | replaces when set and must be within the spawn's; absent inherits the spawn's |
| `virtualKey` | replaces when set; absent inherits the spawn's |
| `prompt`, `vector`, everything else | the charter's, untouched |

Two of those need their reasons stated, because the obvious rule is the wrong one.

**A doer that sets `tools` and not `grant` keeps the spawn's grant.** Absent and empty are
different values on both sides, which is why `task.NamedGrant` returns an `ok` rather than a zero
`Grant`: the zero envelope permits nothing, and would refuse every action of a task that never
mentioned one.

**`tools` intersects because an empty list means *all* of the driver's tools.** A replace would let
a doer offering `[read_file, run_bash]` add `run_bash` to a spawn that offered only `[read_file]`.
"Narrows by construction" is true of a tool list read on its own and false of one overlaid onto
another.

That sentinel has one consequence. Two tool lists that share nothing cannot be composed: the
intersection is the empty set, the empty set is spelled by an empty list, and an empty list means
all of them, so the silent answer to "these two agree on nothing" would be "have everything". Such
a pair is refused instead. It is a contradiction anyway, since a doer that can use no tool the
spawn permits cannot do the work.

## Which credential it runs under

`virtualKey` is the ADDRESS of the model credential the work runs under:

```sh
verse data acme keys review -put '{key: "vk-abc123"}'      # the node IS the credential
verse perms acme keys review --grants '[{read: [group:reviewers]}]'

verse data provider review careful -put '{
  driver: llm, risk: low,
  virtualKey: acme.keys.review,
  frame: "You review the change against the tests it claims to pass."
}'
```

An address rather than a name, because verse already has one namespace and it is the tree. A name
would want a registry beside it — per runtime, agreeing across deployments, carrying no
permissions of its own.

**The read is the authority.** Spending a credential has a cost, so who may spend it is a
permission, and the one verse already has answers it: versed resolves the address **as whoever
named it** before the task starts, so a charter naming a node its own owner may not read is
refused rather than run. There is no group, flag or list invented for keys — put the perms you
want on the node, and where credentials live is your convention, not verse's.

That is also why a provider entity may set this field at all. A provider is an ordinary entity,
writable by anyone who can write entities; the address it carries is not authority, and the read
of it is checked against the namer regardless of who wrote the provider.

**The node holds the key**, so rotating one is a write — `verse data acme keys review -put …` —
and every charter and provider naming that address follows without being touched. Nothing is
declared twice and no config is redeployed.

That means the store holds bearer strings, which is a real cost and was taken deliberately. The
alternative was a reference to a name each runtime allowlists, and it bought less than it looked:
whoever may read a credential can already SPEND it by naming it in a spawn, so the indirection
only moved where the key was typed — at the price of a third name, a list per runtime to keep in
step, and a deploy to rotate. What is left is a smaller difference: a permitted reader can also
copy the key OUT. A Portkey virtual key is bounded by its own budget and route, which is what
makes that payable. **A provider key is not, and does not belong here.**

### What protects a key here, and what does not

**A credential in the verse is protected by permissions and by nothing else.** It is stored as
written — there is no encryption at rest in the store, no envelope, no key management — so the
protection is a convention the permissions model enforces: who may read the node is who may use
the key, and who may read the node is whatever its record says.

That has two edges worth knowing before you put a real key in.

**You must fence the place.** A verse with an open root holds out `read` to everyone, so a
credential written into it is readable by everyone the moment it exists, including by every rule.
Set the place up once and everything created under it is fenced, including nodes that are already
there:

```sh
verse perms acme keys --grants '[]' --child-grants '{}'
```

Both halves are needed. `--child-grants '{}'` alone hands nothing down but leaves the place itself
readable, and a child inherits that.

**A rule that may read a key may also copy it.** Fencing decides *who*; it cannot decide what they
do next. A rule runs as its owner, so a rule owned by somebody who may read a credential can derive
that node's contents anywhere it may write — into an io's place, where a connector will say it out
loud. There is no check against this and there is not going to be one: the same read that lets a
spawn spend the key lets a rule copy it, and refusing the second while allowing the first would be
a distinction the model cannot draw.

So the honest statement of the guarantee: **verse keeps a key from people the record excludes, and
keeps nothing from people the record admits.** That is enough for a Portkey virtual key, which is
bounded by its own budget and route — the worst case is spend, inside a limit somebody set. It is
not enough for a provider key, which is unbounded, and one does not belong here at all.

If you need more than permissions, verse is not where the secret should live: keep it in the
runtime's own environment and let a spawn name no credential at all, or store something verse can
hold safely — an encrypted value your runner can open, a reference into a secret manager — and
accept that rotation then happens wherever that lives rather than in a write.

So two things are separated that one mechanism would have run together: **who may spend** (perms
on the node) and **what it costs** (Portkey, which hangs budget, rate and routing off the virtual
key). The second is why spend gets an owner without verse growing an accounting system, and why
cheap-model triage and expensive review are one line rather than two runner deployments.

## The ceiling

A `provider.*` entity is an ordinary entity. **Anyone who can write one can create a doer** —
so if a doer's `grant` were taken as-is, writing an entity would be a privilege escalation,
and the charter that spawned the task never said so.

The rule is the one delegation already uses (`task.Grant.Subset`, the check `NewChild`
makes at a call_agent boundary): the charter sets a ceiling, a doer may only narrow.

**A spawn that declares `provider:` must declare `grant:`, or it will not install.** An omitted
grant is not a small ceiling but an unbounded one, since `task.Launch` treats a nil grant as
unbounded, so without the requirement "a doer may only narrow" would be a check against everything
in the common case. `grant: all` is spellable for exactly this: say unbounded rather than
defaulting into it.

```
spawn declares `provider:` but no `grant:`: a selected doer may carry its own grant, and
that is only checkable against a ceiling the charter states. Add one (`grant: read-only`,
`no-delegate`, `none`) — or `grant: all` to say the selection is deliberately unbounded
```

### Refused at eligibility, not at the bind

A doer that would widen is not trimmed and not failed at launch. It is **ineligible**: it
never enters the option set, so it cannot be picked and cannot be voted for.

Both halves matter. Trimming, meaning quietly dropping the offending field and running the doer
anyway, would put a task under an envelope different from the one its own entity states, which is
the defect the named-grant vocabulary exists to prevent. Checking after the pick would let a group
deliberate, reach a threshold, and elect a doer that then cannot be seated: a decision with no
seat, which no voter could have known they were making.

An empty option set is explained rather than silent, because the causes are fixed in different
places:

| why nothing is eligible | where the fix is |
|---|---|
| the spawn names no doers and no capability | the charter |
| no `provider.<cap>.*` is registered | register one |
| all registered were excluded by `match` | loosen the match |
| they matched and may not run under this spawn | narrow the doer, **or** widen the spawn |

The last one names both sides, since which is right is a judgement the message cannot make:

```
provider selection has no eligible doer … why="1 matching doer(s) may not run under this
spawn: greedy: its grant \"all\" is not within the spawn's \"no-delegate\" — a doer may
narrow authority, never widen it"
```

A doer whose `grant` names something outside the vocabulary is refused too, rather than
resolving to nothing. A typo must not be a route to running un-enveloped.

### What a doer may not set

`vector`, `resolver` and `environment` on a provider entity make it ineligible — refused,
again, rather than ignored, so a doer's entity and the task it produces cannot disagree.

`vector` is the sharp one, and the reason it is not in the fragment. It is the one axis that widens
without touching authority: `vector: auto` means "this firing already answered its gates", so a doer
that set it would take every action boundary off the docket while its grant looked untouched.
Whether a person sees an action is the charter's call. An ordering over `auto|ask|park` that would
let a doer *tighten* autonomy is a live question. `resolver` and `environment` are excluded too:
a spawn may set either, and a provider's fragment that sets one is refused.

## What you can see

Because a selection changes what a task may do, three surfaces say what it did.

**The task entity** carries `doer`, `grant` and `tools` for every spawn, including `auto`, which
poses no question and therefore has no other surface. `doer` is separate from `driver` because a
driver does not identify a doer: two doers on one driver is the case this feature serves, so a task
showing only `llm` would report quite different work as the same agent.

```sh
verse data verse task instance <id>
# doer: provider.treat.fixer   driver: llm   grant: no-delegate   tools: [read_file, …]
```

**The docket** carries each option's envelope, so what an answer would authorise is beside the
name it is cast for:

```sh
verse gate
```

```tony
- chain: treat.onDone
  gate: provider
  id: verse.trigger.treat.provider~aa92f8cc
  mode: ASK
  options:
  - grant: no-delegate
    id: fixer
    need: 2
    tools: [read_file, list_dir, write_file, run_bash]
  - grant: read-only
    id: memo
    need: 2
    tools: [read_file, list_dir]
  origin: treat
  subject: demo.finding.7
  where: engine
```

```sh
verse gate verse.trigger.treat.provider~aa92f8cc -answer fixer -as alice@example.com     # 1 of 2 for fixer
```

A doer that adds nothing to the spawn writes neither field, so a ballot between doers that vary by
nothing but model reads as a plain list of names. This is not decoration. Once options differ in
what they may *do*, a voter picking between two names is picking between two authorities, and a
listing that showed only names would have the group authorise a capability nobody put in front of
them.

**`verse provider`** lists the registry with every attribute beside the driver, which is where
an operator answers "what would this doer be allowed to do" before voting.

## Across a restart

A `vote` or `ask` selection without `within` is a question with no deadline: the spawn waits
until somebody picks. So it has to survive verse going down, and it is the harder of the two
kinds of question to bring back — it opens from *inside* the action, after that action has
written its latch, so the subject no longer matches its rule and nothing re-fires it.

What makes it resumable is the `chain` its question carries: the address of the action the
answer is holding up (`review`, `review.onDone`, `review.seq[2]`). A start navigates that chain
back into the charter and re-enters the selection, which re-poses the same question over the
ballots already cast and against the deadline already stamped, so the pick made afterwards
decides who runs, exactly as it would have. It is on the docket entry too, because a selection
names a subject and a capability, and neither of those answers "which rule is waiting on this".

Two things the resumed spawn cannot recover, both deliberate. `$references` resolve against the
subject as it stands *now*, a re-read rather than a guess, and the run names no `occurrence`,
because a restart is the world coming back rather than a commit causing something. A selection
whose rule or action has left the charter, or whose subject is gone, is settled `orphaned` with a
`why` instead: a ballot nobody reads is worse than an empty docket. See
[a question outlives the verse](reference/gates.md#a-question-outlives-the-verse-that-asked-it).

## Corners

- **A human cannot be selected.** A brokered doer is not a driver name — the runner is a
  property of the verse, not of a spawn — so "this needs a person" is still a gate or a
  separate rule rather than a selection whose winner happens to be human. This is the largest
  missing piece.
- **`tools` is one flat allow-list.** There is no way to say "whatever this driver normally
  offers, minus `run_bash`", or "this work requires `write_file`, so a doer without it is not
  eligible".
- **`Grant.Subset` is sound but incomplete**: predicate-for-predicate identity, or a wildcard
  parent. That costs nothing, because the grant vocabulary is four names built from the same
  per-verb atoms, so identity finds the real subset relations, and
  `trigger/providerceiling_test.go` pins all sixteen pairs. The incompleteness is also unreachable
  to test, since a doer's grant is a *name* and a structured one cannot be written. Both change
  together.
- **A grant name is resolved twice**: once by the engine to check the ceiling, once by
  `versed.launchOf` to build the launch. Harmless, and worth consolidating.
