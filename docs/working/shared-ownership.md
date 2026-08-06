# Shared ownership: an entity with more than one writer

Status: proposal, nothing here runs. The order of work is
[shared-ownership-plan.md](shared-ownership-plan.md). It supersedes the count-based sketch in
[sources-residual.md](sources-residual.md#the-way-through-is-the-conditional-write), which is
wrong about one thing — see [what is already true](#what-is-already-true-and-what-was-not).
Everything in that section was run against the tree at `go-tony v0.0.116`; the transcript is
[at the end](#the-probe).

## The problem, which has now arrived twice

`owns` is an **address**: `linear:issue:ENG-*`, compared statically at install because two
declarations are all the check has in hand. It works whenever a system's ids encode ownership
positionally, which is most of the time and is not a law.

- A **Linear project** belongs to several teams at once, so no team can claim it as an
  address, and two sources over two teams would each tombstone what the other reflected. The
  source reflects no project entity at all.
- A **GitHub project** is org- or user-owned and spans repositories: the same shape, in a
  system that shares no code or data model with Linear ([gh-source.md](gh-source.md)).
- A **GitHub user** is neither. It has exactly one owner and that owner is *the system* —
  `github:user:octocat` belongs to GitHub, not to any repository — so every per-repo source
  would write it. Slack's users looked identical and were not, because a bot token is issued
  for one workspace and `slack:user:<ws>/<U…>` therefore has a positional owner.

The residual asked for a second data point before concluding anything. There are now three
cases and two distinct shapes: **many-to-many**, and **one owner who is nobody's source**.
Both are the same failure of the address grammar, and both are fixed by the same move.

## The move

> **Sharing an entity is not an exception to single-writership. It is single-writership at a
> deeper address.**

Two sources do not share `linear:project:p1`. One is the single writer of
`linear:project:p1` **at** `owners.ENG`, the other at `owners.OPS`, and nothing anywhere is
written by two things. The store has addressed writes at that depth since `entity.Path`
existed; what has not caught up is the *claim*, which can only name a ref.

So: **a claim is a `Path`, not a `Ref`.**

```tony
owns:
- linear:team:ENG
- linear:issue:ENG-*
- linear:cycle:ENG/*
- linear:project:*@owners.ENG      # every project, at my slot, and nowhere else
```

Disjointness gains a fourth segment and no new decision procedure. Two claims are disjoint
when **any** segment is — system, kind, id, **or path** — where two paths are disjoint when
neither is a prefix of the other, compared **segment-wise**:

| a | b | |
|---|---|---|
| `…@owners.ENG` | `…@owners.OPS` | disjoint — the second segment differs |
| `…@owners` | `…@owners.ENG` | **not** disjoint — the container holds the slot |
| `…` (no path) | `…@owners.ENG` | **not** disjoint — the root holds everything |
| `…@owners.ENG` | `…@owners.ENGINE` | disjoint — segment-wise, not string-wise |

The last row is the same care the `#` in `github:issue:<org>/<repo>#*` and the `-` in
`linear:issue:ENG-*` take, and it comes free here rather than needing a separator, because a
path is already a list of segments before it is a string.

~~**`@` rather than a dot**, though `Path.String()` renders `demo:pr:1.labels.urgent`. That
rendering is ambiguous the moment an id contains a dot (`git:ref:verse/main.go`), and nothing
parses it back today — the CLI takes the field path as a separate `--at`, split on `.`
(`fieldPath`, `cmd/verse/client.go`). A claim is parsed, so it needs a separator an id cannot
contain.~~ **Also wrong: `@` is taken.** It is `trigger.ChainSep` and appears inside ids today
(`resolvers:instance:<name>@<origin>`, a derived entity's `<rule>@<subject>`), so no split rule
recovers a path from `verse:derived:rule@subject`. A claim carrying a path is therefore a
**document** — `{ref: "linear:project:*", at: [owners, ENG]}` — with the string form unchanged
for the ref-only case, which is additive and needs no separator at all. See
[phase 1's plan](gh-source/1-path-claim-plan.md#two-things-the-design-got-wrong-found-while-planning-this),
which also finds that a cover cannot spell its path `at`, because `entity.Pattern` already uses
that name for something else.

## What is already true, and what was not

Four things this rests on. Three were already claimed by the design; the fourth contradicts it.

**A write merges at its path, and disjoint paths do not race.** Two sources writing
`owners.ENG` and `owners.OPS` need no precondition at all — not a weakened CAS, none. This is
`TestWriteMerges`' rule (`entity/write.go`: "a write says what should be true at one place; it
does not say what should no longer be true elsewhere"), and it holds through logd.

**A precondition may be about another path.** `Require.At` is a `Path`, folded into one root
match so the check and the write commit together (`rootPattern`, `entity/logdstore.go`). So
"delete this entity iff I am the last owner in it" is one store operation.

**"No key other than mine" IS expressible**, and the residual says it is not:

> the match grammar can say "OPS is absent" (`!not {OPS: null}`) but not "no key other than
> mine", so either the condition names a known set — which no source has — or the entity
> carries a **count** and the delete is `Value: nil, When: [{At: …owners.n, Pattern: 0}]`

There is no need for a count. The spelling is:

```tony
!all.or [!field "ENG", !irtype null]
```

`!all` requires every value of an object to match; `!field` matches a value's **own key**
against its child. So this reads "every key here is either mine, or holds a tombstone." It is
true of `{ENG: …}` and of `{}`, false of `{ENG: …, OPS: …}` and of `{OPS: …}` — which is
exactly the last-owner-out question, with none of the machinery the count needs: no field to
maintain, no decrement to get right, no way for the count to disagree with the keys.

Dropping the count matters more than it saves, because a count is a **second representation of
who is here**, kept in step by convention across processes that cannot see each other. That is
the class of bug this design spends its whole budget avoiding.

### The trap in it, which cost most of the investigation

The obvious spelling is `!all.field "ENG"`, and it is right about the payload and wrong about
the store. **logd tombstones a removed key with a null leaf and keeps the key** — the store's
own `absentPath` says so, and it is what makes a delete replayable. So after OPS leaves, the
document still has an `OPS` key holding null, and `!all.field "ENG"` is false forever: the
delete is refused every time, by a source that is genuinely the last owner.

It passes a unit test over a hand-built payload and fails against a real store, which is the
worst available failure mode for something whose job is to decide a deletion. The `!irtype
null` arm is what tolerates the tombstone, and none of this is visible from the design.

Two smaller ones found on the way, worth writing down because both fail *quietly*:

- **Tags compose with dots, not juxtaposition.** `!all !field "ENG"` is not `!all.field "ENG"`.
  In a nested position it parses and evaluates to something else (false, always); at the top
  of a document it is a parse error. The composed forms are `!not.all.field "ENG"` (there is a
  key that is not mine) versus `!all.field !not "ENG"` (every key is not mine) — different
  questions, one character apart.
- **`{x: null}` is a presence test**, so `!irtype null` is how "present and null-valued" is
  said. Verse's own `TestNullPresenceAbsenceIdioms` pins the three spellings; this is the first
  caller that needs the third.

## What converge has to do

The connector side of this is nothing — a source answers with entities and covers as it always
did. Everything below is verse's, which is the point: **a connector author must not be the one
who gets slot semantics right.**

1. **A cover needs a path.** Today a cover says "this answer is complete for these entities";
   a slot owner's answer is complete for *part* of them. A cover at a path tombstones the
   value at that path, not the entity. `entity.Pattern` already has an `At`, and it means
   something else — where to *root the predicate* — so this is a new field rather than a reuse,
   and it is a change to the wire contract a source serves (`covers` in
   [reference/sources.md](../reference/sources.md)): `contract` tier, with a `Breaks:` line.
2. **Replace becomes replace-at-path.** `entity.ReplaceFrom` spells the removals that make a
   write a replace; a slot owner spells them within its slot and nowhere else.
3. **Skip-unchanged must compare the slot**, not the payload — though it is the *replace* above
   that carries the danger, not this. A whole-payload comparison over a correct slot write only
   re-writes a value already standing, which produces no commit; what it costs is a round trip
   per pass and a `lastReflect` stamped every pass, since `Wrote` counts writes issued rather
   than commits produced ([phase 1's plan](gh-source/1-path-claim-plan.md#why-the-replace-is-the-dangerous-half)
   has both failures separated, and why the second is the one that would be invisible).
4. **The tombstone is the only contended write**, and is the conditional delete above.
5. **`--forget` removes the slot, then tries the delete.** A removal may only tombstone what
   the install proved this source owned, which is now a path; the entity goes only if that
   leaves nobody.

## Where a custom mergeop earns its keep

Everything so far is an address, and needs no new tony. The remaining case is the one the
residual named as what would *push this to predicates*:

> a system whose entities are one-owner-shaped but whose ids do not say so — an owner in the
> payload and a uuid in the ref

`github:project:*` is that: the owning org is a field, the id is a number GitHub minted. There
is no path to claim, because the owner is not a place in the payload — it is a *value* in it.

**Arbitrary matches cannot be compared, and the reason is not conservatism.** The residual's
example is the sharp one: a null in a match is a presence test, so the obvious "same field,
different constants" rule proves `{repo: null}` disjoint from `{repo: verse}` when the second
*entails* the first. `!not` inverts which side of the comparison is safe, `!or` makes a claim a
union, and two `!glob`s are disjoint by automata intersection. Worse, the mistakes are not
symmetric: a wrong refusal is an operator's afternoon, and a wrong *acceptance* is two writers
on one slice, silently, forever.

So the claim grammar must be a **whitelist of forms whose pairwise disjointness verse can
decide with a total function**, and everything else refused. And a whitelist that must also
*work as a matcher* is precisely a registered tag:

```tony
owns:
- {system: github, kind: project, by: !by(org) "signadot"}
```

`!by(<kpath>) <literal>` — the field path as the argument, a literal scalar as the child.
Nothing else: no negation, no disjunction, no glob, no null. **The restriction is structural
rather than reviewed**: a presence test is not expressible in it, so the rule that is unsound
for matches in general —

> same path, different literals ⇒ disjoint

— is sound for this tag, because the only thing a `!by` can say is "this field equals this
value". Two `!by`s at different paths are *not* provably disjoint and are refused, which is the
conservative direction.

Three reasons this wants to be a registered mergeop rather than a struct verse parses itself:

- **One document, two uses.** The same claim is compared *statically* at install and
  *evaluated* at converge as a matcher against a payload in hand ([below](#the-price-a-predicate-carries)). A struct
  needs two implementations of one meaning, kept in step by hand; a tag is one implementation
  that both callers use.
- **A whitelist beats a blacklist as tony grows.** Refusing `!not`, `!or`, `!glob`… in a claim
  is a list that must be maintained against a format that adds tags. Admitting only `!by` is a
  list of one that cannot rot.
- **It is available today, with no upstream change.** `mergeop.Register(Symbol) error` is
  exported, and `Symbol`/`Op` are implementable from outside the package — every method and
  parameter type on both is exported. `!by`'s `Match` is a dozen lines over the machinery
  `!at` already has (`doc.ListKPath`, then compare); its `Patch` returns an error, as a
  match-only op's does.

The stock alternative is `!at(org) "signadot"`, which matches identically and needs nothing
new. It is rejected for the whitelist reason alone: `!at` is a general operator that composes
with everything (`!not.at(a.b) 3` and `!at(a.b) !not 3` are both legal and mean different
things), so admitting it into a claim admits the whole grammar through the side door.

### The price a predicate carries, and an address does not

**An address claim is proved once, at install. A payload claim must be checked on every
write.** An entity's org field can *change*, moving it between owners while both sources are
running — which no install-time proof can prevent, because it is a fact about data and the
proof was about declarations.

So converge gains one check for path-less predicate claims: an entity a source's own claim does
not match is **refused, not written**, and reported the way a failed reflect is. It costs one
match per entity per pass against a payload already in hand — cheap, and it is what keeps the
single-writer property true rather than merely declared.

That asymmetry is the honest summary of the whole design: an address is checkable in advance,
which is why it was the right default; a predicate is checkable only in arrears, which is why
it is the exception and carries a runtime cost the default does not.

## What this is not for, and the thing that bounds it

[`scripts/lineage.sh`](../../scripts/lineage.sh) answers a neighbouring question and answers it
differently, which is what makes it the boundary marker for this design rather than a competitor
to it. Its subject is how one piece of work keeps its identity across three systems, and the
answer is that **no fourth id is minted**: a correlation is a *derived* entity whose address is
`<system>:<kind>:<rule>@<subject>`, so identity is (rule, subject).

That address is **single-writer by construction**. One rule, one subject, one writer, no claim
to prove and no slot to divide — re-firing about the same PR rewrites one address rather than
accumulating a second. So:

> **The many-to-many problem exists only for entities verse REFLECTS.** Anything verse derives
> escapes it, because verse chose the address.

Which is the useful bound, and it narrows this design rather than weakening it. Two sources end
up on one entity only because an external system imposed an identity on us that does not say who
owns it. Everything verse names for itself is already safe, and a slot is not the general
mechanism for "several things want to say something about one entity" — the derived entity is,
and it is better at it.

**One consequence to say out loud**, because it changes a sentence that has been true since
sources landed. Today "a source is the single writer of its slice" and "a reflected entity is
exactly what its source says" are the same statement, which is why converge replaces a whole
payload. With slots the second one goes: an entity is the union of what several writers say, and
**nothing owns the whole of it**. A reader that assumed the payload was one source's answer is
now wrong, and — since converge only touches what it covers — a field written onto a shared
entity by anything else survives, where on an owned entity it is still replaced away on the next
pass. That asymmetry is not obviously wrong, but it is new, and it is the corner where the
discipline lineage.sh demonstrates would erode first.

**And the mechanism is more general than the claim is.** Writing your own path, and deleting
under a precondition when you are the last one out, is available to *any* writer today —
`entity.Write` has taken a `Path` all along. What is source-specific is only the ownership claim
and converge. lineage.sh's own first edge is a case that wants the mechanism without the claim:
a release cannot ask for "the traces in THIS release", because membership is a fact nobody
writes and the trace's address belongs to the deriving rule. A second writer recording its own
slot on somebody else's entity is exactly the shape that gap has — which is worth knowing before
anyone concludes membership needs new grammar.

## The engine side: already correct, and one thing missing

A slot write is a write to a strict subtree of a subject, and the question is what the trigger
engine does with it. **Nothing has to change for a charter to be correct**, and that is not
luck: [change-events.md](../change-events.md) made this decision for exactly this case, before
there was a caller for it.

**A deep write is one event, on the entity, carrying the whole entity.** `walkEntities`
descends exactly three object levels — system, kind, id — and stops, so a patch at
`linear.project.p1.owners.ENG` yields one change on `linear:project:p1`; and `To` is levelled
from the applied document rather than being the delta. The reasoning is already written down:

> The moment a write can address a field (`demo:pr:1.stage`), the delta stops being the entity,
> and a consumer matching on `To` is matching a fragment.

with the measurement beside it — a rule whose `when` reads two fields fires on a whole-entity
write to the matching state and stays **silent** on the deep write that reaches the identical
state, if `To` is the delta. So a charter about a project sees the project, whichever slot moved,
and none of it knows slots exist.

**What does change is the wake rate.** A rule that reads a shared entity is evaluated on every
owner's write, so N owners is N times the evaluations — and `Pattern.At` does not narrow it.
`At` moves where the *predicate* is evaluated, not what changed: since `To` is the whole levelled
entity, `at: [owners, ENG]` matches whenever ENG's slot matches, no matter who committed.

Three existing mechanisms absorb that, and none of their arguments depends on how many writers
an entity has — which is the thing worth checking, and it holds:

- **A converging write ends itself.** Rewriting the same value produces no commit, so it raises
  no next evaluation ([cycle-throttle.md](../cycle-throttle.md)).
- **`claimFlight(rule, subject)`** drops a wake for a firing already in flight on that subject.
- **`checkLatch`** already refuses, at install, a spawning action whose subject it cannot prove
  leaves the selection — the case where repeated wakes would actually multiply work.
- The **cycle throttle**'s bound is pigeonhole over the installed triggers and assumes nothing
  about what a firing writes, so more writers cannot weaken it.

The one that is *not* absorbed is converge's own: [skip-unchanged must compare the
slot](#what-converge-has-to-do). Comparing whole payloads makes two sources rewrite each other
forever — a genuine runaway, which the throttle catches after the fact rather than preventing.

### What is missing: "changed under this path"

Before slots, "the entity changed" and "its one writer wrote" were the same statement. Slots
break that equivalence, and nothing in the grammar replaces it: `Pattern.Op` is `set`/`delete`,
`WatchSubtree` is `(system, kind)` with no id and no path, and `At` is about the predicate. A
rule cannot say *fire when ENG's slot changed* — only *fire when this entity changed and ENG's
slot looks like this*.

**The store already computes what that would need, and discards it.** In `eventsFromDelta`:

```go
to := ch.payload                                        // the delta fragment: {owners: {ENG: …}}
if level := payloadIn(doc, s.root, ch.ref); level != nil {
    to = level                                          // …overwritten, one line later
}
```

`ch.payload` is the patch verbatim for that entity — precisely the set of paths this commit
touched. Carrying it alongside `To` rather than instead of it costs no extra walk and no extra
read, and it splits the same way the design already splits `Op` from `To`: **the delta says
when, the level says what.** A trigger could then filter on "the commit touched
`owners.ENG`" while its predicate still reads the whole entity.

Two things to know before building it. On a **scoped** watch the delta is a computed diff
between scope states whose first delta is a `!replace`, so the fragment there is over-broad —
it would fire more, which is the safe direction. And change-events.md's own residual already
says the changed-*ref* set comes from walking the delta and that this is sound for a baseline
delta; this is the same residual one level deeper, so the two want settling together.

It is also charter grammar, which is the largest contract surface there is, so it is not a
free addition — and it is not needed for anything here to work. It is what a shared entity
would want *after* somebody has one and finds the wakes annoying.

## What this does not fix

- **Every slot write is a change on the shared entity** — see [the engine
  side](#the-engine-side-already-correct-and-one-thing-missing), which is a longer answer than
  this bullet had.
- **A tombstoned key is never reclaimed.** `owners` accumulates a null leaf per owner that has
  ever left. Harmless for correctness — the delete condition tolerates them by construction —
  and it means a long-lived shared entity carries a growing husk of departed owners.
- **Two installs racing.** Both prove disjointness against what is standing, and neither sees
  the other. That gap exists today for address claims and this does not widen it, but a
  design that makes shared entities ordinary makes concurrent installs more likely.
- **`--allow-overlap` means less than it did.** Its warning — two writers on one slice, each
  tombstoning what the other reflected — was the *only* way to have two writers. Now there is a
  safe way, and the flag needs to say which one it is not.

## Open questions

- **Does anything actually want a `!by` claim?** GitHub projects are the only candidate in
  sight, and they can be had by [layer one](#the-move) alone if the source keys the slot by
  repo. If nothing needs it, the mergeop is a mechanism looking for a use and the path claim is
  the whole proposal.
- **Should the tag be namespaced?** `mergeop`'s registry is process-global and `Register`
  errors on a duplicate, so a `!by` shipped upstream later would make verse fail at init. A
  name nobody upstream would choose costs nothing; deciding it is entangled with
  [contributing.md](contributing.md#open-questions)'s open question about what verse and
  tony-format owe each other.
- **Who empties the container?** `owners` itself is claimed by nobody. The last-owner delete
  takes the whole entity, so it never matters — until somebody wants a shared entity that
  outlives its owners.
- **Is a slot's shape anybody's business?** Nothing says two owners' slots agree about
  anything. That is either the right amount of freedom or the reason a reader cannot make sense
  of a shared entity without knowing who wrote which half.

## The probe

What was run, so the claims above are reproducible rather than argued. Dropped into
`entity/`, against `go-tony v0.0.116`:

```go
// Two owners, one entity. No precondition: disjoint field paths do not race.
mustWrite(t, s, Write{At: ref.At("owners", "ENG"), Value: Obj{"name": "Migration"}.Payload()})
mustWrite(t, s, Write{At: ref.At("owners", "OPS"), Value: Obj{"name": "Migration"}.Payload()})

// Last owner out: a conditional delete whose precondition is about ANOTHER path.
mine, _ := parse.Parse([]byte(`!all.or [!field "ENG", !irtype null]`), parse.ParseTony())
del := Write{At: Path{Ref: ref}, When: []Require{{At: ref.At("owners"), Pattern: mine}}}

ok, _, _ := s.Write(del)                        // false — OPS is still here
mustWrite(t, s, Write{At: ref.At("owners", "OPS")})
ok, _, _ = s.Write(del)                         // true  — and the entity is gone
```

And the four readings of the condition, against payloads built by hand:

| | `{ENG}` | `{ENG, OPS}` | `{}` | `{OPS}` | `{ENG, OPS: null}` |
|---|---|---|---|---|---|
| `!all.field "ENG"` | ✓ | ✗ | ✓ | ✗ | **✗** |
| `!all.or [!field "ENG", !irtype null]` | ✓ | ✗ | ✓ | ✗ | **✓** |

The last column is the store's tombstone, and it is the only column that matters.
