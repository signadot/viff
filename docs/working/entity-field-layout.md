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

```tony
value:    # what a writer said — ANY tony value: a record, 42, "open", [a, b], null
status:   # what verse derived ABOUT it — open, verse-owned, never authored
meta:     # facts about the ENTITY rather than about its content
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

**`meta` is about existence, not content.** Provenance (`derivedFrom: {rule, subject}`, and the
source that owns the slice), lifecycle intent (`vanishing`), and the timestamps of the record
itself. Lifecycle sits here rather than in `status` because it is an INTENT somebody wrote, not
something verse observed — the same reason k8s puts `deletionTimestamp` in metadata.

Nothing is mandatory. An entity nobody authored has no `value`; one verse has never touched has
no `status`; most have no `meta`. The envelope is a reservation, not a schema.

## Virtual fields

`git-issue:repo:verse` holds `{issues: 29, open: 10, closed: 19}` — three numbers the source
recomputes and rewrites every pass, each rewrite a commit, each commit a change event every
watcher of that slice pays for. `motion:channel:*` stores a `tally` the engine rewrites per
ballot. Nothing else in the verse would disagree if either were computed when somebody asked.

**They are computed by Go here, not by a tony operation, and the vocabulary is the proof.** The
whole operator set is `addtag all and arraydiff at comment delete dive embed field glob has-path
if insert irtype json-patch key let not nullify or pass pipe quote raw rename replace retag rmtag
strdiff subtree tag unquote` — there is no count, no length, and no clock. `!list-paths` selects
entities; it cannot answer `10`. So a declared derivation covers the one case that is pure
selection and none of the cases actually worth having:

| wanted | needs | expressible as a match? |
|---|---|---|
| `git-issue:repo.status.open` | counting a selection | no — nothing counts |
| `task:instance.status.elapsed` | a clock | no, and see below |
| `gate:question.status.overdue` | a clock, against a stored `escalatesAt` | no |
| `motion:channel.status.tally` | the quorum/select discipline | no — that is engine logic |

**A clock cannot be a MATCH operator, and that is not an omission.** A match is evaluated at a
REVISION — `AsOf`, and the edge rule `¬on(C-1) ∧ on(C)` — so it has to be a pure function of
store state, or a replayed commit reaches a different verdict than the live one did.

Verse already puts time exactly where it survives that: a formula is temporal and a `deadline`
leaf reads the system clock (`formula.go` — "a time derivation, not a store fact", with no
subtree to watch). So the split this needs is the split the trigger model already made, one
layer down.

Which draws the line the layout needs, and it is per field rather than per mechanism:

- **Pure of the store at a revision** — a count, an aggregate, a tally. Computable at any
  revision, so a `subject` may match on it and a replay agrees.
- **Not pure** — anything reading a clock. It may be rendered, and it may be a formula's
  business (that is what `deadline` is), but it must never enter a match, or the edge model
  quietly stops being replayable.

So a virtual field is Go, registered against a `(system, kind)`, answering at read. That weakens
the case for a kind-level ENTITY rather than making it: code needs no place in the store to be
declared. What a kind entity would still buy is discoverability — somewhere for `verse state
git-issue.repo` to say which of its fields are computed and which are held — and later an
`accept:` schema. Worth wanting, not required by this.

**Cost control.** A virtual field over a whole kind is a sweep per read, and `verse state`
answers the whole verse — so if every kind carries one, a bare read becomes N sweeps. Whatever
the answer is (evaluate only under the path asked for, cache by revision, refuse one above some
depth), it has to be decided before this is offered rather than after.

## What breaks

Everything reads one level deeper. `verse state demo.pr.1` answers `{value: …, status: …}`
rather than the payload; `verse state demo.pr.1.value.stage` is the field. A charter's
`value: {…}` predicate keeps working by construction — that is why the name was chosen — but
`--if '{stage: open}'` roots at the payload today and would need to root at `value`, which is a
default to change in one place (`Pattern.At`).

The migration is a rewrite of every payload, so it wants the same wipe-and-reseed
[the state branch already needs](../../scripts/staging-reseed.sh) rather than a second one.

## Open questions

- Does `meta` earn a place separate from `status`, or is provenance just derived state? The
  argument for splitting is that `vanishing` is written by an author and everything in `status`
  is not.
- Reflected entities: is the mirrored truth `value`, with `meta.source` naming the owner? That
  reads right, and it makes "who is the single writer of this slice" a fact on the record rather
  than a convention held in `source/own.go`.
- Do declarations keep `spec`, or fold into `value`? Two words for one idea is the cost; "the
  rule as installed" is clearer than "the rule's value" is the benefit.
- What happens to `verse.charter.<rule>`, which is keyed by rule name and grows differently from
  everything else in `status`? `status.charter.<rule>` keeps the shape and moves the root.
- Should a virtual field be visible as such? A reader who cannot tell a held count from a
  computed one is the goal at the read, and the opposite of it when they are deciding whether
  writing to it will stick. `status` being verse-owned means nobody should be writing there at
  all, which may make the question moot.
