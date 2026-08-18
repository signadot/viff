# charter spaces

Status: proposal, nothing here runs. It argues from code that does.

Today a charter is one flat set of rules with globally unique names. There is no way for an org
to organise it into per-team-managed chunks, no hierarchy, and no way to call one trigger from
another — in a sequence, or anywhere else.

Two proposals are fused in that sentence and they are separable. **Spaces** are about who owns
which part of the verse and how a charter is organised. **Funcs** are about naming a piece of a
charter so more than one place can use it. Spaces need no funcs and funcs need no spaces; they
are written together here because the same acyclicity argument turns up in both, and because
imports are what a func needs in order to be usable across a boundary.

## Why a namespace rather than an ownership process

The reason to make this a namespace with mechanical rules is organisational, not technical. An
org of any size, handed a shared entity namespace and no way to carve it, will spend months
in a design review about who owns `gh` instead of shipping. The mechanism has to make the
carve-up **first-come and mechanically refused at install** — you declare what you own, the
install proves it disjoint from what is already claimed, and the second claimant gets an error
rather than a meeting.

That is why the shape below includes `system:kind:id` rather than only naming rules. A namespace
over rule names is name hygiene; two teams collide when they both write `gh:pr:*`, and renaming
their rules does nothing about that. The thing worth partitioning is the entity namespace.

## The shape

Like go packages, or jsonschema contexts: a global hierarchical namespace, imported by last
segment, renameable where used. It is the only known shape that gives syntactic clarity of
dependency alongside organisation, and it implies imports must be acyclic.

A space declares a name and a **territory** — the slice of the entity namespace it owns.

```tony
name: acme/payments/billing
owns:
- { system: payments, kind: invoice }
- { system: payments, kind: ledger }
reads:
- acme/platform/sdlc
```

## Territory is spelled as a match union

A territory is a union of system-and-kind patterns, which is *exactly* what a stratum's `match`
already is (`trigger/stratum.go:58`), restricted to the same two segments for reasons that
transfer without change: a pattern that constrained `id` could not be checked at install, and
one carrying a payload predicate would make ownership depend on a state that rules change as
they fire. The containment and overlap predicates are written (`matchContains`, `matchOverlap`),
and space disjointness is `addStratum`'s existing loop (`trigger/stratum.go:333`) one level up.
No new vocabulary, and most of the implementation.

**The hierarchy lives in the name, and the territory is enumerated.** That is forced by the
store rather than chosen. `refPath` renders an entity as `<root>.<system>.<kind>.<id>` and says
so at `entity/logdstore.go:230`: kpaths are `.`-delimited and `/` is a literal character. So a
system named `acme/payments` is one field, not a subtree, and a watch on `acme` is not its
ancestor. Combined with `system` and `kind` taking no operator — they become a docd watch path
and a store prefilter, and a watch path cannot be a predicate — there is no way today for a
space to say "my territory is `acme/payments/*`" in any match.

The alternative is to make `refPath` split the system on `/`, at which point a prefix is a
genuine docd subtree, prefix watches work natively, and the structural objection to an operator
on `system` dissolves for the prefix case. The cost is that `pathToRef`
(`entity/logdstore.go:1498`) can no longer invert a path without knowing where the system ends,
which needs a space registry the store consults on every read. That is a much larger change than
it looks and this note does not propose it; it is recorded because the enumerated form is a
choice and not the only one.

## Strata are per space

Each space declares its own strata, over its own territory.

This does not weaken the ordering, and the reason is worth stating because the opposite is the
natural fear. Strata must be pairwise disjoint because an entity matching two is at two levels
and "strictly higher" then has two answers (`trigger/stratum.go:333`). If space territories are
pairwise disjoint, and each space's strata are contained in its own territory, then strata
across spaces are disjoint *by construction* — the check localises rather than disappearing. Two
spaces may both spend level 10 with no ambiguity, because no entity is in both.

So per-space strata make the existing discipline cheaper, not looser. A team argues with itself
about its internal levels, and argues with the org exactly once, about its territory — which is
the whole point of the previous section.

## Cross-space derives

Per-space levels are not comparable across spaces: space A's 10 and space B's 5 order nothing.
A derive out of A into B's territory therefore has no defined answer to "strictly higher", which
is the one question the ordering exists to answer.

Two coherent resolutions:

- **Forbid it.** A space derives only within its own territory. Spaces influence each other by
  *observing* — reads are already unconstrained at any level, and `strata.md`'s argument for why
  an upward read is safe does not depend on the reader and the target sharing an ordering. This
  keeps imports purely about definitions.
- **Let the import DAG order the spaces**, making the effective level `(space rank, local
  level)`. Then import acyclicity stops being hygiene and becomes the effect guarantee: a derive
  strictly climbs the reads graph. It is the more powerful version, and the cost is that one
  graph then does two jobs — you can no longer import a task func from a sibling utility space
  without that meaning something about levels.

**Propose forbidding it in v1.** The second is a strictly later addition: a charter written
against the first stays legal under the second.

## Who owns the base slices

This is the open question the proposal creates and does not answer, and it will be the first
thing anyone asks.

Nobody's space owns `gh`, `linear`, `task`, `trigger`, `gate`, `motion`, `vote`. Two different
mechanisms already have partial claims on them and neither knows about the other:

- A source registration declares `owns` — an address-shaped claim to being the single writer of
  a slice (`source/own.go`) — and that claim is proved disjoint **source-against-source only**
  (`versed/sources.go:209`). Nothing compares it to a charter's patch target.
- A stratum's `match` claims a slice for a level, checked against rules at install
  (`checkLevels`, `trigger/stratum.go:559`), and never against a source.

So there are already two independent partitions of one namespace with no relation between them,
and the collision is not hypothetical: `strata.md`'s own worked example patches `triaged: true`
onto a `gh:issue` that the reflecting source declares itself the single writer of. That is live
today and mostly invisible. Spaces make it load-bearing, because "which space owns `gh`" has to
have an answer before a team can put a rule in one.

The cheap first move, independent of everything else here, is to run the containment check a
rule's subject already gets against a source's `owns` at registration. Same check, same data, no
new concept. It does not answer the ownership question but it stops the two partitions
contradicting each other silently.

The writers that remain outside any of this — `verse entity put`, a runner, a brokered dial-in
worker, the task and gate mirror — are the class `strata.md` already accounts for: *the ordering
covers a charter to the extent its writes are declared.* A brokered worker is a task, and tasks
were always outside. Giving them spaces would be claiming a guarantee nothing checks; fencing
them is [authority](../roadmap/authority.md)'s business, not a namespace's.

## Imports, and what acyclicity is worth

**Acyclicity of imports is not a runtime safety property**, and the note this replaces implied
it was. A cycle in the verse is made of writes and selections, not of references: two spaces
that import nothing from each other cycle freely through the store the moment one writes what
the other selects on. What import acyclicity buys is legibility of *definition* dependency —
being able to read off which spaces a space needs in order to mean anything. That is worth
having and it is not a guarantee about behaviour. (Under the DAG-ordering variant above it
becomes both, which is that variant's whole appeal.)

**An import naming a space that is not installed yet is held, not refused.** That follows the
precedent a rule naming an absent stratum already sets (`checkLevels` returns dormant;
`promote`, `trigger/stratum.go:444`). Installation is a runtime act with no deploy step, so
order-independence is a property the model already paid for and should not spend here. It
means acyclicity is checked incrementally as spaces arrive rather than once over a file.

## Names

A path-shaped name works today with no change: `/` is legal in a trigger name and `.` is not —
`validTriggerName` (`trigger/trigger_api.go:373`) requires a single bare kpath field because the
chain grammar owns the dot (`review.onDone`), and `platform/deploy/review` passes. `!glob` does
not cross `/`, so `trigger:rule:platform/*` globs segment-at-a-time. A naming convention is
therefore available before any of the mechanism above is built, and may be the right first
version on its own.

**"Space" collides with "scope"**, which is already the copy-on-write overlay and already a
`verse charter add --scope` flag. Two near-synonyms for unrelated concepts is a cost paid
forever by every reader, and the word to pick is the one nobody can confuse with a scope.

**`galaxy` is the candidate**, on the grounds that verse's own name licenses it. It reads
correctly for what this is: a partition of the whole, disjoint from its neighbours, carrying its
own internal structure — which is exactly a territory with its own strata. It is unambiguous in
the tree, and `verse galaxy list` does not read like anything else. The metaphor does not have
to extend past that; the levels inside one stay levels.

---

# funcs

## Trigger funcs are the wrong shape

A trigger cannot be called. It is "invoked" by satisfying its condition during a write, which is
odd on its face — and worse when the condition carries `on:`, since an edge fires once and a
second call has no edge to be.

The sharper reason is identity. A trigger's identity is `(rule, subject)` the whole way down: the
gate question id, the task id `<chain>@<subject>`, the progress facts at
`verse.charter.<trigger>`, the derived entity id `<rule>@<subject>`. There is no room in that
grammar for a call site, so a trigger called twice about one subject collides with itself. Making
room means a call stack, which costs re-fire idempotence being a predicate rather than a search —
the same property that already forbids two `seq` segments in one chain.

So the callable thing is not a trigger.

## A func is a call node, resolved late

An earlier draft of this argument had a func expanded into the caller at install. That is wrong
twice over. `trigger-model.md` states the model has **no compiled form** — a trigger is
interpreted directly — and an expansion is exactly one. And a frozen copy means `verse charter
show` displays the expansion rather than the call: you could not ask who calls a func, or fix
every caller by fixing the definition, which is most of what a func is for.

So the rule stores the call and the engine resolves it when it fires:

```tony
action:
  seq:
  - { call: lint,   args: { paths: $subject.value.changed } }
  - { call: review, args: { depth: deep } }
```

The chain grammar accommodates this without a new separator, because the call node is
self-describing: walk the caller's chain kpath, hit a `call`, resolve the definition, keep
walking inside its body. That is how resume's chain lookup already navigates a charter, and the
task id stays deterministic because the call site is an address in the caller.

## Task funcs

A named parameterised launch spec, callable from any action in any space that imports it. This is
the strongest of the four and the closest to built: `launch.Fragment` is already a partial spec
with a defined overlay discipline — set replaces, absent inherits, tools intersect, narrowing
only — used today by provider selection (`launch/fragment.go`). A task func is a named fragment
plus arguments, and the ceiling rules are written.

## Action funcs and gating funcs

Both fall out of the same call node rather than being separate proposals, with one consequence
each worth writing down:

- **An action func is checked at use, not at definition.** A `derive` in the body has to be
  checked against the *caller's* stratum, so a func is not independently installable-and-valid;
  its validity is joint with each caller's.
- **A gating func's `onDenied` is an ordinary action**, so it recurses into the rule above. Gate
  question ids are `(trigger, gate, subject)` and the trigger is the caller's, so ids stay
  stable through a call.

## Parameters bind under `$args`

Today `$name` resolves subject payload fields plus `$subject`, with a second, flatter substitution
in spawn strings that the docs already call a wart. Putting parameters in that same flat scope
means a parameter named `stage` silently shadows the subject's `$stage`. A charter has no lexical
scope to disambiguate with, so give the arguments a root: `$args.stage`. One segment, no
shadowing, and nothing to reason about.

## Drift, and its two existing answers

Late binding means a definition can move under a caller. Two cases, and the tree already has the
mechanism for each:

- **Live work.** A func edited under a running sequence is what `verse.charter.<trigger>.shape`
  catches — a hash of the declared shape compared on each advance, failing with a reason rather
  than running whatever now sits there. Extend the hash over the resolved body.
- **Installed but not running.** Editing a func can invalidate a caller's install-time checks: a
  `derive` in the body against the caller's stratum, the latch requirement on a spawning action,
  and a body carrying a `seq` called from inside a `seq` (which breaks the unique-ancestor rule
  `seq` depends on). The precedent is `RemoveStratum` (`trigger/stratum.go:361`), refused while
  rules name it rather than silently withdrawing a guarantee those rules were told they had. So
  editing or removing a func re-runs its callers' checks and is refused if any caller breaks.

**The cost to state plainly: install stops being the single moment everything is checked.** A
rule's validity becomes a joint property of the rule and the current definitions, so checks run
on both edges — installing a caller, and editing a callee. That is the price of not having a
compiled form, and it should be named here rather than discovered later.

## What is open

- **Who owns the base slices.** The blocker, above. Everything else can be built around a wrong
  answer here; this one cannot.
- **Whether cross-space derive is forbidden or DAG-ordered.** v1 forbids; the second is additive.
- **Whether territory should be a real subtree.** The `refPath` split, and whether a space
  registry in the store's read path is worth what prefix matching buys.
- **`seq` inside a func, called from inside a `seq`.** Refused, presumably — but "no chain may
  contain two `seq` segments" is stated over chains, and a chain now spans two documents.
- **What renaming on import means for `verse charter show`.** A rule read back should say what
  was written, and a renamed import is a fact about the caller that the callee does not carry.
