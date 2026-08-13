# Derive: what is left

Status: residual of a landed feature. The fan-out **is built** — `!each {over, key, value}`,
documented in [strata.md](../strata.md#each--when-one-answer-holds-many-facts) — so the
complaint this note used to carry (*a task can find N things, and verse cannot make them N
facts*) is answered. What follows is what the landing deliberately did not do, and why, so that
each one is a decision somebody can disagree with rather than a gap nobody wrote down.

The three questions this note left open were decided by
[derive-each-plan.md](derive-each-plan.md), and the reasoning is there:

| | |
|---|---|
| declared key, or a hash of the element? | **declared** — a hash changes identity when any field changes, so fixing a typo retires one entity and creates another |
| a tag, reserved keys, or a sibling `deriveEach:`? | **a tag** — `derives()` reads two levels of address and treats the rest as payload, and a tag is how that reader is told to read one subtree differently |
| staleness? | **append-only** — below |

## A sweep, and why it is not here

A finding that stops appearing in an answer leaves its entity standing.

The obvious next feature is the sweep: delete this rule's derived entities for this subject
whose key is not in this firing's set. It is what somebody will want the first time a ledger
carries something already fixed, and it should still not be smuggled in under a loop.

Two reasons, and the second is the one that changed since this note was first written.

**A delete is a new capability, not a longer loop.** The whole ordering rests on a derive being
idempotent — same answer, same addresses, same bytes, no change event. A removal is not
idempotent in that way: it is a write whose effect depends on what was there, so it re-opens the
argument the ordering settled, at the level above rather than below.

**And absence is the wrong evidence.** The consumer this was built for is a ledger of findings
([verse-dev-comment-drift.md](verse-dev-comment-drift.md)), which wants resolution to be a fact
somebody wrote — *resolved, at this commit, by this change* — not an inference from a model not
mentioning something this time. A sweep converts the second into the first silently. That is
worse than a stale entity a reader can see, and it is worse in the direction that matters: it
erases a finding nobody addressed.

So the shape a sweep should take, if it is built, is probably not a sweep at all. It is the
consumer writing `resolved` and something reaping on *that* — which is an ordinary charter over
an ordinary fact, and needs no new capability in `derive`.

## Two neighbours, still not folded in

**`derives: [X1 X2 …]`** — a list of derive clauses rather than one. Real, cheap, and *not* what
`!each` is: N is known when the rule is written, so it says "this rule produces several kinds of
fact", where the fan-out says "this answer contained twelve things". `derive:` already has
multiplicity keyed by ADDRESS — several systems, several kinds — so what a list adds is two
entities of the *same* kind, which the map cannot express. Worth doing, separately.

**`count: N` on the matching side** — the symmetric ask, and bigger than its syntax. Today a rule
matches one entity and `$subject` **is** that entity, which is what makes `$subject.id` mean
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
