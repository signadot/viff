# Every call on the store has a budget, or one of them takes everybody's

Status: the verse-side work is built (`entity/logdstore.go`, `entity/reconnect_test.go`). The
substrate half is filed upstream as tony-format `ps8kfs9dh12kr777fnn0`.

**This note said something else first, and was wrong.** It claimed verse's write plane treated a
dropped connection as terminal — that reads re-established and writes did not, and that what
verse needed was a session that redials. The evidence was behavioural: versed wrote nothing for
five and a half minutes and a restart cured it. The reasoning was that a store outage should not
outlive the outage, which is right; the diagnosis was a guess, and the guess was wrong.

Four tests killed it. `entity/reconnect_test.go` takes the connection away under a live store
three ways — closed cleanly, gone dark, and peer replaced over the same volume — and the store
recovers from all three unaided, in 4ms, 15s and 4ms. libctl redials, its heartbeat tears down a
wedged connection, and `Since` survives the reconnect. **The session was never the problem.** Had
the first version of this note been implemented, verse would have grown a retry layer over a
session that already retried, and the outage would have happened again on schedule.

## What actually happened

Two things, and they compound. The first is not verse's at all.

**The store got slow, monotonically, and could not stop.** logd never snapshots unless it is
configured to, and `o sys up` — what the deployment runs — has no way to configure it. So the
delta log grew from the first commit and every read replayed all of it, through an index build
that re-parses each path it examines. Measured on staging: a 147 KB document, ~280 entities,
15 MB of delta log, `logB` at 0 bytes because no snapshot had ever been taken. A match on a 5 KB
slice took 12.5 seconds. Deleting `/data` and letting the sources refill it: 201ms empty, 659ms
at 139 commits. Same code, same clients, same data. That is upstream, it is filed, and it is what
took the engine down — twice in one day, unrecoverable by restarting anything, because the cost
was on the volume.

**And verse turned "slow" into "stopped".** This is ours. Every call on the session had a budget
except one, and the one without a budget is the one a watch makes when it re-establishes:

    entity/logdstore.go   ctx, cancel := context.WithCancel(s.ctx)   // no deadline
                          s.sess.Watch(ctx, path, …)
    libctl/watch.go       s.mu.Lock(); ensureConnected(ctx)
    libctl/logd.go        connectLocked: dial, back off, retry — until ctx is done

The store's context ends when the process does, so "until ctx is done" was "until versed exits".
A watch re-establishing into a slow store held the session's one mutex for the duration, and
every writer queued behind it. **A Go mutex takes no context**, so a writer with a ten-second
budget did not spend it waiting for the store — it spent it waiting for the lock, got the lock
after the outage, and only then discovered its deadline had passed. What every caller reported
was `context deadline exceeded`, on writes that never reached the wire.

That error is why this looked like a network fault for a day. It names a timeout the caller never
actually waited out, on an operation that never happened.

## What was built

**One budget, and every session interaction inside it.** `sessionBudget` (10s) is now the single
number: reads, writes, and the attempt to establish a watch. `establish()` wraps the four watch
opens that had no bound at all, and its comment carries the reason, because the distinction it
draws is easy to undo — it bounds the ATTEMPT, not the watch. libctl uses that context for the
connect and the handshake only and does not retain it, so a bounded attempt costs the watch
nothing, and the patience that was inside the mutex moves out to the re-establish loop, which has
backoff and holds nothing while it waits.

**And the bound a context cannot reach.** Bounding the attempt is not sufficient, which is worth
knowing before someone simplifies it away: libctl's handshake read takes `wireTimeout`
unconditionally — 30s by default — so a caller asking for ten seconds still waited thirty when a
peer accepted a connection and then said nothing. Verse now sets `WireTimeout: sessionBudget` on
the session it opens. Two bounds, because there are two unbounded things: `establish` bounds the
retry LOOP, the wire timeout bounds each ATTEMPT, and either alone leaves a hold that outlasts
every caller's deadline.

`TestWatchReestablishDoesNotStarveWrites` is the proof and was the reproduction: a store with one
subtree watch, a route that goes dark and heals 20 seconds later. Before, one write attempt in a
20-second window, failing after connectivity was fine. Now it recovers 5s after the heal.

## What is still true from the first version

**Readiness is right by accident.** `/status` reports `Store.Rev()`, which with no live root watch
opens one — so the probe is a store round trip, and that is why the pod correctly refused traffic
through both outages. Nothing about `/status` says it means to be. A refactor making `Rev()` read
a cached field would turn readiness into "the HTTP server is listening", and the next outage of
this kind would present as a healthy verse writing nothing.

**Boot should be the steady state's first iteration.** `cmd/verse/up.go` still has its own
wait-for-docd loop, with its own deadline and log line. An unreachable store at second zero is the
same condition as one that vanished at 14:26, and a system that handles the second needs no
special case for the first.

Both are in [store-session-plan.md](store-session-plan.md), steps 2 and 4, unbuilt.

## The pattern worth naming

The first version of this note ended on a pattern — that a property of the PROCESS keeps leaking
into behaviour that should be about the store — and gave three instances: `Since` refusing cursors
below this instance's open revision, a task record keyed by name so a second run overwrote the
first, and a session established once at boot. Two of those were real and are fixed. The third was
a misreading, and it is the more interesting one now, because of what made it plausible.

**An absent bound is invisible while everything is fast.** Every timeout in this system was
chosen, written down, and defensible, except the one nobody wrote — and that one only ever
mattered when something else went slow. It cost a day of looking at the wrong layer, and the
error message pointed away from it the entire time. Where a call can wait on a shared resource,
the question is not "how long should this take" but "whose deadline is it spending".
