# External sources: implementation plan

Status: plan. Steps 1 and 2 of the design note are built; this is how step 3 lands. The design
is [sources.md](sources.md) — read that first, since this file does not restate its reasoning,
only the order of work and what each step must prove.

The shape being built: a registration at `sources:<system>:<name>` carrying exactly one of
`url`/`dial-in`/`dir`, probed at install and checked for provable ownership disjointness, with
a `Reflector` per registration under the server's lifetime, a hint route, a removal fork, and a
scope.

**This is a build, not a re-seam.** The note was written against a `sources` worktree that is
not in this repo under any ref (the note records what searching for it established), so the
registration, the restart, the durability and the tombstoning have no prior code to inherit.
That is what makes step 3 roughly the size of steps 1 and 2 together, and why it is decomposed
here rather than taken in one pass.

## Three decisions this plan forces back into the note

**An ownership claim is a ref glob, not an `entity.Pattern`.** The note proposed
`{system: git, value: {repo: verse}}`, reusing the charter's selector so ownership needs no
second vocabulary. Writing the prover is what showed why that does not work: converge evaluates
`Covers` against stored entities whose payloads it can read, while an install compares two
declarations with nothing in hand. Addresses compare statically; payload predicates do not — and
proving predicates apart syntactically has traps (a null in a predicate is a PRESENCE test, so
"same field, different constants" would prove `{repo: null}` disjoint from `{repo: verse}` when
the second entails the first). So `owns` is a ref with `*` allowed — `git:*:verse/*` — with id
globs restricted to a literal prefix and an optional trailing `*`, which makes disjointness a
string comparison rather than automata intersection. `Covers` stays a `Pattern`; the note now
carries the full argument and the direction this is wrong in.

**`lastReflect` cannot be both things.** The note asks it to be "a fact about verse's own last
write … the poll cursor" *and* the signal that catches "a source that has wedged — alive,
answering, reflecting nothing new". Those are different clocks. A cursor moves only when
something changed, so on a quiet repo a healthy source and a wedged one carry the same
`lastReflect`; a stamp that moves every pass catches the wedge but writes to `sources:*` once
per poll interval, and anything watching that slice then fires forever on a verse where nothing
is happening. So it splits, along the line runners already drew:

- **Stored** on the registration: `lastReflect` (when verse last WROTE from this source) and
  `lastError` (the last failure, cleared by the next success). Both are facts about verse's own
  actions and both change rarely — no churn.
- **Reported, never stored**: `lastPass` on `GET /sources`, read from the live reflector — when
  a pass last completed, whether or not it changed anything. That is an opinion about *now*,
  which is exactly what the runner rule says must not be written down.

**A hint carries its refs as query parameters, not a body.** The note writes `POST
/sources/<name>/hint {refs: […]}`. The sender is a `/bin/sh` git hook running `curl`, the law
says the hint carries no data, and a handful of refs fit in a URL — so `?ref=` (repeatable) is
the form that keeps the sample hook a single line and needs no content-type negotiation on a
route that must stay cheap enough to leave unauthenticated.

## Order of work

Six steps. Each is independently reviewable and leaves the tree green. The dependency order is
real: 2 needs 1's entity and its prover, 3 needs 2's registration to have something to run, and
5 needs 3's reflector to have something to stop. Steps 4, 5 and 6 are independent of each other.

### 1. The registration entity, and the name it needs

New `source/registration.go`, modelled on `runner/entity.go` throughout — same shape, same
reasons, and where it differs the difference should be stated in the code.

First a rename with no behaviour: `source.System` is already `"local"`
(`source/federation.go`, the `verse connect` federation) and the registration wants that name.
It becomes `source.FederationSystem`; three call sites (`versed/server.go` ×2,
`cmd/verse/connect.go`). Then `System = "sources"` and `Ref(system, name) entity.Ref`.

```go
type Entity struct {
    Name   string   // half the address
    System string   // the other half, and the slice reflected
    URL    string   // exactly one of these three
    DialIn string
    Dir    string
    Owns   []string // ref globs: ["git:*:verse/*", "git:head:verse"]
    Scope  string
}
```

Ownership and its prover land here too, in `source/own.go` — a pure function with its own test
and no wiring, so it can be read on its own:

```go
// Claim is one ownership claim, parsed from its ref-glob spelling `<system>:<kind>:<id>`.
// A segment may be `*`; an id may additionally be a literal prefix with a trailing `*`, and
// nothing richer — prefix-disjointness is a string comparison, where general glob
// disjointness is automata intersection.
type Claim struct{ System, Kind, ID string; Prefix bool }

func ParseClaim(s string) (Claim, error)
func (c Claim) Disjoint(o Claim) bool
// Disjoint reports whether every claim of a is provably disjoint from every claim of b.
func Disjoint(a, b []string) (bool, error)
```

Two claims are disjoint when **any segment is**: two literal systems that differ, two literal
kinds that differ, or two id prefixes neither of which prefixes the other. A `*` segment is
disjoint from nothing.

`Descriptor.Owns` changes from `[]entity.Pattern` to `[]string` with it — step 2 of the note
built it as patterns, and this is where that decision is undone. `Git.Owns()` returns **two
entries per repo**, `git:*:<id>/*` and `git:head:<id>`, because refs and commits are
`<repo>/<name>` while a head is the bare `<repo>`. The tempting single `<id>*` covers both and
also claims `<id>-extras/main`, which would make two disjoint repos refuse to install together.

`Check()` covers what cannot be discovered later: a name or system missing, a `:` or `/` in
either (both are route segments), a url that does not parse or has no http scheme, and —
the one with no runner analogue — **exactly one of url/dial-in/dir**. Deliberately not the
runner's "a runner with no url is refused": there, absence already meant `local`, so a url-less
entity was ambiguous; here the field present says which it is.

`CheckFields(node)` names a field the decoder would drop, read as a document before decoding.
The `ulr:` lesson matters more here than for a runner: this entity has six fields and a dropped
`owns` installs a source with no tombstone authority, reporting success.

`Find` and `List` mirror the runner's, accepting the ref with or without the `sources:` prefix.

**Proves:** the ref round-trips through `entity.ParseRef`; each `Check` refusal fires with a
message that says what to do; two of url/dial-in/dir together is refused and none of them is
refused; `CheckFields` names `onws:` rather than installing a source that claims nothing.

And the prover, as a table: two git repos are disjoint across all four claim pairs (this is the
motivating case and the reason the rule is not just system-or-kind); `verse` and `verse-extras`
are disjoint, the near-miss the two-entries-per-repo shape exists for; `git:*:*` is disjoint
from nothing in git; different systems and different literal kinds are disjoint; a `*` kind
overlaps a literal one; an id glob with `*` anywhere but the end is a parse error, not a
silently-wrong claim. Plus symmetry over the whole table — an install checks one way and means
both.

### 2. Install: the routes, the two checks, and the CLI

New `versed/sources.go` and a route block in `Handler()` beside the runners':

```
GET    /sources                       list
POST   /sources                       install (?probe=false)
GET    /sources/{system}/{name}       show
GET    /sources/{system}/{name}/check probe now
DELETE /sources/{system}/{name}       remove (step 5 adds the fork)
```

`addSource` is `addRunner` with two checks added, and both are the thing an external source
loses that an in-process one had by construction:

- **the descriptor's system must be the one the operator named** — a `github` registration
  pointed at a git source, caught while somebody is present;
- **ownership must be provably disjoint from every standing registration in the same scope**,
  using step 1, or the install is refused. `--allow-overlap` is the way past, with the cost
  stated rather than discovered.

The probe is `source.Probe` from step 2 of the note, and its three dispositions are the
runner's for the runner's reasons — *not a source* refuses, *unreachable* installs with a
warning on the response, because a manifest landing before its pod is the ordinary state of a
source deployed alongside the entity that names it.

`cmd/verse/source.go` mirrors `cmd/verse/runner.go` (`verse sources`, `verse source
add|show|check|remove`), registered in `main.go` beside `runnersCmd(), runnerCmd()`. It lands
here rather than last so that everything after this step is typeable.

**Proves:** the three probe outcomes reach the right dispositions (the `runner_test.go` pattern:
a real `sourced` behind an `httptest` server, a not-a-source, and a closed listener); a system
mismatch is refused naming both; a second registration overlapping the first is refused and
`--allow-overlap` installs it; `verse source add` reaches the route through the client the way a
person types it.

### 3. Lifetime: a reflector per registration, and a restart

`Server` gains a map of registration ref → `{*source.Reflector, cancel}`, guarded by its own
mutex — the `scopeEngs`/`scopeEngine` shape at `versed/server.go:641`, which is the tree's
existing answer to "a long-lived thing owned by an entity".

- `sourceFor(e source.Entity) (source.Source, error)` builds the source the registration names:
  `url` → `source.HTTP(e.System, e.URL, nil)`, `dir` → an in-process `source.Git`, `dial-in` →
  refused until step 4 of the note.
- Started on install, stopped on remove, restored in `Start()` — **after** `LoadStrata`,
  `LoadCharter` and `LoadGoals`. A reflection is a write that triggers react to, so a source
  restored before the charter would deliver its first pass into a verse that has not got its
  rules back.
- `lastReflect`/`lastError` are written here, on the rule decided above: `lastError` on
  transition, `lastReflect` only when a pass wrote something.

One thing this step turned up that the plan did not predict, recorded here because it is a rule
rather than an incident: **every status write needs a still-installed precondition, and it has
to ask about a FIELD.** A write at a field of an absent entity CREATES it, so a pass in flight
when a source is removed resurrects the registration as a stub carrying only a timestamp — and
`entity.Present()` on the ref does not catch it, because that is a null pattern and a tombstone
IS a null value at the entity's own path. Asking whether `name` still stands does catch it.

**Proves:** an added source reflects with no restart (install a `dir` source over a temp repo,
wait for `git:head:*`); a restart brings it back and it reflects again (the `smoke.sh` restart
section is the pattern); removing one stops its goroutine (assert no further writes after a
quiet interval); a failing source records `lastError` and clears it on the next success; a quiet
pass leaves the registration entity byte-identical — the anti-churn assertion, which is the one
that will break if somebody later stamps a heartbeat here.

### 4. The hint, and the end of `Webhook`

`POST /sources/{system}/{name}/hint?ref=…` → `Reflector.Hint(refs...)`, which step 1 of the note
already built. No refs means a full resync. It writes nothing itself, which is what lets it stay
unauthenticated in a wide-open dogfood verse: a delivery carrying no data can cost at most a
reconcile of a slice its sender does not get to choose the contents of.

Then the retirement: `Git.InstallHook` posts there instead of `/hooks/git`, and
`Source.Webhook`, `source.Hinter` and `Reflector.Hooks` are deleted. The `Hinter` shim existed
to keep step 1 green; this is the step that was always going to remove it.

**Proves:** a hint POST reconciles the slice it names and nothing else; a hint that writes an
entity is not expressible (the route has no body); a hint for an unknown registration is 404
rather than a silent no-op; the installed git hook's curl line hits the new route (extend
`TestGitInstallHook`, which already reads the script).

### 5. Removal, and the fork

`removeSource` is `removeRunner` with the nouns changed (`versed/runners.go:158` — same 409,
same two-flag refusal message): a removal whose registration has a **non-empty reflected slice**
is refused until told which of `--forget` (tombstone it; the reflection is over) or `--keep`
(leave the last known state, now frozen and ageing). "Non-empty" is what the registration's
`owns` selects. A removal with nothing reflected needs no flag, there being nothing to be wrong
about.

**Proves:** the refusal, with both flags named in the message; `--forget` tombstones exactly
what `owns` selects and leaves the neighbours (a second source's slice) standing; `--keep`
leaves everything and the entities are still readable afterwards; no flag is needed when the
slice is empty; either way the reflector is stopped.

### 6. Scope

The registration is written through `s.scopedStore(r)`, which already reads `?scope=`; the
reflector for a scoped registration is built over `s.Store.Scope(name)`. The ownership check in
step 2 is scoped with it — two sources in one scope must still be provably disjoint, while a
scoped source claiming what a baseline source also claims is the overlay doing its job and
must NOT be refused. Restore in `Start()` enumerates scopes the way a scoped charter would.

The lifetime is free and should be pinned as such: `DeleteScope` already drops the overlay, so
it takes the registration and everything reflected with it, and a scoped source has no
deregistration path to forget.

**Proves:** writes and tombstones land in the scope and never in baseline (read baseline
directly and assert it is untouched — the failure this guards is a converge handed the wrong
store, which nothing in the reflection would report); a scoped source shadows baseline's view of
the same repo for that scope's triggers and no one else's; `Covers` evaluated against the
overlaid read tombstones an entity inherited from baseline *in the scope only*
(`TestLogdStore_ScopeHidesAnInheritedEntity` is the primitive this rides on); `DeleteScope`
takes the registration and the reflected entities together.

## What this plan deliberately does not do

- **No dial-in, no xap.** That is step 4 of the note, and it is a `DialContext` on verse's side
  over a contract this plan does not change. A `dial-in` registration is refused at install
  until then, rather than accepted and silently never dialled.
- **No authority on the hint route.** It carries no data, which is the argument the note makes,
  and it lands with [authority](../roadmap/authority.md) rather than before it.
- **No write-back**, no paging, no streaming reflect. All three are the note's own exclusions.
- **No `verse source sync`.** A manual pass is one line over `Reflector.Reconcile` and nothing
  needs it yet; a hint already covers "look now".
- **No ownership inference.** A source that does not implement `Owner` reports no `owns`, and a
  registration with no `owns` is refused a second registration in its system rather than having
  one guessed for it from a reflection.

## Risks worth watching

**Ownership-as-an-address is a guess, and it is wrong in a knowable direction.** It works when a
source's ids encode ownership positionally, which git's do only because `git.go` prefixes every
id with the repo — a property of that code, not a law. A source reflecting `linear:issue:ENG-123`
whose ownership is "project ENG" cannot spell that as an address at all, and will hit
`--allow-overlap` on its second registration. If the first non-git source does, that is evidence
the vocabulary should go back to predicates, not that the operator should keep passing the flag.
`Covers` stays a `Pattern` throughout, so that road is still open.

**A trigger over `sources:*` and the registration's own writes.** The anti-churn rule above
keeps a quiet verse quiet, but `lastError` still writes on transition, and a source flapping
between reachable and not would write on every flap. Worth watching once anything actually
watches that slice.

**A `dir` registration is not portable and a restart can resurrect a stale one.** It names a
path on the machine verse runs on. A restart restores it whether or not the directory is still
there; the reflector will simply report errors into `lastError`, which is the right behaviour
but is worth confirming rather than assuming.

**Stopping a reflector on remove has to be exact.** The map is the only handle on a running
goroutine, so a remove that fails between cancelling and deleting the entity — or an install
that overwrites a registration without stopping the old reflector — leaks a writer onto a slice
nobody is registered for. Two writers on one slice, each tombstoning what the other reflected,
is precisely what the disjointness check exists to prevent, so leaking one past it would be the
same bug through the back door.
