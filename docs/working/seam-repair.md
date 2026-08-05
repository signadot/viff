# Repairing the seams

Status: design, nothing built. It answers the census in git-issue `md0te4qch12krf7ndsn0` —
24 observed failures at the broker, runner and answering seams, and four behaviours that hold
up. The census is the evidence; this note does not repeat it, only cites it by tag.

The census was taken before any fix, on purpose, so the design could read the whole list at
once. That paid: eleven of the findings are four mechanisms seen from different sides, and
fixing them one at a time would have produced eleven local patches instead.

## What is actually wrong

Every seam reports success it cannot vouch for. A control that never left the building reads
as applied (`R1`), a refusal reads the same as agreement (`P4`), an answer is recorded as
delivered when it is merely queued (`A2`), and an index that stopped being written says
nothing at all (`P5`). Underneath is one habit: a call whose failure has nowhere to go.

The second habit is a reader that cannot tell what it does not know. A closed stream means
both "the task ended" and "your connection died" (`R3`, `R4`); a truncated replay carries
no marker (`B4`); a resume across a standing gate silently omits the gate (`P6`).

Neither is a distributed-systems subtlety. They are places where a return value was left out.

## Four mechanisms

### 1. A control call reports what became of it

```go
// Handle
Cancel(reason string) error
Steer(instruction, by string) error
Resolve(decision string, proceed bool, say, by string) error
```

Three outcomes, distinguished by sentinel: applied (`nil`), refused by the far side
(`ErrNoSuchQuestion`, `ErrAlreadyAnswered`, `ErrFenced`), and never reached it
(`ErrUnreachable`). The third is the one absent everywhere today, and it is the one an
operator most needs, because it is the only one where retrying helps.

It maps onto the HTTP surface without invention: `nil` → 204, already-answered → 409, no such
question → 404, unreachable → **502**. That last is `A1` — answering an open question on a
runner that is down currently returns "no such question", which sends the operator hunting a
typo.

Closes `R1`, `A1`, `P4`, and the diagnosis half of `R2`.

*Why sentinels rather than a `Delivery` struct.* Both express it. `error` is what every caller
already has a place to put, and a struct would be a second convention for the same fact.

### 2. One resumable reader

A reader that is behind must be able to become current. Four parts, and only the first is new
thinking:

**A stream that ends without a terminal is a broken stream.** That rule needs no machinery —
it fell out of `ba1e492`, which made the bus close after the terminal and never before. It is
what lets a reader tell `R3` from a completed task.

**Reconnect at the last cursor.** `mirror.Reflect` already writes `cursor` to the entity, so
it already knows where it was. On a stream that ended without a terminal it re-subscribes
there, with backoff. `SubscribeSince` becomes part of `Handle` rather than an optional
assertion; three of the four implementations already have it, and `FailedHandle` — work that
never started — answers it the way it answers `Subscribe`, with the one terminal frame.

**A resume replays open gates.** This is `P6`, and it is the finding that matters most,
because a resolver is a client of `?since=` and nothing else. The live path replays open gates
today; the resume path does not, on the stated grounds that the ring carries them — true until
256 events later. The bus tracks open questions the way the broker already tracks `ps.pending`
(an `EvPending` is open until its `EvDecision`) and replays them after the gap marker.

**A silent stream is not a healthy one.** A keepalive comment every N seconds; a reader that
has heard nothing in 2N treats the stream as dead and reconnects. With a client timeout on the
control calls — on a dedicated client, never `http.DefaultClient`, which is shared
task-wide — that closes `R2`.

Closes `R2`, `R3`, `R4`, `P6`, and makes `B4`'s truncation visible.

### 3. The cursor a worker resumes from is the one it acknowledged

Not the one the broker holds. `claim` answers with `cCursor: ps.inboxCur` — the present — so a
worker that drops and re-claims is told to resume from now, and skips exactly the control
issued while it was away (`P2`). That window is the outage, which is when control is most
likely to have been sent.

The acknowledgement already exists and is not being read: a worker polling `inbox?since=N` has
thereby acknowledged everything at or below `N`. The broker records that high-water mark and
`claim` returns it. No new protocol field, no new round trip.

The same principle covers the other direction. `B3` replays control because the worker forgot
where it was; `P2` skips it because the broker assumed. One rule fixes both, and idempotence
keys on `report` (`B2`) make a replay harmless rather than merely unlikely.

Spawn is the sharp case, and it got sharper while this note was being written. `R6`: two
spawns of one id do not merely create two tasks — the second REPLACES the first in the
daemon's map. The original keeps running with nothing addressing it, and every control, event
and gate for that id reaches the replacement. So a retry of the request `R5` describes, the
one whose response was lost, destroys the work it was retrying. Spawn must be idempotent by
id — a repeat returns the existing task — before anything is allowed to retry it.

`P8` belongs here too: a report whose batch contains a terminal drops everything after it and
answers 200. It should answer with how many events it applied, which is what makes a retry
safe to compute.

Closes `R5`, `R6`, `B2`, `B3`, `P2`, `P8`.

### 4. A claim is a lease, and an answer is delivered when it is acknowledged

`B1`: a worker that claims and vanishes holds its claim, its gate and its task forever.
There is no lease, no heartbeat, no health check — the code's own comment says "the eventual
health-check calls `Revoke`", and nothing calls `Revoke`.

The renewal already exists, again: a worker polls its inbox. Each poll or report renews the
lease. On expiry the broker releases the claim so another worker may take over, and publishes
that it did — the task is not terminal, because the work may well still be running
somewhere.

`A2` is the same shape one layer up. An answer is recorded as settled the moment it is queued:
`handle.Resolve` emits `EvDecision` and clears the gate at enqueue, so every surface reports
the question decided while the worker that must act on it has heard nothing. The decision
should be recorded when the worker's acknowledgement passes that item — which mechanism 3 has
already made observable. Until then the record says answered-and-undelivered, which is a state
a docket can show and a person can act on. If the lease expires first, the answer was never
delivered and the question returns to the docket.

`P3` and the brokered half of `A3` are one line of the same idea: a terminal must settle the
open gates it is abandoning. The latch already exists and simply does not reach them.

Closes `B1`, `A2`, `P3`, brokered `A3`.

*The SAT check on mechanism 4.* Take the rule "an answer is not decided until acknowledged"
and look for a model where it holds and the system is still wrong. Here is one: the worker
acknowledges the inbox item and dies before acting. The answer is recorded as delivered; the
gate never opens. Acknowledgement is not execution, and no protocol makes it so — what bounds
it is the lease, which expires and republishes the work. So the rule is sound only in company
with mechanism 4's other half, which is an argument for landing them together rather than a
defect in either.

## What stands alone

- **`P9`** — a cancelled sole waiter is dropped from the registry and the record stays
  `pending`. `OnPost` fires on the way in and has no counterpart on the way out. A few lines,
  no design cost, and it prejudices nothing.
- **`P5`** — a store that stops accepting writes leaves a finished task reading `running`
  forever, and a gate on no docket. The policy (drop rather than stall the loop) is right; the
  silence is the defect. The minimum is that the failure is countable and visible on
  `verse status`, since by construction it cannot be written to the store.
- **`B4`** — the broker's log is unbounded and truncates its replays with no marker. Bound it
  like the ring, and the below-watermark gap frame — written in `4cfa941`, untriggerable today
  — starts meaning something.
- **`B5`** — what a fenced worker should do next is unwritten. Prose, not code, and it should
  be written before the lease makes fencing common.

## Order, and what it buys

**First, and cheap.** Nothing here needs a new concept: control calls return errors; a
dedicated client with a timeout; `P9`'s missing hook; a terminal settles the gates it
abandons; `report` answers with what it applied; spawn is idempotent by id; `claim` returns the
acknowledged cursor. That is eleven findings, mostly mechanical, and it makes the rest
diagnosable while it is being built.

Spawn idempotence is first among these rather than merely in the list. Until it holds, every
other retry this design adds is a hazard rather than a repair.

**Then, one concept each.** The resumable reader, the claim lease, delivery-by-acknowledgement.
Each is a design in its own right and each wants its own note. They compose in that order:
reconnection is what makes a lease survivable, and a lease is what bounds an unacknowledged
answer.

**Last, policy.** Retention, and the visibility of index failures. Both want a number nobody
has evidence for yet, which is a reason to do them after the rest is running.

## What not to build

**Not a consensus story.** Two verses over one broker already behave correctly (`P1`): the
broker's pending set arbitrates, one answer is delivered, and the loser is recorded stale. The
one race that remains is narrow, unobserved in three runs, and named in the census. Nothing
here needs agreement between verses.

**Not a clock.** Nothing in these seams depends on a clock for correctness today — every
timestamp written is a record, never a decision input. The lease introduces the first such
dependency and should be designed knowing it is the first: one clock (the broker's,
monotonic), a generous TTL, and no comparison of timestamps written by different machines.

**Not the LLM driver.** Its failures at these seams are the transport ones already enumerated;
what is new is model-side, which is a different subsystem and costs real budget to exercise. It
gets its own pass.

## What is open

**Whether the resume replays open gates, or tells the reader to read the record.** `P6` has two
answers. The bus can replay open questions after a gap, or a stream reader can be told that the
stream is not the authority and `gate:question:*` is. The second is cheaper and is already true
— the docket survived `P6` precisely because the question is an entity — but it makes every
stream client a store client, which a resolver on another machine may not want to be. Not
settled.

**Whether `Handle` should carry this much.** Mechanisms 1 and 2 both widen it, and it is the
seam a third party implements. The alternative is a wrapper that adapts a minimal Handle and
holds the retry, the deadline and the reconnection — which is where that logic wants to live
anyway, since it is identical for every runner.

**What an unacknowledged answer looks like on the docket.** `A2`'s fix creates a state between
answered and delivered. It is honest and it is new, and a docket that shows four states instead
of two is a docket that has to be explained.
