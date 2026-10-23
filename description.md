# verse: adopting logd scratch across the board, and the commit rate behind staging snapshots

Adopting logd's scratch (tony-format r482sz5r) across verse, and what it does for staging's snapshots.

## The cost being removed

Staging commits about one write a second (commits 128634 → 136019 over 2h, 2026-10-23 08:40–10:40). logd snapshots the whole document every 1000 commits or 4 MiB of delta, and keeps every snapshot inside the compaction cutoff (staging: 1d). The document is at least 8.2 MB: what `user:scott` can read is 8.2 MB, and `verse.*` (tasks, asks, perms) is unmeasured on top. So a day holds ~85 snapshots of 8+ MB. That is most of each 615 MB log, and every compaction rewrites all of it: one cycle starts as the last ends, ~25 min each. (index.regions is 6.0 GB beside 1.2 GB of logs, which deserves a separate look upstream.)

The snapshot volume is (commits/day ÷ 1000) × document size × cutoff. Three levers, then, and scratch pulls the first.

What the commits are (2,301 slow commits in those 2h, by path shape):

| path | slow commits | what it is |
|---|---|---|
| `verse.schedule."repo-charters.<repo>:minute"` | 629 | each reconciler's minute tick |
| `repo-charters.<repo>.livecheck.live~status` | 495 | the git iod's answer to the minute's live-check |
| `repo-charters.<repo>.out.git.obsolete~status` | 338 | the git iod's answer to the minute's obsolete sweep |
| `verse.iods.github.out` (+ slack 54) | 330 | the out half's pass report, every pass |
| `repo-charters.<repo>.out.git.obsolete` | 119 | the sweep's input, re-derived each minute |
| `verse.schedule.minutely` | 103 | |
| `repo-charters.<repo>.livecheck.live` | 63 | the live-check's input, re-derived each minute (`round`) |
| `repo-charters.<repo>.out.github.live-drift` | 46 | |
| everything else | ~180 | |

Most of it is the reconciler asking the same question every minute and writing the unchanged answer down, plus the out half reporting each pass. Only the last row is scratch material as things stand: the rest is watched.

## What goes in scratch

A field belongs in scratch when all four hold:

1. **Nothing watches it.** No rule's condition, no stratum, no versed watch reads it. Scratch has no watch and never will (Scott, r482 10:16). What a consumer must be told about goes in the document.
2. **Only its current value matters.** Nothing reads it at a commit (AsOf), nothing wants its history, and nobody audits who wrote it (a scratch write has no author).
3. **No precondition guards it.** Scratch has none; two writers race and the last one wins.
4. **Losing it costs a repeat.** A crash loses the last flush tick (~1s) of writes, so whatever is lost is written again by the next pass anyway.

These are, in practice, the *when-last-confirmed* fields: `lastPass`, `checkedAt`, pass counts, a round's echo. Verse already treats the in half's `lastPass` this way: it is read from the live reflector and never written, "because it is an opinion about now" (versed/io.go). Scratch is that rule for opinions that must survive a versed restart.

## The pattern: a record's *what* in the document, its *when* in scratch

A status splits in two at the same address. What it says (verdict, sha, error, delivered-or-not) stays in the document and is **written only when it changes**. When it was last confirmed goes to scratch at the record's own address, every pass. A watcher sees changes and nothing else. A reader that wants "is this alive" reads the scratch half, when the document half (or a person) says to look.

The second half matters as much as scratch does: a document field that changes every pass (a timestamp, a counter, a round) makes every pass a commit, and moving the field out is what lets the elision work.

## Where it applies

1. **The out half's pass report** (`verse.iods.<name>.out`). The record holds the out half's CONFIG, `places` and `as`, which the install's `sends` check reads. The pass report (`delivered`, `failed`, `lastPass`) is merged into the same record. Only the report moves: `lastPass`, `delivered` and `failed` go to scratch. `lastError` stays in the document, written once per transition as now. (r482's Phase 8 says "verse moves `verse.iods.*.out` to scratch", and moving the whole field would take the config with it; worth a line on r482.) `verse iod` reads both halves.
2. **Outbox statuses, generically** (io/out.go). `Sink.Deliver` answers the fields to merge. It answers two sets instead, the status and the seen, and `Out.Write` merges the seen into scratch every pass and writes the status only when a field differs from what stands. `fail` already elides a repeat failure; this makes the same true of a repeat success. The per-round fields of the git iod's check (`checkedAt`, the `round` echo) become seen fields.
3. **The reconciler's minute rounds** (system/repocharters). This one is a charter change, independent of scratch, and the biggest row. The live-check input carries `round: <due>`, so each minute re-derives the input (63) and the iod answers once per round (495). `differingFor` counts rounds, so the answer changes every minute verse/live differs. The obsolete sweep's input carries `before: <due>` likewise (119 + 338). As standing orders, the input says *what* to check and the iod's own pass says *when*: no round in the input, an answer written on change, and "two rounds in a row at one commit" said by the answer itself (first seen differing at sha X, then confirmed at X). The minute tick then drives nothing in the live-check, and the sweep's `before` becomes the iod's read of the questions at the pass. Of the 2h's slow commits, ~1,000 go.
4. **Schedule ticks** stay in the document: they are events, rules watch them, and the tick is written under a precondition. After (3) the per-repo minute schedules may have nothing left to drive. If some rule still needs them, one shared minute schedule replaces six (629 → ~105).
5. **Not candidates:** task and ask records, claims and leases (preconditions), ticks, any `~status` a rule conditions on (`livecheck.*`), the perms mirror, and the reflections, which are what rules read.

## The mechanism in verse

- **`entity.Scratch`**, beside `entity.Store` and not inside it: `ScratchWrite(at []string, v Payload) error` and `ScratchRead(at []string) (*ir.Node, bool, error)`. The logd store implements it over libctl's `ScratchWrite`/`ScratchRead`, and entitytest's stores in memory. It has no Write, no When and no commit: nothing on it can be mistaken for a document write.
- **The address is the record's.** The scratch half of A lives at A in scratch's namespace, which is separate in logd, so the same spelling is free. Perms are checked by versed at A, as for reading or writing A, since docd knows no principals and versed is the one check (attribution.go). Writes are made as verse, as `passIO` already writes the report.
- **Wire:** a route of its own, `GET`/`PUT /scratch?at=`, never merged into `/state`, so a read of the document is never a read of scratch, and the CLI and MCP mirror it (`verse data --scratch`). The `verse iod` listing reads both, by name.
- **Charters cannot reach scratch.** Conditions name document paths and there is no scratch expression; none is added. Nothing to refuse.
- **Deploy:** `scratch.tony` sits beside the log on docd's PVC. A wipe clears it, and `deploy/carry.sh` carries nothing from it. Check the wipe path against the ship path (wipe + repopulate + sources).
- **go-tony:** verse requires the go-tony that has scratch: an older logd answers `invalid_message`. A `contract` tier where it lands; no fallback.

## Order

- (3) can start now, with no go-tony change. It is a reconciler template change plus the git iod's check; it removes the biggest share of commits and needs your eye on the protocol.
- When go-tony with scratch is out: the bump, `entity.Scratch` with both implementations, the `/scratch` route with perms and `verse data --scratch`, then (1), then (2).
- Then measure commits/min and snapshot cycle length on staging, before touching the cutoff.

## The snapshots themselves

Besides the commit rate:

- **The cutoff.** 1d was chosen to outlast the waits a gate, hook or sequence step makes before reading at its causing commit (deploy/build.tony). Past the cutoff a read is answered approximately, at a nearby snapshot (cpqj2tf6). Your r482 reading, that past reads of volatile state should just answer current, says the same for the record classes here. 6h would cut it 4×; what reads further back than 6h is the question.
- **The document's size.** The GitHub reflection is 4.0 MB: example/app's comments are 0.8 MB, and PRs and comments across repos most of the rest. git-issue is 3.0 MB (tony-format's 397 issues, 1.7 MB). Each of those bytes is in every snapshot. Reflecting less (closed PRs and their comments, issue bodies already closed) shrinks every snapshot at once.
- **Upstream:** a root snapshot re-encodes subtrees that have not changed since the last one. logd already writes path snapshots (`path snapshot created path=verse.verse.iods`). A root snapshot that refers to an unchanged subtree's last snapshot instead of copying it would make a snapshot cost what changed. That would be a new tony-format issue, not r482.