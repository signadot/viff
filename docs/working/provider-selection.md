# A provider is a doer, not a driver name

Status: **built, except the human row.** A governed provider selection binds a *launch-spec
fragment* rather than a single `driver` string; a doer may only narrow what the spawn granted;
the resulting envelope is visible on the process entity and on the docket. Selecting a human
is not done — see "The human row is separate work" at the end.

The argument is kept because the argument is what the rules mean. Two things changed on
contact with the code, and are marked in place: the ceiling needed an install-time requirement
this draft did not have, and `Grant.Subset`'s incompleteness turned out to cost nothing yet.

Background if these words are new: a **charter** is a set of triggers; a **trigger** fires an
**action** when the verse changes; an action may **spawn** a process, described by a
`launch.Spec`. See [the trigger model](../trigger-model.md) and
[tutorial §6](../tutorial.md#6-gates-the-group-decides--whether-and-who) for the selection machinery as it exists.

## What a provider selection does today

A spawn normally names its doer outright — `driver: llm`. A spawn may instead declare a
*governed selection*: the eligible doers, and how one of them is picked.

```tony
action:
  spawn:
    task: "review $subject"
    provider: { capability: review, match: { tier: premium }, select: vote, need: 2 }
```

Eligibility is a registry lookup. `Engine.eligibleProviders` (`trigger/engine.go:942`) lists
the `provider:<capability>:*` entities, filters them by a tony match over their attributes,
and sorts them. `select` then decides how one is picked: `auto` takes the first, `ask` and
`vote` reify a motion on the docket and resolve it through the same machinery a whether-gate
uses. `Engine.selectProvider` (`trigger/engine.go:868`) returns the winner.

What the winner contributes is one field:

```go
// trigger/trigger.go:390
if spec.Provider != nil {
    driver, ok := e.selectProvider(spec.Provider, subj)
    if !ok {
        return nil, nil // no eligible doer / selection did not resolve — do not spawn
    }
    spec.Driver = driver
}
```

`driver` is read off the winning entity's payload; everything else about the spawn comes from
the charter. So the registry is attribute-rich for *selection* (`tier`, `cost`, a specialty)
and one-field-deep for *effect*.

## The gap

That was proportionate when a spawn was `(id, task, driver)`: three values, so three values
were all a charter could influence, and the only way two doers could differ was which runtime
ran the prompt.

`c6746a3` changed the shape. A `launch.Spec` now carries `frame` (the system prompt),
`tools` (which tools are offered at all), `vector` (the autonomy dial), and `grant` (the hard
authority envelope) — what the agent is told, what it may attempt, what it must confirm, what
it may do at all. Selection predates that field set and never caught up.

So today two doers can differ **by model** and cannot differ **by capability or by
instruction**, which is now most of what distinguishes one doer from another.

## The case that exposes it

A friction-triage charter classifies each finding and routes it. The routes want to be:

| doer        | driver | tools                        | grant                | frame                                        |
|-------------|--------|------------------------------|----------------------|----------------------------------------------|
| fixer       | llm    | read, list, write, run_bash  | bounded, in a worktree | "apply the change, build, run the named test" |
| memo-writer | llm    | read, list                   | read-only            | "put the decision in front of a person"       |
| a human     | broker | —                            | —                    | —                                             |

Two of the three are the same driver. The entire difference between them lives in fields a
provider entity cannot hold. `provider:treat:fixer {driver: llm, tier: premium}` says nothing
about being allowed to write a file, and the charter's own spawn block cannot say it either,
because that one block has to serve whichever doer wins.

So the charter hand-rolls the selection as six near-identical `route-*` triggers, one per
class, each with its own frozen `frame`/`tools`/`grant`. That is the registry written out
longhand, unable to change without a charter edit — the exact thing the registry exists to
replace ([tutorial §6](../tutorial.md#6-gates-the-group-decides--whether-and-who): "you add or retire doers by writing or
deleting entities, with no charter change").

The third row is why this matters beyond tidiness. A human *is* a provider — the [dial-in
broker](../tutorial.md#5-human-executed-processes-dial-in) already runs a process a person executes. "This finding needs a person" should not be a
gate or a separate rule; it should be a selection whose winner happens to be human, over the
same votable, attributable surface as every other who-question. That is unreachable while a
provider is a driver name.

## The proposal

A provider entity carries a launch-spec fragment. The selection binds that fragment alongside
`Driver`: the charter's own spawn fields as the base, the winner's fields overlaid.

```sh
verse entity put provider:treat:fixer '{
  driver: llm, applies: true, risk: low,
  tools: [read_file, list_dir, write_file, run_bash],
  grant: no-delegate,
  frame: "You apply a confirmed one-line fix in a worktree, then build and run the named test."
}'
```

```tony
action:
  spawn:
    task: "…"
    grant: no-delegate                     # the CEILING — see below
    provider: { capability: treat, match: { risk: low }, select: auto }
```

Classification then emits the *attributes* a treatment is selected by — confirmed, one file, a
named test, low risk — instead of a class that names a trigger, and a new treatment is an
entity rather than a charter edit.

## The authority problem

`provider:*` entities are ordinary entities. Anyone who can write one can create a doer. If a
selected provider's fields are taken as-is, writing an entity becomes a privilege escalation,
and the charter that spawned the process never said so. This is the part to settle first.

### Three axes can widen, not one

The obvious rule is the one delegation already uses: the winner's grant must be a
`Grant.Subset` of the grant the spawn declared (`process/action.go:56`, the check `NewChild`
makes at `process/child.go:77`). The charter sets a ceiling; a provider may only narrow.

That is right, and insufficient, because `grant` is not the only field that can widen.

- **`tools` does not narrow by construction.** Empty means *all* of the driver's tools
  (`launch/launch.go:66`, `process/runner.go:32`). A spawn saying `tools: [read_file]` and a
  provider saying `tools: [read_file, run_bash]` widens. The overlay for `tools` has to be
  **intersection**, not replacement.
- **`vector` can remove the human without touching authority.** `NamedVector` yields
  `{Default: auto|ask|park}`. A provider writing `vector: auto` under a spawn's `vector: ask`
  takes every action boundary off the docket. `process/authority.go:46` states what that means
  — `vector: auto` says "this firing already answered its gates" — and that is a claim only the
  charter is positioned to make. Either **forbid `vector` on a provider entity**, or order the
  modes and require non-loosening. Forbidding is the smaller first commit.
- **`frame` is advice**, and needs no envelope check: a model may ignore every word of it. It
  is worth noting what the safety argument rests on, though. `launch/launch.go:145` already
  declined to `$var`-expand `tools`, because "letting payload text pick which tools an agent is
  handed is the widening-by-binding risk". A provider's `frame` is entity-authored text
  becoming a system prompt — the same shape one level up. It is fine *because* grant and tools
  hold, which is what the two points above are about.
- **`resolver` and `environment`** are declared on the spec and read by nothing today
  (`versed/server.go:241`). Neither should be settable by a provider until it has semantics;
  `environment` is the sandbox floor and will want the same ceiling rule when it lands.

### The ceiling is ⊤ unless the spawn says otherwise

`NamedGrant("")` returns `ok=false` — deliberately, so "declared no envelope" and "declared the
empty envelope" stay distinguishable (`process/authority.go:79`). `launchOf` then leaves
`Launch.Grant` nil, and nil means unbounded: "nil = unbounded, which is what every process got
before a spawn could say otherwise" (`process/runner.go:41`).

So for a spawn that writes no `grant:` — most spawns — the ceiling is everything, and "a
provider may only narrow" narrows from ⊤. That is not *unsound*, since the process would have
been unbounded without the provider too, but the check buys nothing in the common case.

**A spawn carrying `provider:` should be required to declare a grant**, or an absent ceiling
should resolve to something other than ⊤. Cheap, and it belongs in the same commit as the
subset check — otherwise the check reads as protection it is not providing.

### `Subset` is sound but incomplete, and today that is survivable

`Grant.Subset` accepts only a wildcard parent or predicate-for-predicate `DeepEqual` identity
(`process/action.go:52`). It is deliberately sound-and-incomplete; true predicate subsumption
is open.

Within the current four-name vocabulary this happens to be fine: `namedGrants` builds
`read-only` and `no-delegate` out of the same `{Verbs:[Read]}` atoms, so `read-only ⊆
no-delegate` passes by identity, and `none ⊆ anything` passes trivially.

It breaks the moment a provider wants a finer envelope than a name — `{Verbs:[Write],
Paths:[…]}` under a `{Verbs:[Write]}` ceiling is genuinely narrower and gets refused.

**Corrected on contact with the code.** That day is further off than this draft assumed,
because a provider's `grant` is a NAME from `namedGrants` and there is no way to write a
structured one. So there is no reachable case where the incompleteness bites, and the fixer
row's "bounded, in a git worktree" is out of reach for a different reason than the one stated
here: not that `Subset` would refuse it, but that the vocabulary cannot spell it. The
sixteen-cell ceiling matrix in `trigger/providerceiling_test.go` pins every pair the
vocabulary can express, so whichever lands first — a structured grant, or subsumption — what
changes is visible.

### The check belongs in eligibility, not in the bind

The natural place to put a ceiling check is where the fragment is overlaid, after the winner is
known. That is wrong for the vote path.

`selectProvider` can open a motion, run it to threshold, and commit (`trigger/engine.go:900`).
If the ceiling is checked afterwards, a group can elect a doer that then fails the check and
never gets seated — a decision with no seat, and no way for a voter to have known.

**Filter non-conforming providers out of `eligibleProviders`.** Then the ballot only ever
contains seatable candidates, and the explanation lands in `whyNoEligible`
(`trigger/engine.go:982`), which exists precisely to explain an empty option set in terms of
what to go fix.

The cost is that the engine must resolve grant names at *selection* time, whereas resolution
lives today in `versed.launchOf` (`versed/server.go:245`). `process.NamedGrant` is importable
from `trigger`, so this is a question of where the vocabulary should sit, not whether it can.

### Overlay or replace, per field

Stated explicitly because "absent" and "empty" differ on both sides:

| field | rule |
|---|---|
| `driver` | provider replaces (today's behaviour) |
| `frame`  | provider replaces if set; absent inherits the spawn's |
| `tools`  | **intersect**. Spawn-empty = all, so provider-set narrows; provider-empty = do not narrow further |
| `grant`  | provider replaces if set, and must be ⊆ the spawn's; absent inherits the spawn's |
| `vector` | not settable by a provider (first commit) |
| `resolver`, `environment` | not settable — no semantics yet |

A provider that sets `tools` and not `grant` inherits the spawn's grant rather than losing it.

## What the selection has to show

`verse docket` renders options as bare names — `options=[gpt(2) claude(2)]`
(`cmd/verse/vote.go:131`). If the options now differ in what they may *do*, a voter picking
between them is choosing an envelope, and the docket has to say so; otherwise the group votes
on a name and authorises a capability.

The `auto` path is worse than the vote path, and it is the one the fixer example uses.
`select: auto` opens no channel at all, deliberately (`trigger/engine.go:882`) — an auto pick
has no voter and casts no ballot, so putting it on the docket would report a decision as the
group's that nobody made. That reasoning still holds. But it means an auto-selected envelope
appears on no docket, and `mirror.Meta` carries `Subject`/`Runner`/`Chain`/`Task`
(`mirror/mirror.go:35`) — not the envelope.

**Reflect the resolved envelope onto the process entity, regardless of select mode.** It is
provenance of a spawn rather than a decision anyone took, which is exactly where the existing
note says such things belong. The docket rendering is then the second, easier half.

## The human row is separate work

Selecting a human is not a driver name. The runner is a property of the verse, not of a spawn:
`Server.runner` is a single field set once (`versed/server.go:44`), `Runner.Spawn(Launch,
ContentReader)` takes no runner selector, and the runner reaches the process entity only via
`mirror.Meta.Runner`. So a brokered doer needs either a runner axis on the launch spec or a
driver that resolves to a brokered handle.

That is a plumbing change in the Server and the Spawner. The launch-fragment overlay, by
contrast, passes through seams that already exist — `eligibleProviders` already reads a
payload, `launchOf` already resolves names. The two are separable, and the overlay should ship
first, so the runner axis has a working selection to attach to instead of both moving at once.

## Order of work

1. **Done.** Provider entity carries `frame`/`tools`/`grant`; `selectProvider` returns a
   `launch.Fragment`; `applyOne` overlays it per the table above.
2. **Done.** Ceiling: a spawn with `provider:` must declare a grant (refused at install);
   `providerCandidates` marks each doer eligible or refused; `whyNoEligible` explains, naming
   both sides.
3. **Done.** Envelope on the process entity for every select mode (`mirror.Meta`), per-option
   on the docket for `ask`/`vote` (`optionLabels`), and a `doer` field so a process says which
   doer it got when two share a driver.
4. **Open.** Runner axis, and the human row.

Open past that: predicate subsumption in `Grant.Subset` and the structured grant that would
need it (neither is reachable today — see that section), and whether `vector` ever becomes
provider-settable under an ordering over `auto|ask|park`.
