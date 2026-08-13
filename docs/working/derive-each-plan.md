# `deriveEach`, and the other direction: implementation plan

Status: plan, nothing built. The design for the fan-out is
[derive-residual.md](derive-residual.md); this file does not restate its reasoning, only the
order of work and what each step must prove. It also **decides** that note's three open
questions, which is what writing a plan is for — see [What this decides](#what-this-decides).

Two changes, and they are the same seam read in opposite directions:

| | |
|---|---|
| **out** — a charter's structured state into a prompt | `payloadBinding` renders a composite as tony instead of Go's `map[…]` |
| **back** — a task's structured answer into facts | `derive: <sys>.<kind>: !each {over, key, value}` writes N entities, not one |

They are independent and could land in either order. Out goes first because it is one function,
because nothing depends on it, and because it is what makes the fan-out drivable: a rule that
derives twelve findings is hard to iterate on when the next prompt cannot read them back.

The thing both are for is a charter that keeps a **ledger** — outstanding findings, and what
resolved them — instead of re-deriving its whole world on every commit. That charter is
[verse-dev-comment-drift.md](verse-dev-comment-drift.md), whose reviews currently re-audit a
1500-line file per push because nothing remembers what was already checked.

## Order of work

Five steps. Each is independently reviewable and leaves the tree green. 2 must precede 3 (the
engine cannot fire a shape it does not parse); 5 is the acceptance test and needs 1 and 3.

### 1. A composite binds as tony, not as a Go map

`payloadBinding` (`trigger/trigger_api.go`) builds a spawn's flat substitution map with
`b[k] = fmt.Sprint(v)` over an `*ir.Node`. For a scalar that is right and must not change. For a
composite it yields Go's map formatting:

```
$findings → map[logdstore.go:nodeToPayload:map[status:open was:logd's tombstones]]
```

A model reads that, which is why it has not been noticed. It is still wrong in three ways: it is
not a format anything documents, it is lossy where a value contains a bracket or a colon, and it
cannot be pasted back into the verse it came from.

Render composites with `codec.Marshal(v, codec.Tony)`. Scalars keep exactly the text they have
today — reuse `scalarText` (`trigger/deref.go`), which is already the arbiter of "has a text
form" for the patch side, rather than growing a second opinion about it.

Note what this does NOT do: it does not make a spawn's strings use the reference machinery. They
stay the older, flatter substitution — top-level fields, `$var` left in place when unbound —
which is the documented behaviour and the reason an installed charter cannot start failing on a
name it never bound. This step changes the TEXT a composite renders as, nothing else.

**Proves:** a payload field holding a document reaches a prompt as tony that parses back to the
node it came from (round-trip it in the test, do not compare strings). And the negative, which is
the one that could break somebody: every scalar binds byte-identically to before — `3` not `"3"`,
no quotes, no trailing newline.

`Tier: change`. Any charter interpolating a composite gets different text, and while nobody can
be relying on `map[…]`, the honest tier is the one that says a spawn's prompt changed.
`docs/trigger-model.md`'s substitution paragraph moves with it.

### 2. Parse and validate `!each`, and refuse to fire it

No new behaviour at a firing yet, and the refusal is the point of splitting here.

`derives()` (`trigger/stratum.go`) walks `<system>.<kind>` and treats everything under it as
payload. It gains one branch: a value node tagged `!each` yields a `deriveTarget` carrying an
`Each` triple — `Over`, `Key`, `Value` — instead of a payload. The tag sits exactly where
`derives()` already looks, which is why the tag is the cheap syntax and a sibling `deriveEach:`
field is not (see [What this decides](#what-this-decides)).

`validateDerive` gains the rules, all install-time, all message-carrying:

- `over` is a reference and nothing else — a kpath into the payload the action reads. In a
  completion hook that is the task's (`$data.findings`); in a trigger's own action it is the
  subject's (`$subject.value.…`).
- `key` is a reference rooted at `$item`. An element with no stable field cannot fan out and
  says so, which is the better failure.
- `value` may reference `$item` and the subject's IDENTITY parts. `$subject.commit` stays
  refused, for the reason it is refused today.
- The existing "a derive may reference identity and never state" refusal is **relaxed exactly
  under `!each`**, and the error message that carries that rule has to learn the exception.
  The generalisation is the residual's: *a derive may carry state iff its address is a function
  of that same state*.

`allDerives()` must return Each targets too, or the strata check stops seeing a whole class of
cross-level write — which is the one bug in this step that would be invisible.

An Each target that reaches the engine **fails the firing with "not implemented"**, deliberately.
A parsed shape that silently derives nothing is the failure this project keeps finding in other
people's systems; one step of the tree holding a loud refusal is green, a quiet no-op is not.

**Proves:** a well-formed `!each` installs; each malformed one is refused by name and rule; a
plain `derive:` parses exactly as before (same node, no tag, same payload); a `!each` inside an
`onDone` is seen by the stratum check; and a firing that reaches one fails loudly.

### 3. Fire it

`trigger/trigger.go`'s derive loop (`for _, d := range a.derives()`) gains the Each arm:

1. resolve `over` against the action's reference root — `refRoot` for the trigger case, the
   synthesised task payload for a hook — and walk it with `ListKPath`, which is what makes `over`
   a path into structure rather than a named list field;
2. per element, build a root binding `$item` beside `$subject` (a small `itemRoot` beside
   `refRoot`, not a mutation of it);
3. resolve `key` → the address suffix, `value` → the payload;
4. write with the existing `patchEntity`, so causal depth, the cycle throttle and
   `verse.meta.derivedFrom` all keep working unchanged.

The address is the one new scheme and it needs naming rather than inventing at the call site:
`deriveID` is `(rule, subject)` today and becomes `(rule, subject, key)` under `!each`. It must
go through `safeSeg` and the existing `+<hash>` disambiguation, and it must round-trip in a test
— a key holding `/`, `:` or a space is the ordinary case here, since the keys people will reach
for are paths.

An element whose `key` resolves empty **fails the firing**. Not "derive the others and record the
refusal": a fan-out that silently drops a member produces a ledger that is quietly short, and
short is the one failure a reader cannot see.

**Proves:** an answer with twelve findings becomes twelve entities, each addressable by
`verse entity get`; re-running the same answer writes the same twelve addresses with the same
bytes and raises no commit, which is the no-op the whole level ordering rests on; a thirteenth
finding adds one entity and touches none of the other twelve; an element missing its key fails
the firing with the element's position in the message.

### 4. Say what it does not do

Append-only, and written down rather than discovered. A finding that stops appearing in an
answer leaves its entity standing; nothing sweeps.

That is the residual's decision and it is the right one here for a reason the residual could not
see yet: the ledger charter wants **resolution to be a fact somebody wrote**, not an absence.
"Gone from this run's answer" and "fixed, at this commit" are different claims, and a sweep would
turn the first into the second silently — including when the difference is that a model did not
mention it this time. A charter that records `resolvedAt` explicitly is the consumer this
limitation suits, and the one that makes a sweep unnecessary rather than missing.

The docs say so in the same breath as the feature: `docs/strata.md` gains the `!each` section,
and it carries this paragraph, not a TODO.

**Proves:** nothing to run. It is the step that stops step 3 from being read as more than it is.

### 5. Drive it: comment-drift keeps a ledger

The acceptance test, and the reason for the other four.

`comment-drift`'s prompt currently says "every comment in that file", so every push re-audits
`entity/logdstore.go` end to end — 17 boundaries and 41 shell calls on the last run, most of it
rediscovering what the run before it already knew. With a ledger it becomes: here are the
outstanding findings, here is the diff, fix what you can and tell me what changed.

- the outstanding findings reach the prompt through step 1;
- the answer's findings become entities through step 3, keyed by **Go's own naming** — see
  [Keying a finding](#keying-a-finding);
- a person resolves one by writing to it, and the charter does not fight them, because it only
  ever patches keys the answer named.

One discipline the charter side owes, and it is not a grammar change: **do not patch a whole
answer onto a subject.** An answer contract is a match, and a match over an object is a subset
constraint — it can require fields and cannot forbid extra ones, so `patch: $data` hands the
model every key the subject has. Patch under a fixed key (`patch: {findings: $data.findings}`),
which costs nothing and bounds it. Whether that deserves an install-time advisory is a question
for after this lands, not a step in it.

**Proves:** the whole seam, in the shape that motivated it — a review that reads its own previous
findings, writes new ones as addressable facts, and gets faster because it stops re-reading what
it already knows.

### Keying a finding

The `key:` a fan-out declares is the whole of a finding's identity, so it has to be stable under
the edits people actually make. For a comment attached to a declaration, the language has already
solved this: **the package-qualified symbol name**. `entity.nodeToPayload`,
`source/github.Reflect`, `entity.(*LogdStore).Get`. It survives the comment being rewritten (which
is what resolving a finding IS), it survives the declaration moving between files in its package,
and it is unambiguous across packages sharing a base name when the path is repo-relative. A
line number is wrong by the next commit; a `<path>#<name>` key is wrong the first time somebody
splits a file.

The agent can compute it from what it is already reading — package clause, module path — and can
check it, since `go` is on that environment's PATH and `go doc ./entity nodeToPayload` either
resolves or does not. Cheap verification of a key is worth more here than anywhere else, because
a wrong key is not a wrong answer: it is a second entity that looks like a first.

**Fan out per SYMBOL, not per finding.** One entity per symbol, holding the findings outstanding
against it. That is what makes the sub-symbol case — a comment on a loop, on a branch, on a
`switch` arm — need no new identity at all: it is a finding inside the symbol's entity, located
by its own content. Keying those individually would mean either a positional id (`#3`), which
renumbers everything below it the moment somebody inserts a comment, or a hash of the comment
text, whose identity changes exactly when the finding is fixed. The residual already rejected
positional ids for the first reason; the second is worse, because it makes a resolution
unrecordable against the thing it resolved.

If per-comment addressing turns out to be wanted, the extension anchors to **what the fix does
not change** — the code the comment sits on, not the comment — and it should be designed then,
against a case somebody hit, rather than guessed at now.

One consequence to carry into step 3's test: `entity.(*LogdStore).Get` flattens through
`safeSeg`, so the address is not the name. The readable symbol has to be a FIELD on the derived
payload as well, the way `flow` is published rather than being only an id — or `verse entity`
lists a ledger nobody can read.

## What this decides

[derive-residual.md](derive-residual.md) leaves three questions open. A plan that did not answer
them would not be implementable, so:

**Question 1 — declared key or hash of the element?** Declared (`key: $item.ref`). A hash makes
identity change when any field changes, so correcting a typo in a finding retires one entity and
creates another, orphaning everything bound to the first. Declaring it fails loudly for an answer
whose elements have no stable field, which is the better failure and is caught at the first
firing.

**Question 2 — a tag, reserved keys, or a sibling `deriveEach:` field?** The tag. `derives()`
reads two levels of address and treats the rest as payload; a tag is how that reader is told to
read one subtree differently, and it is the only spelling that cannot collide with a payload
whose author happened to use `over` as a field name. The cost is JSON, which has no tags and so
cannot spell a fan-out — the same cost `!get-path` already pays.

**Question 3 — staleness.** Append-only, per step 4.

Landing this rewrites that residual or retires it; it does not leave both standing.

## Not in this plan

- **A sweep**, or any derive that removes. It re-opens the ordering argument from the other end,
  since a delete is not idempotent in the way a write is, and it must not be smuggled in under a
  loop.
- **`derives: [X1 X2 …]`** — a list of derive clauses. Real, cheap, and a different thing: N is
  known when the rule is written, where this is "the answer contained twelve".
- **`count: N` on the matching side.** It makes match evaluation collection-scoped where it is
  per-document, and leaves `$subject` denoting one of them, all of them, or the set.
- **Anything about the absence latch** ([verse-dev-comment-drift.md](verse-dev-comment-drift.md)
  finding 10). It is the other thing standing between a charter and a ledger, it is a contract
  change, and it wants its own plan.
