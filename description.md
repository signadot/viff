# residual: attached resolvers — the two unbuilt drivers, and a `by` nobody verifies

# Attached resolvers: what is left

Status: residual of a landed feature. What runs is [attached
resolvers](../attached-resolvers.md) and [reference/resolvers.md](../reference/resolvers.md) —
bindings by name and by condition, ownership by layer, an instance per origin, the two streams,
`escalate`, a deadline that does the same thing more slowly, `exclusive` and its override.

What is here is what those two name as missing, gathered so the next person does not have to
reconstruct why each was left. Nothing here is a design owed to anything else here.

## 2. The `llm` and `script` drivers

`url` is the only driver built, deliberately: it needs no model credentials, so the whole path
is testable deterministically against an `httptest` server, and it is the driver where verse
runs *nothing* — it hands over two streams and applies what comes back. Everything the other
drivers need is this plus a runtime, so building it first meant the second driver is a driver
rather than a protocol.

`method.vector`, `.tools` and `.grant` are already carried on every binding and are inert for
`url`, which runs nothing verse can gate. They are the envelope the other two will need.

`driver: llm` earns its place only if it does something a script calling the same API cannot.
Otherwise it is sugar over a chat call, and thirty lines of script would serve better. The
candidates are the ones verse could do and a script could not: seeding the conversation from the
stream it is already reading rather than re-fetching it, and holding the position/notes handover
across a context refresh.

And the measurable question under both, unanswered: **does a long conversation hold the
protocol?** Does it stay silent on frames that do not concern it, keep `escalate` from drifting
into approval as context fills, and not re-answer what it has already answered? That is
measurable rather than arguable — one stream, both shapes driven over it, counting the frames
each got wrong — and it decides whether `llm` is a driver or a bad idea. It has not been run in
anything that was kept: the harness for it lived in a scratchpad and is gone, so whoever takes
this builds it again, which is a morning rather than a blocker.

## 3. `by` is self-asserted, so `exclusive` is bypassable by spelling

`--as` is taken as given. A person who types the owner's ref answers as it:

```sh
verse gate approve <id> --as verse.resolvers.security.payments-review    # accepted
```

The answer is accepted, `by` reads as the binding, and `overrode` is **absent** — so the record
does not name both parties, which is the one thing `exclusive` was supposed to buy. The design
is explicit that exclusive is not real against whoever holds the operator API, and answers that
with the override: *"a named act rather than a hole."* Impersonation is an unnameable override,
which defeats the visibility rather than the enforcement.

The instance route is not affected — `POST /resolvers/instance/{id}/verdicts` derives `by` from
the URL, so a service cannot claim to be another binding.

This is not fixable here. Verifying that a caller *is* who it says is
[authority](../roadmap/authority.md)'s whole subject, and anything local would be a lock with the
key taped to it. It was confirmed by hand — the command above, run against a live verse, is the
whole of the reproduction — and there is no test for it in the tree, deliberately: a test would
assert the hole stays open.

The adjacent half of the same gap: **who may install a binding.** A standing binding is exactly
as strong as the ability to add and remove it, and the store has no per-slice write authority.

## 4. Resolvers over a scope other than baseline

Untried. A scoped run's questions live in that scope's copy-on-write view, and nothing has
checked whether a binding installed in baseline covers them, or should.

## 5. The `resolver → trigger` import cycle

`mirror` needs to ask "which standing binding covers this subject" and cannot import `resolver`,
because `resolver → trigger → mirror` already exists. So the lookup arrives as a callback —
`mirror.Meta.Owner` — supplied by `versed`, the one party that can import all three.

It has cost twice already, which is why the TODO on it is not a tidiness note: an optional
function field built by hand at each call site is silently forgettable, and *was* forgotten (a
resumed task lost its standing layer in the commit that added it); and `mirror` spells
`"resolvers"` and `"instance"` as private constants because the real ones cannot be imported.

The edge to cut is `resolver → trigger`, not `trigger → mirror`. `resolver` imports `trigger`
for exactly one identifier — `trigger.Match`, the type of `Condition.Subject` — and `Match` is a
selector over entities that delegates to `entity.Pattern`, already used from `schema`, `versed`,
`cmd/verse` and `entity` itself. Lifting it below `trigger` is right on its own merits and makes
`mirror → resolver` legal as a side effect. It is a real move: 592 lines with its own schemagen
tag and custom codec.

## 6. What a re-posed question should mean

A trigger gate's id is a function of (rule, gate, subjects), so a re-firing re-poses the same
one. A settled record is no longer merged over — the answer is cleared and `askedAgain` marks
it, so the docket never shows a question stamped with its own answer — but *whether* it should
be asked again is the rule's business, and a rule with no `onDenied` re-asks by default. The
advisory names it at install. Whether that default is right is open.

What it costs a RESOLVER is worth saying, since it is not obvious from the rule side: a resolver
over a rule that does not latch is asked the same question forever. With two rules matching one
subject it compounds — each rule's action re-selects the subject and re-fires both.

The advisory that names it is `adviseDenyLatch`, and what it decides is now the same relation
`checkLatch` decides — whether the subject's predicate and the action's post-state are
unsatisfiable (`trigger/deselect.go`). The key intersection
they used to share was collapsed to the top-level keys of an object, and a payload is any tony
value, so a rule with a compound subject predicate earned a note it had not earned: `value: !and
[…]` reported the TAG NAME as the field it read, which no `onDenied` could ever write. That is
gone, along with the reverse — an `onDenied` writing the subject's own state back was called a
latch and re-posed the question forever.

Both callers take the same tolerance: a note is earned by a refusal the check can SEE leaves the
subject where it was, never by one it cannot decide. Advice that is wrong is worse than silence.

What is still open here is the DEFAULT — whether a rule with no `onDenied` should re-ask — which
no relation decides.

## What was answered differently than the design asked

Kept because the reasoning is the useful part.

- **Escalate on conflict.** Overlapping conditions are no longer refused at add: disjointness of
  two predicates is undecidable, so the refusal had to be conservative and would have blocked
  legitimate pairs. They are ORDERED instead — first by name owns — which keeps exactly one
  answerer per layer without deciding anything at install, and covers the runtime case a refusal
  could not see. Promoting on conflict became unnecessary rather than deferred.
- **When an instance is reaped.** The store is already the evidence — a position report and a
  note were commits when they were made — so an instance is machinery rather than an audit
  trail, and belongs to its binding rather than to the thing it watches. Removing the binding
  reaps it, and nothing else does.
- **A deadline needs no sweeper.** Expiry is evaluated when a reader arrives, because the layer
  above is the only party it is for and it learns by reading. Whether that stays true once
  something *else* depends on the timing — a rule that fired on an overdue question, a report
  that counted them — is the open half, and nothing wants it yet.