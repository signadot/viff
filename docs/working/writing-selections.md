# Writing selections: `!read`

Status: proposal, nothing here runs. It adds one thing to the charter grammar — a reference that
reaches an entity other than the firing's subject — and most of the note is about the one place
that is dangerous and the check that makes it safe.

A charter can already *condition* on state outside its subject: `when` and `on` both name other
entities. What it cannot do is **copy** any of it. Every reference an action can write —
`$subject` and its parts, a payload field, a completed process's `$result` — reads the firing's
own subject or the process that just finished, and nothing else.

```
- name: urgent-label
  stratum: observe
  condition:
    subject: { system: gh, kind: issue, value: { labels: { urgent: null, linear: null } } }
  action:
    patch:
      labels: { urgent: !delete null }
    derive:
      signadot:
        feature:
          priority: urgent
          linear: $subject.value.labels.linear
          why: !read { system: git, kind: repo, id: $subject.value.headCommit }
```

`!read <match>` resolves to the payload of the entity that match names, read at the commit that
caused the firing. The address string form works too, since expansion runs before the read:
`!read "git:repo:$subject.value.headCommit"`.

## One entity, named — which is what makes the rest tractable

**A `!read` match must NAME**: all three segments definite, and no payload predicate. That is
`Match.Names()`, which already exists and already draws exactly this line — *"a payload predicate
makes it a selection, whatever the three segments say"*. Refuse anything else at install.

It is worth being clear that this is a restriction chosen for what it buys, not one inherited
from the matcher. Naming makes `!read` a lookup rather than a query, and every awkward question a
query would raise simply does not arise: no ordering to define, no "it matched three, now what",
no truncation, no cost that scales with the verse. The id may still be a reference, so the
address is as dynamic as anyone needs; what is fixed is that exactly one entity answers.

This is also why the verb matters. The first sketch called it `!select`, which echoes
`Match.Select` — and that returns a set, so the name would promise the thing this deliberately
rules out.

## In a `derive` it needs one check, and the check already exists

This is the part that is not free.

[Derive inertness](../strata.md) rests on a fact `!read` breaks: *a reference cannot reach
another entity at all*. That is what guarantees the levels above cannot influence what a derive
writes, and therefore why a rule may `on` or `when` anything at any level without a rule
governing reads. Let a derive read an arbitrary entity and a read from above becomes live: the
thing above changes, the derive writes different bytes, the level above wakes, and round it goes
— across a level, which is the one cycle the ordering exists to exclude.

**The rule that saves it: a `!read` inside a derive must resolve at or below the rule's own
stratum.** An entity at level ≤ mine is written only by rules at that level, or derived into from
below it. Never from above. So inertness holds exactly as before.

And it is decidable at install with the machinery already in place: a `!read` names a system and
a kind, a stratum's `match` constrains a system and a kind, so this is `Matches.contains` run in
the opposite direction from check #2. In the example above, `git` sits below `signadot`, so it
passes. A `!read` from a stratified rule naming an *unstratified* target has to be refused, for
the same reason a derive from the implicit stratum is: no level, so "at or below" has no answer.

In a plain `patch` there is no such concern — nothing downstream depends on a patch being
reproducible — so `!read` there is unconstrained.

## It copies, it does not track

The entity is read once, at the commit that caused the firing. If it changes afterwards, nothing
re-derives: the rule fires again only when its own subject changes, or when an `on`/`when` it
carries covers the target.

That is the intended semantics — the whole point is copying — but it wants stating plainly,
because "why did this go stale" is the question someone will ask, and the answer is not a bug. A
charter that wants to track adds the `on`.

Reading at the causing commit rather than at the moment the action runs is the same rule
`$subject.value` follows, and for the same reason: a firing held at a gate acts whenever the
answer comes, and a late read writes from a state nobody evaluated.

## The cost, and how to not pay it twice

Unlike `$subject.value`, a `!read` target is a *different* entity from the subject, so it is
almost never among the causing commit's own changes — meaning the cheap path does not apply and
every `!read` wants a snapshot. `Store.AsOf` answers by replaying the log to that commit, which
is not cheap.

So the snapshot should be **one per firing, taken lazily and shared** by every `!read` and every
`$subject.value` in that action, rather than one per reference. Worth building that way from the
start rather than discovering it: the natural implementation takes one snapshot per reference,
and an action with three reads would pay three replays for one answer.

## Open

- **Whole payload, or a path into it.** `!read <match>` giving the entity's payload is the simple
  version, and wanting one field then needs a second construct. `!read <match>.headCommit` is
  spellable but is a grammar addition rather than a tag.
- **An absent target: fail, or null.** The existing rule is that an unresolved reference fails
  the firing, deliberately, so that "the process never said" cannot launder into "the process
  said nothing". Consistent answer is fail. But a named entity that does not exist yet is a
  legitimate state elsewhere in the model — a patch on a named subject creates it — so this is
  worth deciding rather than inheriting.
- **Spawn strings.** Almost certainly not: a payload has no text form, which is the same reason
  `$subject.value` is absent from the flat bindings a spawn's templates use.

## Parked: an id drawn from the payload

The sketch this note came from also wanted the derived entity's *id* to come from the subject —
`feature: $subject.value.labels.linear`, so that two GitHub issues carrying one Linear id land on
one feature. That is correlation, and it is a real want, but it is a separate change and a larger
one:

- The **level check survives untouched**, since check #2 reads only system and kind — which is
  the payoff from restricting a stratum's `match` to those two.
- **Inertness survives too**: the id would be a function of the subject's payload, and nothing
  above can write the subject.
- What it spends is **single-writer**. Today no two rules and no two subjects can address one
  derived entity, for free. A payload id makes two subjects converge on purpose, which puts
  derived entities back under [two triggers, one field](../convergence.md) where the rest of the
  model already lives. Survivable — `patchEntity` merges through `Modify`, so two writers touching
  different fields is fine — but it should be opt-in and `advise` should say so at install.
- The real cost is **lifecycle**: with an engine id a re-fire rewrites one address, where a
  payload id orphans the old entity whenever the label changes. Nothing reaps derived entities,
  so [that corner](../strata.md) stops being theoretical.
- And the **spelling is unresolved**. `feature: $ref` with a block under it is not expressible,
  and the obvious repair — a reference in the key — is already refused with a reason worth
  keeping: *"a reference in a key would make the patch's shape depend on the payload, and the
  shape is what a reader of the charter is entitled to know without running it"*
  (`trigger/deref.go`). A tag sidesteps it, since a tag is not a key:
  `feature: !id($subject.value.labels.linear) { priority: urgent }`.
