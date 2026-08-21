# Sources

The surface. For what a source *is* and why it is shaped this way, see
[../sources.md](../sources.md). To build one, copy
[examples/source](examples/source/main.go).

A source is an entity at `verse.sources.<system>:<name>`:

| | |
|---|---|
| `system` | the slice this source reflects and is the single writer of (`git`, `github`, …). Required |
| `name` | its name within that system. Required |
| `url` | verse dials this base for the reflection contract |
| `dir` | a local git repository, reflected in task — no network |
| `dial-in` | the source dials verse. Designed, refused at install until it is built |
| `owns` | the places this source claims to be the single writer of (see below) |
| `scope` | the copy-on-write scope it reflects into; empty is baseline |
| `lastReflect` | when verse last *wrote* from this source. Written by verse |
| `lastError` | the last failure, cleared by the next success. Written by verse |

**Exactly one of `url`/`dir`/`dial-in`.** They differ only in where the connection comes from;
`Reflect` is the same call down all three. Unlike a runner, absence means nothing here — the
field present says which it is.

The system is what the address is keyed on (`verse.sources.<system>:<name>`, under the
reserved root like everything verse keeps about its own running) because it is a question verse
already asks: who writes `git:*`. Neither it nor `name` may contain `/`, and `system` may not
contain `:` — both are segments of the address and of the route that serves them.

A source reflects the world's half and only that: a reflection naming an address under `verse.`
is refused, a claim that names it is refused at install, and no claim reaches there however wide
it is written — `*.*.*` claims every three-segment entity the world is about and none of verse's
own.

## Ownership

`owns` is a list of claims, and it is what an install proves disjointness against and what a
`--forget` removal may tombstone. An ownership claim is an **address**, not a payload predicate,
because an install compares declarations with no entities in hand.

```tony
{at: "git.ref.verse/main"}     one entity
{at: "git.*.verse/*"}          a level may be `*`, and may END in one — a literal prefix
{at: "git"}                    a whole subtree: everything under it, at every depth
{at: "verse-dev.drift"}        …which is how a source that keeps two levels of its own says so
{at: "linear.project.*", path: [owners, ENG]}   …at a place INSIDE the payload
```

A claim is **one pattern per level, at whatever depth** — the same shape an address is. It is
not three fixed dimensions: a claim shorter than an entity owns everything under it, and one
longer than three owns a place a package keeps its own tree at.

`at` is the path inside the payload the claim is about, the same address a write takes
(`entity.Path`). It is what lets two sources share an entity without either being a second
writer of anything: one is the single writer of `owners.ENG`, the other of `owners.OPS`.
Sharing is not an exception to single-writership; it is single-writership at a deeper address.
Absent — which is every claim the connectors make but Linear's on a project — the claim is to
the whole entity.

Two claims are **disjoint** when any level is: two literal names that differ, or two prefixes
neither of which prefixes the other. Where neither parts company first, the shorter claim
contains the longer — `git` overlaps `git.ref.x` rather than being disjoint from it — which is
the same rule the payload path already followed, asked once of the address and once inside the
payload. A `*` level is disjoint from nothing, so `git.*.*` can be proved apart from nothing in
its system. A `*` anywhere but the end of a level is a parse error, not an approximation.
Levels and paths both compare **segment-wise**, so `owners.ENG` and `owners.ENGINE` are
disjoint, while `owners` contains `owners.ENG` and an absent path contains every path there is.

Git claims **two entries per repo**, because refs and commits are `<repo>/<name>` while a head
is the bare `<repo>`:

```tony
owns: [{at: "git.*.verse/*"}, {at: "git.head.verse"}]
```

The single glob `verse*` would cover both and also claim `verse-extras/main` — a real
overclaim, and one that would make two disjoint repos refuse to install alongside each other.

**A claim is a document rather than a string**, and the ids are what forced it: `@` — the
separator a terse `git.*."verse/*@owners.ENG"` would need — already appears inside ids (a task's
is `<chain>@<subject>`), so nothing could parse one back. A grammar with two forms would have to
be read twice everywhere, so there is one.

An install with no `owns` takes what the probe reported. A registration carrying none can be
proved apart from nothing, so it is refused beside anything else in its system.

## Commands

```
verse source                                what is installed, with lastReflect/lastError
        [--probe]                            …and what each one says right now (not recorded)
verse source add <system>:<name> <url>       install one, probing the url first
       add --dir <d> <system>:<name>         …or a local git repo, reflected in task
       add [--file <f>] | [file] | stdin     …or as a document (.tony/.json/.yaml)
           [--no-probe]                      install without asking it what it is
           [--allow-overlap]                 install though another source may write the same place
       show <ref>                            the entity
       check <ref>                           ask it what it is and what it claims, right now
       remove <ref>                          take it away; refused if it reflected anything
              [--forget]                     …the reflection is over: tombstone what it wrote
              [--keep]                       …the truth is moving: leave the last known state
```

A `<ref>` is `verse.sources.git.laptop` or `git:laptop` — the whole address or the bare pair;
the middle form (`sources.git.laptop`) is not accepted, since that was the whole address before
verse's own half of the tree had a name.

`verse status` names the installed sources — refs only, health left to `verse source`. It is
there because it is the one line of a status that says anything is *arriving*: every counter
beside it moves on what verse was already given, so a verse whose last source was removed reads
exactly like a healthy quiet one until that list is empty.

Every command takes the global `--scope`, and a scoped source is installed, listed, and removed
in the scope it reflects into.

## Routes

| | |
|---|---|
| `GET /sources` | list; `?probe=true` adds a live `reach` per source |
| `POST /sources` | install; `?probe=false` skips the check, `?allow-overlap=true` the disjointness proof. `201` with `{ref, reach}`, plus `owns` when taken from the probe and `warning` when unreachable |
| `GET /sources/{system}/{name}` | the entity |
| `GET /sources/{system}/{name}/check` | `{ref, reach}`, plus `says: {system, owns}` when it answered |
| `DELETE /sources/{system}/{name}` | remove. `?forget=true` or `?keep=true`; `409` with both named when it reflected something and neither is given |
| `POST /sources/{system}/{name}/hint` | `?ref=` (repeatable) — reconcile those slices; none means a full resync. `204` |

`reach` is one of `a source`, `not a source`, `unreachable`, and nothing about it is written to
the store. A `500` from a source is *unreachable* rather than *not a source*: something is
speaking HTTP and failing, which is a source having a bad day at least as often as it is the
wrong url, and refusing an install on it would refuse a fact that fixes itself.

The listing reports `lastPass` from the live reflector — when a pass last *completed*, whatever
it did — and `running: false` for a registration with no reflector. Neither is stored.

**A hint carries no data.** It names which slice went stale and nothing else, which is what lets
the route stay unauthenticated in a wide-open dogfood verse: a delivery that is duplicated,
reordered or forged costs at most a reconcile of a slice its sender does not choose the contents
of. A malformed ref is `400` rather than a silent full resync, and a hint at a registration that
is installed but not running is `503` rather than a quiet `204` — a swallowed hint looks exactly
like one that worked.

## The contract a source serves

Two routes, no state, no write face:

| | |
|---|---|
| `GET /source` | `{system, owns}` — what this program is. Has no effect, which is what makes it the probe |
| `GET /reflect` | `?ref=<ref>` (absent = everything it owns) → a `Reflection` |

```tony
covers:
- {path: [git, ref, *], value: {repo: verse}}
- {path: [git, head, *], value: {repo: verse}}
entities:
- at: git.ref.verse/main
  payload: {repo: verse, name: main, sha: "82bd107…", type: commit}
- at: git.head.verse
  payload: {repo: verse, ref: main, sha: "82bd107…", dirty: false}
```

`covers` is what the answer is **complete for**: an entity under a covered pattern and absent
from `entities` is tombstoned. An entity under no cover is written and never tombstoned — which
is how git keeps history, since it covers `ref` and `head` and not `commit`.

A cover may also carry a **`slot`** — a path inside the payload — saying the answer is complete
for the value at that path in each entity it matches, and for nothing else there. converge then
writes, compares and tombstones at that path: two sources can be the single writers of two slots
of one entity, each proved apart by its `owns` (`at:`), neither able to reach the other. When the
last slot goes, so does the entity — a conditional delete, the only contended write in the
design.

Covering an entity both whole and by a slot is **refused**: one says the answer is all of it, the
other that it is one place in it, and both readings destroy something.

**A slot is not a narrowing.** It says where an answer is authoritative, not which entities it is
about, so a slot cover with no predicate claims its path in *every* entity of that kind. Give it
the predicate that selects the entities carrying that slot — `{owners: {ENG: null}}`, a null in a
match being a presence test — or the pass writes a tombstone into a place nobody asked about,
once per entity per pass. See [working/shared-ownership.md](../working/shared-ownership.md).

A cover is an `entity.Pattern` — the same selector a charter's `when` becomes — so the payload
predicate is spelled `value`, as it is in a charter. On the wire each cover also carries an
empty `op: ""`, which means nothing here and is upstream's doing: codegen honours `omitzero` on
every field of that struct except the one whose type implements `MarshalText`
(go-tony `f69agjyeh12ksa25cnn0` item 19). Ignore it; a cover has no op, and verse refuses one
that carries a non-empty value.

**A failed reflect must fail loudly**: `500`, never `200` with an empty body. An empty answer
carrying covers is an instruction to tombstone everything under them, and verse will believe it.

## The reflector

One per installed registration, started at install, stopped at removal, restored at restart
after the charter — a reflection is a write triggers react to, and a level-triggered pass does
not repeat itself, so a rule restored late misses what it missed for good.

Each runs an initial full pass, a periodic full pass every `versed.ResyncEvery` (one minute),
and a targeted pass per hint.

Scoped registrations are **not** restored on restart: `entity.Store` has no way to enumerate
scopes, and a scoped store reads baseline layered underneath, so nothing can find them. It is
the gap scoped charters have — a scope is created on demand by whatever owns it, so the thing
that made the scope can install into it again.
