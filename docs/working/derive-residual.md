# Derive: what is left

Status: residual of a landed feature — `derive` is built and documented in
[trigger-model.md](../trigger-model.md), and this is not a plan to change what it does. What is
left is that **one firing derives exactly one entity**, and a drive has now walked into needing N
from three different directions. The blocking part is not a missing loop; it is an invariant that
`derive` deliberately holds, and the questions below are about whether it can be generalized
without being weakened.

## Three arrivals at the same missing thing

[verse-dev-coderunner.md](verse-dev-coderunner.md) records it as finding 8 — *a task can find N
things, and verse cannot make them N facts*. Since then the same drive reached it twice more:

- a task that answers with twelve findings has one derive, so eleven of them are prose;
- `docs/trigger-model.md` is too big to be one task, and the scoping that fixes it is **per
  section**, which is a fan-out over a list nobody can write in advance;
- the same, per **claim**, which is the finer version of the same fan-out.

Three approaches, one missing primitive. That is the strongest argument in the note for building
it, and it is worth more than the original complaint was on its own.

## What blocks it, quoted rather than paraphrased

`validateDerive` refuses a `$result` reference inside a derive, and the refusal carries its own
reasoning:

> a derive may reference the firing's IDENTITY and never its STATE, and `$subject` is the only
> reference that is identity. The distinction is what the whole ordering rests on: a derived
> entity's address is a function of (rule, subject), so a derive whose content is also fixed by
> (rule, subject) writes the same bytes to the same address on every re-fire — a no-op the store
> ends by itself, which is why a rule may read a HIGHER level without that read becoming a cycle.
> A value read from a payload changes as rules fire, so a derive carrying one would make exactly
> that read live.

So the chain is: content fixed by (rule, subject) ⇒ same bytes ⇒ same address ⇒ **no-op** ⇒ a read
of a higher level is not a cycle. Every link is load-bearing, and a fan-out over a task's answer
breaks the first one by construction — the content *is* state, and N elements need N addresses
where the scheme yields one.

`patch:` may carry `$result` and does. That is not an inconsistency: a patch writes the firing's
own subject, at its own level, so it moves nothing upward and the ordering has no opinion.

## The generalization that might resolve it

The invariant is stated over (rule, subject) because that is the only address `derive` has. Its
*justification* is weaker than its statement, and the weaker form is the interesting one:

> **A derive may carry state iff its address is a function of that same state.**

Today's rule satisfies this degenerately — address and content are both `f(rule, subject)`. A
fan-out satisfies it when each element's id comes from a key **in that element**: the same answer
yields the same twelve addresses carrying the same twelve payloads, which is the same no-op by a
different route. Nothing about the cycle argument changes; what changes is which function the
address is of.

It is checkable at install, which is the property worth insisting on. "The content may not mention
state" and "the content may mention state the address is derived from" are both static checks over
the same syntax tree.

## The shape on the table

```tony
derive:
  demo.finding:
    over: $result.findings        # a kpath; each match binds $item
    key:  $item.ref               # the address is a function of the element
    value:
      ref:  $item.ref
      was:  $item.was
      now:  $item.now
```

Two choices inside it that are settled enough to record:

**A kpath, not a named list field.** go-tony already has `ListKPath`, so `over:` can select across
structure rather than requiring the answer to put its items in one array at one depth. Strictly
more general, and it costs nothing because the traversal exists.

**`$item` rather than `$resultItem`.** The construct wants to work over `$subject.value.something`
eventually, and a name that hard-codes the source would have to be renamed or duplicated the first
time it does.

## Question 1 — a declared key, or a hash of the element?

`key: $item.ref` is a declaration: the charter says which field is the identity, and an element
missing it is refused at firing.

The alternative is no declaration at all — hash the element and use that. It always works, it
never needs a schema, and it is wrong in a specific way: **identity changes when any field
changes**, so correcting a typo in a finding's `now:` retires one entity and creates another.
Everything bound to the first is orphaned by an edit that a reader would call the same finding.

Declaring the key has the opposite failure: an answer whose elements have no stable field cannot
fan out at all, and says so. That is the better failure — it is loud, it is at install or at the
first firing, and the fix is in the answer contract where the shape is already declared.

What is not settled is what happens to an element whose `key` resolves empty. Refusing the firing
is one answer; deriving the others and recording the refusal is another. The first is simpler and
matches how the rest of the engine treats a malformed action; the second loses less work.

**Positional ids are not on the table.** A re-run finding eleven instead of twelve renumbers every
entity below the gap, and everything downstream of them changes identity for a reason that has
nothing to do with the work. The drive asked for exactly this evidence and got it: the fixes it
found were addressable by `POST /tasks/{id}/say`, `HostBroker`, `resolve` — content, stable across
runs.

## Question 2 — does the address get a shaped object?

`derive` today is addressed by exactly two levels of nesting and *everything below is payload*,
which is what lets the form be read without a delimiter (`derives()` in `stratum.go` relies on it).
`over:`/`key:`/`value:` puts a shaped object where raw payload was, so the reader has to know the
difference between a fan-out and a payload with a field called `over`.

Options, none free:

- **A tag.** `demo.finding: !each {over: …, key: …, value: …}` — unambiguous, and tags are already
  the grammar's way of saying "read this differently".
- **Reserved keys.** Cheap to write and ambiguous forever: a payload that legitimately has `over`
  becomes unspellable.
- **A sibling field.** `derive:` stays exactly as it is and `deriveEach:` is the new one. Most
  conservative, and it doubles a vocabulary that is currently one word.

The tag is the most likely right answer and is the one that touches the existing reader least.

## What has no answer: staleness

Twelve entities derived, the task re-runs, eleven findings. The twelfth stays.

`derive` has never had to think about this because one firing wrote one address and a re-fire
overwrote it. A set has a shape a single entity does not: membership. The candidates are a sweep
(delete this rule's derived entities for this subject whose key is not in this firing's set) and
leaving them, with readers filtering on the firing that wrote them.

The sweep is what somebody would want and it is a **new capability** — a derive that removes —
which re-opens the ordering argument from the other end, because a delete is a write that is not
idempotent in the same way. It should not be smuggled in under a loop. The honest first version is
append-only, said out loud, with this paragraph as the reason.

## Two neighbours, deliberately not folded in

**`derives: [X1 X2 …]`** — a list of derive clauses rather than one. This is real and cheap and it
is *not* this: N is known when the rule is written, so it is "this rule produces several kinds of
fact", where the above is "this answer contained twelve things". Worth doing, separately. Note that
`derive:` already has multiplicity keyed by ADDRESS — several systems, several kinds — so what a
list adds is two entities of the *same* kind, which is the collision the map cannot express.

Keeping `derive:` as sugar for a one-element `derives:` costs nothing; removing it would be a
contract break across every charter and every doc for no capability.

**`count: N` on the matching side** — the symmetric ask, and bigger than its syntax. Today a rule
matches *one entity* and `$subject` **is** that entity, which is what makes `$subject.id` mean
something in the derive. A count predicate is evaluated against a **set**, which changes what fires
and leaves `$subject` denoting one of them, all of them, or the set — and there is no obvious
answer that the derive's paths survive. `!lt(count(), N)` has the same shape underneath: it makes
match evaluation collection-scoped where it is per-document now.

And a threshold re-fires: "N entities satisfy this" is also true of the N+1th. Whether that is
once-at-the-boundary or every-time is [cycle-throttle](../cycle-throttle.md)'s question arriving on
a new surface.

Worth separating from both: the **answer contract** can already say "this task must produce N
findings". The count above is N entities *accumulating*, possibly from several tasks and sources.
Different Ns, and only the second needs new machinery.

## What the drive already settled

The run that produced the evidence answered with `status: fixed` and a file name — **two scalars,
no list** — while enumerating its findings in prose. The contract did that deliberately;
[docs-drift.answer](verse-dev/docs-drift.answer) argues against free text in a governed shape, and
that argument survives.

So the two features are coupled: **to derive N entities the answer must declare N structured
items.** The contract has to grow the list before the fan-out has anything to fan out over, and
the drive's own note is where that change belongs.

One wart found in the same run and worth fixing before any of this: the answer's `file` came back
as `verse/docs/task-control.md` — **workspace-relative, including the repository directory**. As a
component of an entity id that is wrong; the same file under a different environment would carry a
different prefix and derive a different entity.
