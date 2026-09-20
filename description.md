# residual: derive — the neighbour not folded in, and what the fan-out does not reach

# Derive: what is left

Status: residual of a landed feature. The fan-out **is built** — `!each {over, key, value}`,
documented in [strata.md](../strata.md#each--when-one-answer-holds-many-facts) — so the
complaint this note used to carry (*a task can find N things, and verse cannot make them N
facts*) is answered. What follows is what the landing deliberately did not do, and why, so that
each one is a decision somebody can disagree with rather than a gap nobody wrote down.

The three questions this note left open were decided by the `!each` landing; the reasoning is in
[done.md](done.md), and the append-only decision is in [strata.md](../strata.md).

## A neighbour, still not folded in

**`count: N` on the matching side** — the symmetric ask, and bigger than its syntax. Today a rule
matches one entity and `$subject` **is** that entity, which is what makes `$subject.name` mean
something in a derive. A count predicate is evaluated against a **set**, which changes what fires
and leaves `$subject` denoting one of them, all of them, or the set — with no obvious answer that
the derive's paths survive. And a threshold re-fires: "N entities satisfy this" is also true of
the N+1th, so whether that is once-at-the-boundary or every-time is
[cycle-throttle](../cycle-throttle.md)'s question on a new surface.

## What the fan-out does not reach

**`over` must name a list.** A kpath could select across structure, and this note originally
reached for `ListKPath` so an answer would not have to put its items in one array at one depth.
The landing refused that, because `$data.findings` and `$data.findings.*` would then be two
spellings, one of which yields the list as a single element whose `key` cannot resolve — a
fan-out that quietly derives one entity. A charter wanting a nested answer flattened says so in
its answer contract, where the shape is already declared. If a real case turns up that cannot,
the general form is a small change to `eachElements` and a large change to what a reader has to
know.

**An element's key is a scalar.** A composite key — two fields together — is spellable only by
asking the answer for a field that already holds both. Nobody has wanted it yet.