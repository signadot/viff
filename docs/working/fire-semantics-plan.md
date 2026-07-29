# Firing semantics: the plan

Status: plan for a proposal. Nothing here runs. It implements
[fire-semantics.md](fire-semantics.md), which states the rule and derives it; this file says
only what has to change and in what order. Section references of the form "design §N" point
there.

Each phase is a merge to `main` and a push. `make test` and `./smoke.sh` must be green at the
merge — baseline re-verified on `7c77701`: smoke passes, 33 checks (36 from phase 5,
which adds three).

**Standing decision: edge-faithful.** Where a choice arises between reporting a level exactly
and reporting every transition, transitions win. A level can be recovered from the store at
any time; a transition the stream never reported cannot be recovered by anyone. This settles
the coalescing question phase 1 left open (phase 2 below), and it is the reason `on` can be an
edge at all.

## 1. The work, by area

**Store.** A snapshot read at a commit — evaluate a predicate and a selection against
`store@C`, and against `store@(C-1)` for the edge. New surface on `entity.Store` and both
backends; the watch layer already maintains a per-subtree document and its previous snapshot
(`WatchSubtree`), so the shape exists even though the interface does not.

And a commit number on a subtree watch: `emitDiff` sets no `Rev` (`logdstore.go:705`), so the
production observation path carries no commit at all — while the root path does
(`eventsFromDelta`). A model defined per commit needs the stream to say which commit it is, and
the burst coalescing beside it (`logdstore.go:741`) has to become per-commit for edges, because
a predicate rising and falling inside one coalesced diff leaves no observable edge. Coalescing
stays right for a level and is lossy for an edge, which is the model's own distinction showing
up in the plumbing.

**Entity lifecycle.** Delete intent as a sink state, written before removal (design §3) —
which is what lets `on` stay negation-free. The selection side is what touches this design:
`Match.Select` (and so every `subject`) has to exclude sink-state entities by default, or
charters act on entities on their way out.

**Trigger.** `Condition` becomes `{On *Formula, When *Formula, Subject Match}` — `on` and
`when` are the same type, since they are the same predicate at two evaluations.
`matchesEvent`/`resolveSubject`/`dispatch()`/`opensTo` are replaced by one evaluation against a
snapshot pair. `validate` drops the naming-vs-selecting fork and the `op` dimension, and gains
the sampling lint (design §5.2), the latch lint (§5.3), and the conflict check (§5.4).
`engine.go`: dispatch becomes "for each commit, for each trigger not skipped by `Entities(C)`,
evaluate and fire per member"; `fire` takes one subject; gate/motion/claim identity keyed on
the subject; `reserveN` becomes one; `subjectLabel`/`spawnLabel`/`summarize` go; the ignition
guard goes; a spawn takes a durable claim on the pattern of `seq.go:339`. `deref.go`:
`refRoot` reads the subject.
`tonycodec.go`: `Condition`'s hand-written codec gains `on` and must omit it explicitly —
`omitzero` is inert on a custom-codec field, which is why that codec is hand-written at all.

**Schema.** `schema/curated.tony`, `trigger/schema_gen.tony`, then `go generate`.

**Tests.** `fanout_test.go` — its thirteen mostly survive as vacuous-`on` sweeps, with the
one-gate-per-firing test (`:255`) inverting and the two label tests (`:430`, `:461`) going.
`condition_test.go:23`,`:56` and `fanout_test.go:396` go with the redirect (design §6).
`patch_test.go:109` depends on the create amendment. Anything asserting `op` on a `when` moves
to an edge. New: the rising edge (including that it does *not* re-fire while true), the vacuous
case, the `Entities(C)` skip, snapshot consistency between `when` and the selection, gate dedupe
across re-evaluation, one motion per subject, a spawn that does not re-spawn after its process
is reaped, a sink-state entity firing `on` once and never being selected as a subject, and a
removal firing nothing.

**Docs.** `trigger-model.md` is the big one — the subject-resolution table, §Broadcast, §`when`,
the completion-pattern paragraph, and the naming-vs-selecting distinction all restate the model
being replaced. Then `tutorial.md` (its §2's "no separate `when`", and the goal latch),
`change-events.md` (its edge-vs-level argument, and the `Rev` residual, which becomes
load-bearing as `Entities(C)`), `reference.md`, and `smoke.sh:219`. `connect.go`'s validator is
moot — `docs/roadmap/expose.md` drops the built-in reactivity and makes it an opt-in charter,
which is where the latch discipline (design §5.3) should be written down for whoever authors it.

## 2. Phases

The order is chosen so that the one phase which changes *when* a trigger fires arrives last and
small — everything it would otherwise have to carry is landed ahead of it, under the old
dispatch, where smoke can see it working.

Two ordering facts drive the shape. **The action's coupling to the cause has to go before
dispatch does**: once firing is per commit there is no `entity.Event` to re-root a binding from
or to read a `flow` out of, so those two removals cannot ride the same phase as the flip —
they precede it. And **gate identity has to go before the flip**, because a vacuous `on`
re-evaluates per commit and today's monotonic gate counter (`engine.go:554`) would pose a fresh
question each time.

### Phase 1 — the store learns to read at a commit, and to name one

Two store-side additions, both prerequisites for anything above them.

**A read at a revision** — enough to evaluate a predicate and a selection against `store@C`,
and against `store@(C-1)` for an edge. One implementation, not two: `LogdStore` is the only
`Store`, and a scope is a copy-on-write child of it, so a scope snapshots its own view for
free. The watch layer already maintains a per-subtree document and the snapshot before it
(`WatchSubtree`), so the shape exists even though the interface does not.

**A commit number on a subtree watch.** `WatchSubtree`'s `emitDiff` sends
`Event{Ref, Op, To}` and sets no `Rev` (`logdstore.go:705`), so the production path —
`ObserveScoped`, which is what `versed` runs — has no commit framing at all. The root path
does (`eventsFromDelta(ev.Commit, …)`), which is part of why the two dispatch paths never
converged. A model defined per commit C cannot be built on a stream that does not say which
commit it is.

Nothing calls either yet. Store-level tests; smoke untouched, which is the point of doing it
first.

**Landed.** `entity.Reader` (the read half of a `Store`) and `entity.Snapshot` (a `Reader`
fixed at a revision), `Store.AsOf(rev)`, and `docSnapshot` — one `MatchAt` over the root, which
docd answers across base and every mount at the same commit, with both reads then pure
functions of that document so two reads of one snapshot cannot disagree. `WatchSubtree` stamps
`Rev`. The coalescing residual is recorded on `emitDiff` rather than fixed: a burst is applied
then diffed once, so the stamp is the latest commit the diff includes — exact for a level,
lossy for an edge, which is phase 2's question.

### Phase 2 — the engine dispatches a commit

One dispatch path, taking a commit — `{Rev, Entities(C)}` — where today there are two: the
root cursor loop (`loop`/`catchUp`/`dispatch`, per event) and a per-trigger goroutine per
subtree watch (`watchTriggerSubtree`, per event, with its own matching). Behaviour is
preserved exactly: the commit is unpacked and each entity dispatched as it is today. Only the
shape changes.

This is worth its own phase for three reasons beyond tidiness. It is where `Entities(C)`
becomes a real value rather than something derived later — and that is the skip index the
whole model leans on for affordability (design §3). It is where the two paths converge, so
every phase after it is written once instead of twice. And it is behaviour-preserving, so
smoke is a real oracle for it, which is not true of any phase after phase 5.

**It also forces the coalescing question, and edge-faithful settles it.** `WatchSubtree`
applied a burst of watch events and emitted **one** diff ("a rapid N-file connect costs one
diff, not N"), so a single diff could span several commits — exact for a level, lossy for an
edge, since a predicate that rises and falls inside one burst leaves no trace in the merged
diff and the firing that should have happened never does. Under the standing decision the
coalescing goes: one commit in, one commit out. The cost is a subtree walk per commit rather
than per burst.

**Landed.** `entity.Commit{Rev, Changes, Gap}` is the unit of observation — `Observe`,
`Since`, and `WatchSubtree` all deliver it — because the framing cannot be recovered
downstream: events of one commit share a Rev, but a consumer reading them one at a time cannot
tell whether the next belongs to the same commit without waiting for the one after, while the
store, which built them from a single delta, knows exactly. `Engine.dispatchOne` is the firing
decision in ONE place for both ways a change reaches the engine (the root loop walking a
commit, and a per-trigger subtree watch), where each used to carry a copy — which is how they
came to disagree about the ignition guard and about budget, invisibly, since an engine only
ever runs one of them. Observation still has two suppliers, and that is inherent: a scoped
engine opens no root watch, so per-trigger observation means per-trigger delivery. What
converges is the decision, which is what had to be written once.

### Phase 3 — gate and motion identity key on the subject

A gate question's id becomes a function of (trigger, gate, subject), and a firing that finds
its question already pending waits on it rather than posing a second. A temporal gate's motion
channel keys on the subject instead of the changed entity (`engine.go:605`).

Both are improvements under *today's* semantics too — a re-fire already duplicates a question —
so this lands with no semantic change. Smoke's gated charter (`smoke.sh:250`) exercises exactly
this path: one question, answered, released, gone from the docket.

**Landed.** `gateID(trigger, gate, subjects)` replaces the counter, hashing the subject list
because the id crosses a URL path and a command line and a broadcast's list is unbounded. A
deterministic id is only safe once the registry JOINS a question already pending — `pending`
is keyed by id, so a second post used to displace the first and leave that caller blocked on a
question nothing could answer — so `process.Registry` now broadcasts one answer to every
waiter and counts waiters, since one caller's cancellation must not take the question away
from the others. The motion channel keys on the same thing; it keyed on the CAUSE, which put
one firing's several subjects behind a single vote and two firings about the same subjects
behind two. The tests had it backwards in the same way — their helper's doc said "subject"
while they passed the changed entity — which is the mis-keying stated out loud.

### Phase 4 — sever the action from the cause

`refRoot(ev, subject)` (`deref.go:44`) reads the subject instead of the changed entity, and the
`flow` redirect (`trigger.go:167`) goes. Breaking for charters that use either; neither depends
on the dispatch change, which is why they come now rather than in the flip.

Fallout is tests and docs: `condition_test.go:23`,`:56` and `fanout_test.go:396` are about the
redirect, and the completion pattern moves to hooks in `trigger-model.md` and `tutorial.md:862`.
Smoke uses neither — its charters have no `$` references and no `when` over `process:*` — so it
stays green without edits.

**Landed.** `refSource` names the two answers to "where do this action's references read
from", because there turned out to be two and the plan assumed one. A trigger firing reads the
SUBJECT — the change that dispatched it is a doorbell and may name an entirely different
entity, whose fields have no meaning on the entity being acted on. A completion hook (and a
sequence element one drives) reads the CAUSE, because $result/$err/$data are exactly what a
hook exists for — and because `Engine.procEvent` synthesizes that payload rather than reading
it back, since the mirror may not have written the terminal facts yet. So it is not merely a
different entity to read, it is a payload only the engine has, which is why this could not be
one rule.

Where the subject IS the changed entity the event already carries its whole level state, so
that is used directly — exact at the commit, and no read. Otherwise the subject is read from
the store, which is CURRENT rather than as-of-the-commit: the one place this is looser than
the model wants, and it tightens for free in phase 6 when a firing carries the snapshot it was
evaluated against.

The redirect is gone, and with it the correlation it gave. What replaces it is hooks, which are
exact — and the tests say so in both directions: a `when` over process:instance now advances
every PR its subject selects (`TestCompletionWhenSweepsItsSubject`), while a hook advances the
one its own process was about and leaves the other alone (`TestOnHookDoesNotCrossSubjects`).

### Phase 5 — `on` arrives, additively

`Condition` gains `On`, and a trigger **that has one** dispatches by the new rule: `¬on(C-1) ∧
on(C)` over the snapshot pair, `when` as a level at `store@C`, `subject` as a selection over
`store@C`, one firing per member. A trigger **without** one keeps today's dispatch exactly.

Two paths coexist on purpose. The new one gets its own tests — the edge fires once and does not
re-fire while true, the level guard refuses a firing, `when` and the selection come from one
snapshot — and the old one keeps every existing charter, including all of smoke's, on the code
that already passes.

**Landed.** `Condition.On` is a `*Formula`, the same type as `When`, because they are one
grammar: a predicate over the store, asked twice for a cause and once for a condition.
`Engine.dispatchEdge` is asked once per COMMIT rather than once per changed entity — `on` is a
predicate over the whole store, so "did it change" is a question about the commit, and asking
it per entity would answer it once for each. The edge is derived as `AsOf(C-1)` against
`AsOf(C)` rather than from remembered state, so a replayed commit reaches the same verdict as
the live one; and the guard and the selection come from the single `AsOf(C)`, which is where
phase 1's snapshot finally earns its place.

`Select` and `Eval` widened from `entity.Store` to `entity.Reader` — the seam phase 1 built for
exactly this, and the reason a snapshot can stand in for the store without a second code path.

Two properties an author has to know, both pinned: an edge does NOT re-fire while it stays true
(a second PR reaching `reviewed` under an unchanged green CI is not a cause), and an `on`
trigger does NOT act on the backlog (no rising edge to observe at install), which is the exact
opposite of the level-triggered seed a subject-dispatched trigger gets. Both are consequences of
`on` being an edge, and both are the kind of thing that is obvious once stated and baffling
when met in the wild.

Smoke gains a step, against the plan's expectation that it needed none: it is the only thing
that exercises install-over-the-wire, and the edge is precisely what a unit test cannot check
end to end — the write that makes the predicate false must not fire, the one that makes it true
must.

### Phase 6 — the flip: no `on` means vacuous

The old dispatch goes. Every trigger is evaluated per commit against the snapshot pair, skipped
when `Entities(C)` intersects none of the subtrees it reads (design §3 — the skip is proved
both ways, and it is what keeps per-commit evaluation affordable). The `op` dimension goes
with it.

This is the phase with prerequisites, all of them now behind us: phase 3 or the gated charter
re-poses per commit, phase 4 or there is no event to bind from, phases 1–2 for the commit and
its snapshots.

Two charters need editing, and neither is a smoke assertion — they are inputs:

- `cmd/verse/connect.go:243`. Its validator is spawn-only with `op: set` on the subject, so
  under a vacuous `on` it re-spawns per commit, forever, per file. It gains a latch —
  `patch: {validated: $hash}` with the subject taking `value: !not {validated: null}` — which
  works because a dir-status entity already carries a content `hash` (`source/meta.go:72`) and
  bindings now come from the subject (phase 4). That makes it validate-once-per-file rather
  than per-change, which is a regression the replacement retires anyway
  (`docs/roadmap/expose.md`).
- Anything else carrying `op` outside an `on`.

The **latch lint becomes an error in this phase, for one shape**: a spawning action whose
patch writes no key the subject's predicate constrains. Key sets are literal even when values
are templated, so the overlap is decidable, and it is exactly the check that would have caught
connect before it diverged.

**Landed.** `Engine.evaluate` is the only firing rule: vacuous `on` means the trigger is
considered at every commit touching what it reads. `Trigger.reads` is that list (cause ∪
condition ∪ subject), and it is both what a scoped engine watches and what `touches` skips on —
one list, so "what it watches" and "what could change its answer" cannot drift apart. Gone with
the old path: `matchesEvent`, `resolveSubject`, `dispatchOne`, `Condition.dispatch`, the
ignition guard and `machinery`, `subjectLabel`/`summarize`, and the `op` dimension.

Three things the plan did not foresee, all of them consequences of "a firing is about one
entity" being taken seriously:

- **A firing is now literally per subject.** `fireSubjects` starts one firing per member, each
  with its own gates, budget and identity — where it used to gate once over a list. Without
  that, the gate arity change the plan predicted did not actually happen: the question still
  named a set.
- **A subject can be re-fired while its own work is still under way.** Every commit
  re-evaluates, and a member whose latch write has not landed is still selected. The guard is
  two things, because a firing can outlive its own function call: `claimFlight` covers the
  firing (gates being answered, action applied), and `working` covers what it STARTED — a
  spawned process still running. Neither changes what runs, since `spawnHooked` already
  refuses a second process under a live id; what they change is when the refusal happens, so a
  firing for work already under way does not put a question on the docket or spend budget on
  its way to being told no.

  Both key on (trigger, subject), which is the identity the spawn id, the gate question and
  the motion channel already use. A residual remains and is correct: an evaluation at a commit
  where the member genuinely still matches, with nothing running, acts again — idempotently,
  because the same patch computes the same value and a no-op write raises no commit
  (`entity.TestSameValueWriteRaisesNoCommit`, which is where the termination argument is now
  pinned). Note this is NOT a compare-and-swap result: `entity.Modify` retries on a failed
  precondition, so it prevents lost updates, not repeated work.

  The one shape neither guard covers: a patch that is not a function of the state it reads —
  a `$reference` to something that moves — computes a different value each firing, which does
  raise a commit, which re-evaluates. That is design §5.3's "a non-idempotent action has no
  fixpoint", with a concrete shape.
- **One commit can reach a trigger several times**, once per watched subtree. `firstSight` is
  a per-trigger watermark: one verdict per (trigger, commit).

**The latch lint warns, it does not refuse.** The plan had it fatal here. Made fatal, it
refused a dozen test charters whose subject payload is deliberately untouched — sequence
progress, provider selection, spawn ids — none of which is about dispatch. The charter is
divergent as written and the operator hears so at install; making it fatal is a change to what
INSTALLS, and this phase is already changing what FIRES. It becomes an error in phase 8, the
same treatment the other lints get.

`connect.go`'s validator is rewritten as the plan describes: `value: {validated: !not null}`
selects the outstanding files, and `onDone: {patch: {validated: $hash}}` latches on the content
hash the dir-status entity already carries — so a file is validated once per version rather
than once per commit. `op: set` was standing in for "on the change rather than the state", and
that is what had to be written down.

Smoke's own charters need no edits, and it is worth saying why each one survives per-commit
evaluation:

| charter | why it converges |
| --- | --- |
| `smoke-advance` (`:153`), `-tony` (`:179`) | the patch moves `stage` out of the subject predicate — a latch |
| `smoke-hotfix-only` (`:217`) | the subject is an id glob with no payload predicate, so it never stops matching — but the patch writes the same value, which produces no commit (`change-events.md`, edge-on-real-change), so it is observationally inert |
| `smoke-gated` (`:250`) | one deduped question (phase 3); once shipped, the subject no longer selects it |
| restart (`:354`) | re-evaluation on start is the sweep, and a PR opening afterwards is a commit like any other |

### Phase 7 — the sink state

Delete intent written before removal; `Match.Select` excludes sink-state entities so nothing
acts on an entity on its way out; `on` over a sink state is the disappearance edge; the reaper
is an ordinary trigger. This is an entity-lifecycle change the trigger model *consumes* rather
than produces, so it can land on its own track — anywhere after phase 5, and before any charter
wants to react to a removal.

### Phase 8 — the grammar and the docs settle

The install-time lints from design §5.2 (a narrow `on` samples rather than reconciles) and
§5.4 (two triggers, one field) as warnings. The create amendment (design §6) decided one way
or the other — which is what `patch_test.go:109` waits on. Then the doc pass:
`trigger-model.md` (the subject-resolution table, §Broadcast, §`when`, naming-vs-selecting),
`tutorial.md`, `change-events.md` (its edge-vs-level argument, and the `Rev` residual, now
`Entities(C)` and load-bearing), `reference.md`, and the schema regen.
