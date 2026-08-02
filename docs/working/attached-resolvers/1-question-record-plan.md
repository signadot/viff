# The question record: implementation plan

Status: plan, nothing built. The shape is [1-question-record.md](1-question-record.md) and the
ground it stands on is [0-prerequisites.md](0-prerequisites.md); this file does not restate
either, only the order of work and what each step must prove.

It lands what the prerequisites call step 2, and takes step 4 with it: once the record is where
a pending question lives, surviving a restart stops being separate work. Steps 1, 3 and 5 there
— the effect vocabulary, the single answering path, uniform `?since=` — are untouched and can
land before or after.

The shape being built: `gate:question:<id>` written when a question is posed, closed when it is
answered, with the process entity pointing at it rather than copying it.

## Order of work

Five steps. The dependency order is real — 2 has nothing to write until 1 exists, 3 is what
makes the restart behaviour observable, and 4 is the one that changes an existing surface. Step
5 is separable and could be dropped without stranding anything.

### 1. Write the record when a question is posed

`process.Registry.Resolve` gains a store-backed side: posting a question writes
`gate:question:<id>` carrying id, origin, subject, gate, mode, tool, derived effects, spawns,
resolver ref, `posedAt` and `escalatesAt`. The in-memory pending map stays exactly as it is —
it is the waiter set, and this step does not touch who blocks on what.

No resolver entities exist yet, so the field is written and left empty. That is the honest
state — unbound — and it is worth carrying from the start so nothing downstream learns to treat
its absence as a schema variation.

**Proves:** a posed question is a fact, whether or not anything owns it. Test: pose a gate,
assert the entity exists with the fields set. Then the two cases that killed publishing onto the
process entity: a question posed by a firing with no process behind it yet, and one posed by a
`call_agent` child, which has no `process:instance` of its own — both produce a record, and the
child's names the child as `origin` rather than the parent that will read it.

### 2. Close it when it is answered

`Registry.Answer` writes the outcome — `status`, `verdict`, `by`, `why`, `answeredAt` — as a
CAS on `status: pending`. The loser of a race writes a `stale` record beside it rather than
returning 404, which is the behaviour `0-prerequisites` names and the runner contract already
specifies.

Nothing reads a verdict off the record to act on it. That is worth a comment in the code, not
only in the note: the read path is the control plane, and a future reader who "helpfully" wires
a reaction here has re-created the command entity the steering invariant forbids.

**Proves:** the answer is durable and attributable, and the race is an outcome. Test: answer
twice from different `by`s; assert one applied and one `stale`, both readable, the process
proceeding once. Assert an answer to an unknown id still fails — a stale answer to a real
question and an answer to no question are different errors.

### 3. `verse gates` reads the slice

The listing stops calling `Registry.Pending()` and selects `gate:question:*` with
`status: pending`. That is a one-line change with a disproportionate consequence: the docket now
survives `verse down; up` for work that outlived the daemon, and says what each question is
about rather than naming a gate.

**Proves:** the docket is durable. Test: pose a question on a brokered process, restart verse,
assert `verse gates` still lists it with the same id and that answering it still reaches the
worker. And the negative: a question whose local runtime died does not linger as pending — it is
reconciled the way its process is.

### 4. `pending` becomes a ref, `pendingGate` goes

`mirror` writes `pending: gate:question:<id>` and stops writing `pendingGate`;
`trigger/flowtree.go` reads the gate name through the ref for `FlowNode.Gate`.

This is the `contract` push. It needs the `!` marker, a `Breaks:` line, and the notice already
filed — which currently says `pending` becomes a *record*, and must be corrected to say it
becomes a *ref*, since that is a smaller change than the notice describes and readers will plan
against it.

**Proves:** nothing in the tree reads the old shape. Test: `verse flow show` still renders the
gate name; grep-level assertion that no other reader exists is the review, not the test.

### 5. Trigger gates write records too

`trigger/engine.go` poses its gate questions through the same path, with `origin` naming the
rule rather than a process. Nothing else changes: the same fields, the same lifecycle, and
several questions open at once for one firing, which is already what the engine does with a
goroutine apiece.

**Proves:** parity. Test: a firing with two gates produces two records with distinct ids and one
subject; answering one leaves the other pending; `verse gates` shows both with their gate names.

## Keeping the loop off its own docket

`gate:*` joins `trigger:*` and `process:*` as machinery excluded from the default match set. A
rule that wants to react to questions names the system explicitly, exactly as one that watches
the charter does.

Without that, every posed question wakes every rule whose subtree includes it, and the first
charter anybody writes over a busy verse becomes a self-sustaining fire. This is one line in the
engine and belongs in step 1, not after somebody finds it.

## The residual to expect

**Retention is unowned.** A pending question is bounded by what is in flight; an answered one is
history and nothing reaps it. The lifecycle marker and the reaper exist, so the work is pointing
them at settled questions and deciding a window — which is a decision driving will inform and
speculation will not.

**The resolver ref is empty until resolvers exist.** Step 1 writes the field; nothing fills it
until the design lands. A reader should treat empty as *unbound* rather than as a field that has
not been implemented, which is the distinction a later lookup will depend on.
