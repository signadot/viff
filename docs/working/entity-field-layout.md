# A field layout for entity payloads

Status: **proposal**, nothing built. It comes out of two defects that are live today, and a
survey of what 202 real entities on staging actually hold.

The change in one line: **an entity's payload gets an envelope — what a writer said, what verse
derived, and what is true about the entity rather than about its content — because the flat
payload cannot hold the second without destroying the first.**

## The two defects that force it

**A reserved key inside the payload destroys a payload that is not an object.** Verse spells
its own facts as `verse.meta.*` and `verse.charter.*` — keys carved out of the author's
document. An entity may hold any tony value since `payload: 42` became legal, and the two do not
compose: writing the lifecycle marker onto one replaces it.

| payload | after `VanishPatch` | |
|---|---|---|
| `{stage: open}` | `{stage: open, verse: {…}}` | fine |
| `42` | `{verse: {…}}` | **the counter is gone** |
| `"open"`, `[a]`, `null` | `{verse: {…}}` | **gone** |

No error, no refusal — the write is a patch at `verse.meta.vanishing`, and patching a field into
a scalar replaces the scalar. Every reaper, every `derivedFrom` linkage and every per-rule
progress write does this to any entity whose payload is not a record.

**Authored and derived are already mixed flat, and the workarounds are already in the tree.**
`sources:git:verse` holds `{name, owns, system, url}` — typed by whoever ran `verse source add`
— beside `lastReflect`, which verse rewrites every pass. One namespace, two writers, no way to
say which is which. `versed`'s `stillInstalled` exists because of it: it asks whether `name` is
present rather than whether the entity is, because a status write must not resurrect a
registration somebody removed.

## What the data looks like

Four provenances, from the staging survey (`GET /state`, 202 entities, 11 systems):

| | example | payload |
|---|---|---|
| **reflected** — a source mirrors external truth | `git:commit:*` (66), `git-issue:issue:*` (29) | `author, msg, sha, repo` |
| **authored** — a person or a charter wrote it | `trigger:rule:*` (8), `sources:*` (3) | `spec` / `name, owns, url` |
| **derived** — verse computed it | `task:run:*` (8), `motion:channel:*` | `chain, status, started, cursor` |
| **mixed** — content plus verse's own | `drift:finding:*` | `at, says, symbol` + `verse.meta.derivedFrom` |

Two of those already split cleanly and were built that way on purpose: a rule is `{spec: !raw
<doc>}` (`charter.go`), and a derived entity carries `verse.meta.derivedFrom` beside the content
its charter wrote. The proposal is to stop having two mechanisms for one idea, and to make the
one that survives work for a scalar.

## What verse does not need from k8s

**Identity is the address, not a field.** `system:kind:id` names the entity, so there is no
`metadata.name`, no `namespace`, no `kind:` in the payload — k8s carries those because its
addressing is weak. Verse's is not, and `verse state demo.pr.1` is the proof: the address is
outside the document and the document is the value.

**No `apiVersion`.** One schema, served by the verse itself (`/schema`), versioned by the
binary rather than by each record.

**A spec may be a scalar.** k8s specs are always objects. `count:n:hits = 42` is a counter, and
the whole point of the payload widening was that it is expressible.

**Status has no schema.** k8s status is a declared type per kind. Here it is open, which is what
"looser" mostly means in practice.

## The layout

TWO reserved containers, and every other reserved name a sibling of them:

```tony
value:        # what a writer said — ANY tony value: a record, 42, "open", [a, b], null
status:       # what verse derived ABOUT it — open, verse-owned, never authored

vanishing:    # …and the named facts, each one thing, at the top level
derivedFrom:  # {rule, subject}
source:       # the registration that owns this slice
```

**`value`, not `spec`, and the reason is the grammar we already have.** A charter's subject
predicate is spelled `value:` today — `subject: {system: demo, kind: pr, value: {stage: open}}`
— so naming the authored half `value` makes the predicate and the storage agree, and every
charter ever written keeps meaning what it meant. `spec` is also wrong for most of this data:
two thirds of staging is reflected fact, and a commit's sha is not a desired state anybody
specified. (`spec` survives as the word for a DECLARATION's value — a rule, a stratum, a goal —
if we decide the distinction is worth two words. I do not think it is.)

**`status` is verse's.** `lastReflect` on a source, a task's `started`/`ended`/`cursor`, a
motion's `tally`, and the per-rule bookkeeping now at `verse.charter.<rule>`. The rule that
makes it useful is the one k8s has: a writer of `value` never writes `status`, and verse never
writes `value`. That is what lets a re-registration stop worrying about clobbering a timestamp,
and `stillInstalled` go away.

**There is no `meta`, and the rule is what a name has to hold.** A container earns its place by
holding a set nobody can enumerate — `value` is any document, `status` grows per kind, and
`charter` grows a key per rule, so it folds under `status` as `status.charter.<rule>`. A single
fact with one meaning does not: `vanishing` is an instant, `derivedFrom` is a pair, `source` is
a ref. Grouping those under `meta` is a k8s import that buys a level of nesting and answers no
question — `verse state demo.pr.1.vanishing` is the address, and `.meta.` in the middle of it is
a word nobody needed to type.

It is also what the tree already found out the hard way and half-applied. Today's reserved
namespace is `verse.meta.*` beside `verse.charter.*`, and `entity/lifecycle.go` split those two
because "`charter` grows a key per rule, `meta` grows a field per lifecycle concern" — the same
distinction, one level down and with a `verse.` prefix that only existed because the author's
document was at the root. The envelope removes the reason for the prefix; the rule above removes
the reason for `meta`.

**Nothing collides, because the top level is verse's.** A writer never addresses it: `verse
state put demo:pr:1 '{stage: open}'` means the VALUE, and the write surface envelopes it. That is
what makes a new reserved sibling a cheap addition later rather than a break — there is nothing
of the author's up there to collide with.

Nothing is mandatory either. An entity nobody authored has no `value`; one verse has never
touched has no `status`; most have neither `vanishing` nor `derivedFrom`. The envelope is a
reservation, not a schema.

**Why not comments or tags instead of an envelope?** They are the obvious alternative, since
both attach to a node without being a field of it — which is exactly what a scalar payload
needs, and exactly what the envelope is for. Both are the AUTHOR's channels, and that is what
rules them out:

- A comment is prose somebody wrote, and the store now counts it: `SameState` compares with
  comments, so verse writing one is a commit and a change event. Machine facts do not belong in
  a channel where a human's note is the ordinary content.
- A tag is worse, because tags COMPOSE. A payload's own tag is already meaningful — a goal's
  `doneWhen: !fact {…}` is stored tagged and read back tagged — and `entity.AsData` exists
  because patching a tagged value onto a tagged one accumulated them (`!fact.fact.fact.fact`,
  observed). Verse adding `!vanishing` to an author's tagged value would change what the value
  means.

So the carriers a node offers besides fields are both spoken for, and the envelope is what is
left. It is also the only one of the three that can hold structure: `derivedFrom: {rule,
subject}` is a record, and a comment is lines of text.

**What it costs**, since it is a real loss and not free: there is no longer one path that
answers "everything verse knows *about* this entity". `demo.pr.1.meta` would have been that, and
a kpath cannot say "everything except `value`". Whether that question gets asked often enough to
be worth a level on every address is the trade, and I do not think it does.

## Virtual fields

`git-issue:repo:verse` holds `{issues: 29, open: 10, closed: 19}` — three numbers the source
recomputes and rewrites every pass, each rewrite a commit, each commit a change event every
watcher of that slice pays for. `motion:channel:*` stores a `tally` the engine rewrites per
ballot. Nothing else in the verse would disagree if either were computed when somebody asked.

**They are computed by Go here, not by a tony operation, and the vocabulary is the proof.** The
whole operator set is `addtag all and arraydiff at comment delete dive embed field glob has-path
if insert irtype json-patch key let not nullify or pass pipe quote raw rename replace retag rmtag
strdiff subtree tag unquote` — there is no count and no length. `!list-paths` selects entities;
it cannot answer `10`. Every case worth having is an aggregation over a selection rather than the
selection itself:

| wanted | needs |
|---|---|
| `git-issue:repo.status.open` | counting a selection |
| `git-issue:repo.status.issues` | counting another |
| `motion:channel.status.tally` | the quorum/select discipline, which is engine logic |

All three happen to be pure functions of the store at a revision — but that is a fact about
those three, not about virtual fields, and the difference is the whole of what has to be
declared. A virtual field is arbitrary Go. Nothing stops one answering the current eclipse
fraction of the sun over Grenoble, and nothing in verse could tell that it had.

So **purity is declared per field, and assumed absent otherwise**, because the two answer
differently to the two questions the store already asks:

- **Pure of the store at a revision** — a count, a tally. `AsOf(rev)` answers what a live read
  at that revision answered, a replay agrees with what it replays, and a `subject` may match on
  it without the edge rule `¬on(C-1) ∧ on(C)` losing its meaning.
- **Anything else** — the eclipse, a clock, a network call. Read-only decoration. It must not
  enter a match, and `AsOf` cannot answer it faithfully at a past revision at all: a snapshot
  promises that no later write is visible to any read of it, and a field that never reads the
  store is not bound by that promise. Either the snapshot refuses such a field or it admits it
  is answering as of now — and which of those it is belongs in this note before anything is
  built.

**Time is one instance of the second kind, not the category.** The clock is not designed, so it
does not get to constrain this; it will land in whichever bucket it lands in, and the buckets
are here for the eclipse anyway.

So a virtual field is Go, registered against a `(system, kind)`, answering at read. That weakens
the case for a kind-level ENTITY rather than making it: code needs no place in the store to be
declared. What a kind entity would still buy is discoverability — somewhere for `verse state
git-issue.repo` to say which of its fields are computed and which are held — and later an
`accept:` schema. Worth wanting, not required by this.

**Cost control.** A virtual field over a whole kind is a sweep per read, and `verse state`
answers the whole verse — so if every kind carries one, a bare read becomes N sweeps. Whatever
the answer is (evaluate only under the path asked for, cache by revision, refuse one above some
depth), it has to be decided before this is offered rather than after.

## Comments on a declaration — done, and what it took

A rule keeps the comments its author wrote now, and the fork this section used to describe is
closed: `persist` still writes the NORMALIZED record (`t.ToTonyIR()`), and the comments ride
along in it. Storing the document as authored was the other route and is not needed.

`//tony:schemagen=verse-trigger,notag,comment=Doc,lineComment=Note` is the whole of the
declaration; go-tony v0.0.142 built the encode half of those annotations and v0.0.143 finished
the codec. Two things in verse had to move with it, and both were the same shape — code that
asked a node what it was without stepping past a comment first:

- `codec.Unwrap` in `charterDefs` was applied to the value being INSTALLED, not just to the one
  being inspected, so the CLI threw the rule's comment away before it ever reached the wire. It
  was invisible for as long as nothing parsed comments and lossy the moment something did.
- `entity.Get` unwrapped its ANSWER as well as its receiver, so `ruleOf` — reading `spec` back
  to rebuild a rule at startup — got the value with the wrapper already gone. The rule now is
  that navigation sees through a comment and the value it arrives at keeps its own.

WHERE A COMMENT LANDS is positional, and worth knowing before writing a charter, because one of
the three spellings does not do what it looks like:

| written | belongs to |
|---|---|
| anything before a container's first item | the CONTAINER — and blank lines do not separate, so a file header and a comment meant for the first rule merge into one |
| a comment between items | the item that FOLLOWS it — so every item but the first can take one |
| `- # …` then the fields | that item |

So in a charter file the first rule cannot take a head comment the ordinary way — there is no
position between "the list starts" and "the first rule" except inside the item, and that
spelling works. Every later rule takes one normally (go-tony v0.0.144). A rule written as its
own `---` document takes one too.

Nested comments are a separate matter, and nothing about them is broken — every position
decodes, and a head-commented patch still fires and applies, because `tony.Patch` sees through
the wrapper. They are only LOST, and the rule for which is simple: **a comment attaches to a
value, and it survives iff whatever that value decodes into can carry it.**

```
# 1 above the rule          survives — Trigger declares comment=Doc
action:
  patch:
    # 4 above the patch     survives — Action.Patch is a raw node, so the wrapper rides inside it
    stage: x
condition:
  subject: demo:pr          2, above the subject, is dropped
name: r                     3, above the action, is dropped
```

So the remaining work is two piles of very different size. The GENERATED types (`Action`,
`Gate`, `Gating`) need one annotation each and a regenerate. The CUSTOM-codec ones
(`Condition`, `Match`, `Formula`, `Requirement`, `Stratum`) cannot use the annotation at all —
codegen writes no codec for them, so their hand-written `FromTonyIR`/`ToTonyIR` would each have
to capture `node.Lines` and rebuild the wrapper themselves. For `Match` that is probably not
worth it: its commonest spelling is the one-line string `demo:pr:*`, which has nowhere to hang a
head comment.

## What breaks## What breaks

Everything reads one level deeper — but exactly one, which is the point of collapsing `meta`.
`verse state demo.pr.1` answers `{value: …, status: …}` rather than the payload;
`verse state demo.pr.1.value.stage` is the field, and `demo.pr.1.vanishing` is the marker that
is three levels deep today (`verse.meta.vanishing`) and one after this. A charter's
`value: {…}` predicate keeps working by construction — that is why the name was chosen — but
`--if '{stage: open}'` roots at the payload today and would need to root at `value`, which is a
default to change in one place (`Pattern.At`).

The migration is a rewrite of every payload, so it wants the same wipe-and-reseed
[the state branch already needs](../../scripts/staging-reseed.sh) rather than a second one.

## Open questions

- Is `vanishing` really a sibling rather than `status`? The argument for the top level is that
  it is an INTENT somebody wrote, not something verse observed — the reason k8s keeps
  `deletionTimestamp` out of status too. The argument against is that it makes the top level
  hold one authored thing when its whole justification is that it is verse's.
- Reflected entities: is the mirrored truth `value`, with `source` naming the owner? That reads
  right, and it makes "who is the single writer of this slice" a fact on the record rather than
  a convention held in `source/own.go`.
- Do declarations keep `spec`, or fold into `value`? Two words for one idea is the cost; "the
  rule as installed" is clearer than "the rule's value" is the benefit.
- Is `status.charter.<rule>` the right name once it is inside `status`? `charter` named the
  thing it came from; `status.rules.<name>` names what it holds. A rename is free while nothing
  reads it under the new layout.
- Does a snapshot refuse an impure virtual field, or answer it as of now and say so? `AsOf`'s
  promise is that no later write is visible to any read of it, and a field that does not read
  the store cannot keep that promise either way.
- Should a virtual field be visible as such? A reader who cannot tell a held count from a
  computed one is the goal at the read, and the opposite of it when they are deciding whether
  writing to it will stick. `status` being verse-owned means nobody should be writing there at
  all, which may make the question moot.
