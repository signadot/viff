# A spawn that ran: the record, and why it is not a singleton seq

Status: exploration, nothing here runs. It comes out of git-issue `2xth4bxsh12krsfwdxn0`, whose
check landed as an install-time relation (`trigger/deselect.go`) — this is the runtime answer
that would make most of that check unnecessary, examined far enough to say what it would cost.

## The question

A trigger is evaluated at every commit touching what it reads, so something has to stop a
spawning rule from spawning again. There are exactly three families of answer, and verse uses a
different one at each level:

| | mechanism | who owns it | scope |
|---|---|---|---|
| a **latch** | the action writes what the predicate refuses | the CHARTER | per entry into the state |
| a **live check** | `spawnHooked` refuses a second process under one id | the engine, in memory | while the process runs |
| a **record** | `seqDone`: a store fact saying this already ran | the engine, durably | per subject, per shape, ever |

The check exists because a bare spawn has only the middle one, and the middle one is a lease on
a running process: once it is reaped the next commit spawns again. That is the whole argument
for refusing an unlatched spawn at install.

The obvious move, then: **give a bare spawn the third one.** A `seq` already has it — which is
exactly why the check exempts a sequence — so read `action: {spawn: X, onDone: Y}` as
`action: {seq: [{spawn: X}], onDone: Y}` and the machinery is already written. `startSeq`
claims, `seqDone` refuses a repeat, `finishSeq` fires the carrier's hook.

It would buy three things. The bound becomes a STORE FACT rather than a liveness check, which is
the hole the whole check exists to paper over. The refusal could become advice — an unlatched
rule would terminate rather than spin. And it is not new machinery.

## What it costs, measured

**1. `done` is "ever", and a latch is "per entry".** `seqDone` reads
`verse.charter.<rule>.done` off the subject and nothing ever clears it; only editing the
sequence (the shape hash) frees it. So a `seq` runs at most once per (rule, subject, shape) for
the life of that charter. Applied to every spawning rule, the tutorial's own `shape-latch` —
open → reviewing → reviewed — would review a PR once and never again, however many times it
returns to `open`. That is the "once, ever" semantics
[change-events](../change-events.md#what-dropping-create-vs-update-costs-and-the-idiom-that-recovers-it)
says the model deliberately gives up in exchange for state-driven flows — inverted for every
charter at once, silently.

So the record cannot be a boolean. It has to be keyed on something that moves when the subject
legitimately re-enters the state.

**2. The key is a version, and a charter already writes one by hand.** Record the subject's
content at fire time — a hash of its payload minus the reserved root — and re-fire only when
that differs. The spin case is bounded exactly: an action that writes nothing leaves the subject
identical, so it does not run again. Re-entry works: open → reviewed → open changes the payload
twice, so the recorded version is stale and the rule runs.

The evidence that this is the right shape rather than a clever one is that `verse connect`
already authors it: `value: !not {validated: null}` with a patch writing `validated`, described
in its own comment as *validated once per version rather than once per commit*. The charter is
being made to hand-write what the engine could record. (It writes a literal today only because a
hook cannot reach `$hash` — see the comment at `cmd/verse/connect.go`.)

What it changes: any write to the subject re-fires the rule, including one that has nothing to
do with it. That is looser than a latch, which re-fires only on re-entry into the selected
state — and much tighter than today's "every commit touching what the rule reads".

**3. Where the record lives is a real choice, and both options cost.** On the SUBJECT
(`verse.charter.<rule>`, as `seqDone` does) it needs no new entity, but it pollutes the author's
document, shows up in `verse get`, and is a commit that wakes the rule that wrote it — the
record must then be excluded from the version it records, or it invalidates itself. In its OWN
entity (as the run claim is, `process:claim:<chain>@<subject>`) the subject stays clean, but
nothing reaps it: one entity per (rule, subject) that ever fired, durable by design. A fan-out
over ten thousand subjects is ten thousand records either way.

**4. The release story has a hole today, and it is load-bearing.** Measured: a sequence element
that ends in a status other than `done` takes the hook path in `Engine.applyHook`, and nothing
on that path releases the run claim.

```
after a failed element: claim process:claim:review@demo:pr:1 held = true
spawns before stir = 1, after = 1
```

The sequence does not run again — not because `done` was set (it was not) but because the claim
was never given back. `Server.reconcileClaims` clears it at the next restart, so the repeat
policy for a failed sequence is *not again until verse restarts*. Fail-stop is implemented by a
leak. `releaseRun`'s own comment says "every exit path releases", and this exit path does not.

That matters here beyond being a defect worth fixing on its own: extending this mechanism to
every spawn extends that behaviour to every failed spawn, which is worse than today. A failed
spawn re-fires on the next commit now, and `onFailed` exists because that is sometimes what you
want.

**5. Two mechanisms for one question.** With both a latch and a record, "will this run again?"
has two answers that can disagree, and an author has to hold both. Today there is one answer —
the predicate — and one place to be told it is wrong.

## What it would do to the install check

Not remove it. The relation in `trigger/deselect.go` answers "can the subject still match after
this action runs", and under a record that stops being *spins forever with a bill attached* and
becomes *fires once and never again* — a different defect, and still one worth naming while the
author is present. What would change is the SEVERITY: the refusal could become advice, because
the runtime would bound the loop. `latchUnsure` and `latchUntouched` are already the two
verdicts that would want to soften first.

## Where this leaves the singleton seq

The rewrite as posed is the wrong route. It inherits `done`-as-forever (1) and the unreleased
claim (4), and it would fold two questions the seq machinery deliberately keeps apart —
`startSeq`'s own comment: the claim is a MECHANISM for exclusion, `done` is a POLICY about
repeating, and "fusing them is what forced the policy to be maximally strict back when the
mechanism could not be trusted."

What is right is the other direction: lift the RECORD out of `seq` into a concept of its own — *a
rule has run for this subject at this version* — and let a sequence's `done` become the special
case where the version is the whole sequence. Then a bare spawn gets a bound without inheriting
a repeat policy nobody chose for it.

Three things want settling before any of that is worth writing:

- **Reaping.** A durable per-(rule, subject) record with no story for its own removal is a
  retention floor. The claim has one (release at every exit) and it is already leaking; a record
  that is *supposed* to outlive its run has a harder version of the same problem.
- **What counts as a change.** Hashing the payload minus the reserved root is the obvious
  answer, and it re-fires on writes the rule does not care about. Hashing only the paths the
  predicate reads is tighter and is the same walk `readPaths` already does — but then re-entry to
  the same value (open → reviewed → open) is invisible, and the record has to be cleared when the
  subject deselects, which nothing currently evaluates.
- **Whether the author can see it.** A rule that does not fire because of an invisible record is
  the same debugging problem as a rule that does not fire because of an invisible conjunct — the
  reason `condition ∧ ¬patch` was shelved on that issue. `verse trigger show` would have to say
  what has run.
