# perms: the check is ordered with the store — what is served from revision R is checked against perms at R

## The rule

**A read or a watch event served from revision R is checked against perms as they stood at R.** For a read of the head, R is the revision the read was served at. For a replay (`verse watch --since`, `verse ask --watch --since`), R is each event's own revision.

So what a reader could read at R, they can read again at R, and nothing else:
- **Revoked later:** history from before the revoke stays replayable to them. Reads and events after the revoke are refused.
- **Granted later:** history from before the grant stays hidden, including a version that was private and later scrubbed. Reads and events after the grant are admitted.

## Today

The check answers from one in-process view of the perms mirror (`attr.records`), loaded by `entity.Track` and kept current by a watch on the mirror (`attributed.apply`). The view has no revision, so what is served is judged by whatever the view holds when it is checked:

- **Watches and replays** are filtered by `attributed.narrow` (`entity/attribution.go:1061`) with `accessOf` on the current view, whatever the event's revision. A replay from `--since 0` is judged entirely by perms now. A reader revoked since sees none of the history, and a reader granted since sees all of it.
- **The view lags the store.** A record committed at R is honoured only once the mirror watch delivers it. Probe: with an open root, a stranger read a node whose record held out nothing **47 of 50 times** right after the commit that created both, and 0 of 50 times 50ms later. For a writer's own record, `invalidate` reads it back after the write returns. Any read or open watch in between is checked against the view without it. This is also true of a grant or a revoke, not only a create. That case isn't measured.
- **A creation is seen once by everyone the grants above admit.** This is documented as deliberate at `narrow` (`entity/attribution.go:1055`): the owner record is a second commit, and writing it first was judged worse. Grants on create makes the node and its record one commit, and with this rule that window closes.

## The shape

- **A versioned perms view.** The view answers "the record at key K as of R", not just "the record at K". The mirror is small and changes rarely, so each key's changes can be kept by revision, with retention matching what the store still replays. An alternative is to ask logd for the mirror at R (state at a revision) when R is older than what the view keeps.
- **A watermark.** The view knows the revision it has applied through. The mirror watch alone can't tell "no perms change at R" from "hasn't seen R yet", so the view follows the store's whole commit sequence: it applies mirror changes and advances past everything else.
- **Waiting, bounded, failing closed.** Serving from R waits until the watermark reaches R. Normally that's the watch's delivery lag, milliseconds. Past a bound, the read is refused as unchecked, as a check whose mirror did not answer is refused today. It never falls back to an older view.
- **Which R.** A read uses the revision the store served it at, where the store reports one. Otherwise it uses the latest revision this process has seen committed, which is conservative. A watch event carries its own (`Commit.Rev`).
- **What goes away:** `invalidate`'s read-back as the ordering mechanism, and the creation-window note at `narrow`.

## Plan

### 0. Probe

- Can the view get the complete revision sequence from the verse-wide stream, at what cost, including on a source's first pass (thousands of commits)?
- Does logd report the revision a read was served at (`Get`, `ReadAt`, listings)? If not, what is the tightest R available?
- Can logd answer the mirror at a past revision, and how far back relative to what a replay can reach?

Record the answers here before building.

### 1. The versioned view and the watermark — `entity`

### 2. Reads and watches checked at R — `entity` (`check`, `narrow`, `withhold*`, `ReadAt`, listings), with the bounded wait

Proves, each test run once with its piece removed:
- A record committed with its node at R is honoured by a read and by a live watch at R.
- A revoke at R: a replay of events before R still admits them, and events after R are refused.
- A grant at R: a replay of events before R still refuses them, and events after R are admitted.
- A stalled view refuses rather than admitting on stale perms.

### 3. Docs

`docs/perms.md`: what history a reader sees after a grant or a revoke.

## Contract

This changes what a replay shows after a grant or a revoke, in both directions, so it's a behaviour change for anyone replaying history. `Tier` to be declared where it lands on main.

Blocks grants on create: with the node and its record in one commit, this rule is what makes the record hold from that commit.