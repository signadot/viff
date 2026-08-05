# Provider selection: how a doer is chosen, and what it may bring

Every `spawn` answers two questions. *What* is the work — the prompt, the frame, the contract.
*Who* does it. This is the second one.

A spawn can answer it outright by naming a `driver`. Or it can leave the answer to the verse:
declare a **provider selection**, and the engine picks a doer at fire time from a registry of
entities, through the same votable machinery a gate uses. The doer that wins contributes what
it *is* — its driver, its standing instructions, the tools it works with, the authority it
runs under — overlaid onto the spawn that selected it.

This note is the deep-dive. For the walk, see
[tutorial §5](tutorial.md#the-same-machinery-answers-who); for where `spawn` sits
inside a rule, [the trigger model](trigger-model.md).

## The registry

A doer is an entity: `provider:<capability>:<id>`. The capability is what it can do; the id is
which one it is.

```sh
verse entity put provider:treat:fixer '{
  driver: llm, risk: low,
  tools: [read_file, list_dir, write_file, run_bash],
  grant: no-delegate,
  frame: "You apply a confirmed one-line fix, then build and run the named test."
}'
verse providers
# treat      fixer          driver=llm risk=low grant=no-delegate tools=[read_file …]
```

Its payload is one namespace holding two kinds of key, and they are deliberately not
separated:

- **attributes** — `risk`, `tier`, `cost`, anything: what a selection *matches on*;
- **fragment fields** — `driver`, `frame`, `tools`, `grant`: what the winner *contributes*.

`driver` is both — matchable and bound — and splitting the two would make a provider
entity a schema rather than the free-form thing that lets a new attribute be invented without
a code change.

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

`select` is the who-dial, and it is the same dial a whether-gate has:

| `select` | who decides | surface |
|---|---|---|
| `auto` (default) | a rule — the first eligible, by id | none |
| `ask` | one party | a motion, threshold 1 |
| `vote` | the group, at `need` | a motion |

`within` arms a deadline on `ask`/`vote`: no pick by then closes the motion with no outcome
and nothing spawns.

`auto` raises no motion at all, which is deliberate rather than an optimisation. The docket is
the votable, attributable surface; an auto pick has no voter and casts no ballot, so opening a
channel for it would put `outcome=gpt tally=0` on the docket — a decision reported as the
group's that nobody made. The whether-axis has always worked this way: a channel opens for a
gate carrying a formula, never for `mode: auto`.

Nor is an auto pick latched. Re-deriving per firing is deterministic given the same registry
(the eligible set sorts), and it follows the registry when that changes — where a committed
pick would outlive the doer it named.

## What the winner contributes

The winner is bound as a `launch.Fragment` — `driver`, `frame`, `tools`, `grant`, plus its own
id — and the spawn is the base it overlays. That is the shape that matters: the charter's
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
| `prompt`, `vector`, everything else | the charter's, untouched |

Two of those need their reasons stated, because the obvious rule is the wrong one.

**A doer that sets `tools` and not `grant` keeps the spawn's grant.** Absent and empty are
different values on both sides — which is why `task.NamedGrant` returns an `ok` rather than
a zero `Grant`, since the zero envelope permits nothing and would refuse every action of a
task that never mentioned one.

**`tools` intersects because an empty list means *all* of the driver's tools.** A replace would
let a doer offering `[read_file, run_bash]` add `run_bash` to a spawn that offered only
`[read_file]`. "Narrows by construction" is true of a tool list read on its own and false of
one overlaid onto another.

That sentinel has one consequence worth knowing: two tool lists that share nothing cannot be
composed. The intersection is the empty set, the empty set is spelled by an empty list, and an
empty list means all of them — so the silent answer to "these two agree on nothing" would be
"have everything". Such a pair is refused instead (see below). It is a contradiction anyway: a
doer that can use no tool the spawn permits cannot do the work.

## The ceiling

A `provider:*` entity is an ordinary entity. **Anyone who can write one can create a doer** —
so if a doer's `grant` were taken as-is, writing an entity would be a privilege escalation,
and the charter that spawned the task never said so.

The rule is the one delegation already uses (`task.Grant.Subset`, the check `NewChild`
makes at a call_agent boundary): the charter sets a ceiling, a doer may only narrow.

**A spawn that declares `provider:` must declare `grant:`, or it will not install.** This is
not fussiness. An omitted grant is not a small ceiling but an unbounded one — `task.Launch`
treats a nil grant as unbounded, which is what every task got before a spawn could say
otherwise — so without the requirement, "a doer may only narrow" would be a check against
everything in the common case. `grant: all` is spellable for exactly this: say unbounded
rather than defaulting into it.

```
spawn declares `provider:` but no `grant:`: a selected doer may carry its own grant, and
that is only checkable against a ceiling the charter states. Add one (`grant: read-only`,
`no-delegate`, `none`) — or `grant: all` to say the selection is deliberately unbounded
```

### Refused at eligibility, not at the bind

A doer that would widen is not trimmed and not failed at launch. It is **ineligible**: it
never enters the option set, so it cannot be picked and cannot be voted for.

Both halves of that matter. Trimming — quietly dropping the offending field and running the
doer anyway — would put a task under an envelope different from the one its own entity
states, which is the defect the named-grant vocabulary exists to prevent. And checking after
the pick would let a group deliberate, reach a threshold, and elect a doer that then cannot be
seated: a decision with no seat, which no voter could have known they were making.

An empty option set is explained rather than silent, because the causes are fixed in different
places:

| why nothing is eligible | where the fix is |
|---|---|
| the spawn names no doers and no capability | the charter |
| no `provider:<cap>:*` is registered | register one |
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

`vector` is the sharp one, and the reason it is not in the fragment. It is the one axis that
widens without touching authority: `vector: auto` means "this firing already answered its
gates", so a doer that set it would take every action boundary off the docket while its grant
looked untouched. Whether a person sees an action is the charter's call. An ordering over
`auto|ask|park` that would let a doer *tighten* autonomy is a live question, not a closed one.
`resolver` and `environment` are excluded for a duller reason: neither has semantics to
resolve to yet, so honoring them would be a promise nothing keeps.

## What you can see

Because a selection changes what a task may do, three surfaces say what it did.

**The task entity** carries `doer`, `grant` and `tools` for every spawn — including `auto`,
which raises no motion and therefore has no other surface at all. `doer` is separate from
`driver` because the driver stopped identifying the doer: two doers on one driver is the case
this whole feature serves, so a task showing only `llm` would report quite different work
as the same agent.

```sh
verse entity get task:instance:<id>
# doer: provider:treat:fixer   driver: llm   grant: no-delegate   tools: [read_file, …]
```

**The docket** shows each option's envelope where the options differ in one:

```sh
verse docket
# … select open  options=[fixer(2)[no-delegate +read_file,list_dir,write_file,run_bash]
#                         memo(2)[read-only +read_file,list_dir]] …
```

Only what differs from a plain name is added, so a ballot between doers that vary by nothing
but model reads exactly as it did before. This is not decoration: once options differ in what
they may *do*, a voter picking between two names is picking between two authorities, and a
listing that showed only names would have the group authorise a capability nobody put in front
of them.

**`verse providers`** lists the registry with every attribute beside the driver, which is where
an operator answers "what would this doer be allowed to do" before voting.

## Corners

- **A human cannot be selected.** A brokered doer is not a driver name — the runner is a
  property of the verse, not of a spawn — so "this needs a person" is still a gate or a
  separate rule rather than a selection whose winner happens to be human. This is the largest
  missing piece; see [provider selection improvements](working/provider-selection-improvements.md).
- **`tools` is one flat allow-list.** There is no way to say "whatever this driver normally
  offers, minus `run_bash`", or "this work requires `write_file`, so a doer without it is not
  eligible". Same note.
- **`Grant.Subset` is sound but incomplete** — predicate-for-predicate identity, or a wildcard
  parent. Today that costs nothing, because the grant vocabulary is four names built from the
  same per-verb atoms, so identity finds the real subset relations; `trigger/providerceiling_test.go`
  pins all sixteen pairs. It is also unreachable to test the incompleteness, since a doer's
  grant is a *name* and a structured one cannot be written. Both change together.
- **A grant name is resolved twice** — once by the engine to check the ceiling, once by
  `versed.launchOf` to build the launch. Harmless, and worth consolidating.
