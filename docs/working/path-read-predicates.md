# A refusal that outlived its reason

Status: the change described here is made — `!list-paths` takes a `value:` predicate as of this
note. What is written down is *why the refusal was there*, because it was correct when written
and stopped being correct without anybody touching it, and that is the failure mode worth
recognising again rather than the feature.

## What happened

`!read` — the single tag `!get-path` and `!list-paths` were split out of — refused a payload
predicate, and argued for it honestly:

> It NAMES exactly one entity — all three segments, no payload predicate, and system and kind
> literal — which is a restriction **chosen for what it buys** rather than one inherited from
> the matcher. Naming makes it a lookup instead of a query: no ordering to define, no "it
> matched three", no truncation, and no cost that grows with the verse.

Four things bought, and at the time all four were real: `!read` answered with one node, so a
predicate would have turned it into a query it had no shape for.

Then the tag split by cardinality ([`3c0d9f1`](../trigger-model.md#get-path-and-list-paths--reaching-another-entity)),
and `!list-paths` paid three of the four on the way past — in the same commit, as deliberate
design:

| what naming bought | what the list form did with it |
|---|---|
| no ordering to define | defined one: `Match.Select` sorts by ref, then document order |
| no "it matched three" | that is what a list **is** |
| no cost that grows with the verse | `id: "*"` is explicitly the list form's business |
| no truncation | **still unpaid** — see below |

The refusal was carried onto both halves. On `!get-path` it kept its argument. On `!list-paths`
its argument had been spent, and the words were replaced with new ones:

> a path read addresses entities by identity. Selecting by what they LOOK like is a different
> question, and one whose answer changes as rules fire

That reason does not survive contact with the same codebase. A firing's path reads are answered
from **one snapshot taken at the causing commit** (`occasion.snapshot`), shared across the whole
action precisely so two reads cannot "learn them about three different [worlds]".
[Convergence](../convergence.md) states it as a property: *"An action's reads are pinned to C
too, and the ones that are say so in their spelling."* An answer that is pinned to C does not
change as rules fire. The replacement rationale was never true.

Worth being exact about the order, because the tempting story is wrong: the pinning was not
added later. It landed in `fa8eaff`, the refusal was reworded in `3c0d9f1`, and the two are the
same day. This is not a rationale that went stale — it is one that was **written stale**, when a
restriction survived the collapse of its reason and needed something said in its place.

## What the refusal was costing

The read it forced is the *more* expensive one. `id: "*"` walks a whole kind; a predicate
narrows that walk. So the rule was buying "no cost that grows with the verse" by mandating the
version whose cost grows with the verse — the one thing on the original list that a predicate
straightforwardly helps with.

[`scripts/lineage.sh`](https://github.com/signadot/verse/blob/main/scripts/lineage.sh) is where this surfaced: a release's manifest had
to collect every trace in the store, because the thing that would have narrowed it was refused.

## The change

`value:` on `!list-paths` only. `!get-path` keeps the refusal and keeps the original argument
for it, now stated in its own terms rather than by appeal to identity: a predicate can name
several, a value slot takes one, and — the sharp version, from `Match.Names` — *"an entity that
is not there does not satisfy `{state: green}`, and one that is there might not either."*

Mechanically it is small, which is itself evidence the refusal was not load-bearing: `Match`
already carries a `value` dimension, `Select` already resolves it, and `pathSpec.list` already
built a `Match`. The predicate is part of the address, so it is expanded before the read like
the rest of it — which is what makes it worth having over a constant:

```tony
prs: !list-paths { system: lineage, kind: trace, id: "*",
                   value: { target: $subject.value.target }, path: pr }
```

One rule rather than one rule per branch. The level check is untouched: it reads `System` and
`Kind`, which are still literal.

## What is still open

- **Truncation.** The fourth thing naming bought, and the list form never paid it. `id: "*"`
  with no bound is the same exposure with or without a predicate, so this is a pre-existing gap
  the split left rather than one the predicate opens — but it is now the only item on the
  original list that is still owed.
- **Inside a `derive`.** Allowed, and it looks safe for the reason the ordering exists: a derive
  writes strictly above its own level and reads at or below, so its writes cannot reach its own
  read set however that set is selected. That is an argument, not a test, and derive inertness is
  what the whole ordering rests on. Someone should try to break it before leaning on it.
- **Whether a predicate read should track.** A predicate makes the copied value more obviously a
  query result, and a query result has no address to re-read. That is the same staleness
  [derived views](derived-views.md) is about, and the answer there covers this without change:
  the subtree is still `system:kind`.

## The reusable part

A restriction and its reason can come apart silently, and the moment it happens is a
*refactor that pays the reason's costs on purpose* — the split here bought ordering,
multiplicity and unbounded cost deliberately, and nothing went back to ask what those had been
buying. The refusal then looks load-bearing because it is old and because something plausible
has been written next to it.

What caught it was not review: it was writing a demo that hit the refusal, quoting the message
to explain the edge, and then being asked why the message's reason should apply when reads are
pinned to a commit. Worth asking of any restriction whose comment argues from a property the
code guarantees elsewhere — the comment is where the two can disagree without anything failing.
