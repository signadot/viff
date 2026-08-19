# Reviewing every commit, not just the tip

The comment-drift flow reviews a push's TIP and nothing else. A push of three commits reviews
one of them; a comment that started lying in the middle commit is never looked at, and if an
earlier review filed a finding that the middle commit fixed, the finding stays open forever
because nothing re-reads it.

`docs/working/verse-dev/drive-drift.sh` scenario 3 holds this still: two commits, one push, one
task. It is the harness's only remaining `~`.

## Why it skips

The chain starts at `git:head:verse`, which names ONE commit per push — the tip. Everything
downstream is a function of that subject, so a commit that never becomes a subject never becomes
anything.

The material to fix it is already in the verse. The git source reflects a bounded tip window as
`git:commit:<repo>/<sha>`:

```
git:commit:verse/a0a53cbf  {repo: verse, sha: a0a53cbf…, msg: …, author: …, parents: […]}
```

`git log -n 50` on the mirror, so: main's history, tip first, 50 deep (`Git.commitDepth`).
Nothing reads them today.

## The shape of the fix

Fire the review chain on the COMMIT rather than on the head. One record per commit, which is
already what the record is keyed by — `id: $subject.value.sha` — so the addressing does not
change at all. Only the subject does.

```tony
# was: condition: {subject: git:head:verse}
- name: commit-landed
  stratum: reflected
  condition:
    subject:
      path: [git, commit, "*"]
      value: {repo: verse}
  action:
    derive:
    - path: [verse-dev, drift, commit]      # was drift.head
      id: $subject.value.sha
      value: {repo: $subject.value.repo, sha: $subject.value.sha, reviewed: pending}
```

`gather` and the review rule follow the record, so they need only their path renamed. The train
half does not move: `train-cut` still cuts on `git:head:verse` (one train per tip is correct),
and `says` still reads the tip's record — the tip is a commit like any other, so
`at: [verse-dev, drift, commit, $subject.value.sha]` is the same read at the new name.

`head-moved` goes away. Nothing else reads `verse-dev.drift.head`.

### What that buys

- every commit on main gets a record, a task id, and a review, exactly once;
- a finding filed against one commit and fixed in the next is seen, because the next commit is
  reviewed with the ledger gathered onto it;
- `git:head:verse` stops being load-bearing for review, which removes the last place where "the
  push" and "the commit" were conflated.

## The width, measured

The note at the top of `comment-drift.tony` rejected this shape once: *"a rule over `git:commit`
is as wide as the reflected history: 131 commits on staging, all 131 firings timing out inside
the store, every derive lost."* That is the risk this plan reintroduces on purpose, so it was
measured rather than argued about (`scratchpad/stress-width.sh`, N=131, local):

| phase | what | result |
|---|---|---|
| A | 131 commits land, no charter | 2s — 15ms each |
| B | install the charter; 131 fire at once | 131 records by **14s**; tasks 0 at 14s, 0 at 49s, 22 at 68s, 41 at 90s |
| C | 131 more with the charter running | abandoned — minutes, with reads timing out throughout |

**Nothing is lost.** All 131 derived, all 131 gathered, tasks arriving steadily. The note's
"every derive lost" does not reproduce — what reproduces is the latency, and a first pass
misread that as loss because `verse data --paths` defaults to depth 3 while these records are
four deep, and because a store that cannot answer prints an empty list rather than the error the
long form gives (issue `tyr7f6dhh12ksdd4gnn0`).

**The verdict: a window cannot arrive at once.** Not because anything is lost — it all
completes — but because it takes minutes and degrades reads while it does, and phase C says a
loaded store makes it worse rather than better. The steady state is untouched: a push of one to
five commits is five spawns at ~1/s, which is what the flow already does today.

**The derive half is cheap and the spawn half is the wall.** 131 records in 14 seconds; then the
spawn rule fires nothing for the better part of a minute and trickles at ~0.9/s, which is about
what a single spawn costs end to end — so spawns do not parallelize. A push of one to five
commits is unaffected; only a window arriving at once is. Two hypotheses were checked and are
wrong: dispatch is already parallel (`go e.fireOne` per subject in `fireSubjects`), and the
cascade throttle did not trip (`DefaultTripPace` is exactly 1s and matches, but there is no
`cycle:` line and the chain is 3 deep against a cap of 4). Something inside the spawn path
serializes what the dispatcher parallelized — issue `bteb1a97h12kr7nagnn0`, with the isolating
experiment written down there.

**What that costs in writes.** verse's own log showed ~3900 store commits while working through
131 source commits — roughly 30 writes per commit once the record, the gather patch, the task
run entity and its state transitions are counted. At width 131 that is ~4000 commits arriving
together, per-commit latency climbing (295ms → 1.05s in the slow-commit warnings), and that is
when a concurrent read times out.

## The install, in two steps — and the derive that made it possible

The seam the measurement points at: **arm the review after the baseline, not before.**

1. install `commit-landed` alone — one record per reflected commit, ~3s for 60, ~13s for 131;
2. bulk-set those records to `reviewed: baseline` (a loop over `--paths`, ~1s; there is no
   place-level bulk form);
3. install the review rules **without re-applying rule 1**. Nothing arms.

Driven: `docs/working/verse-dev/two-step-install.sh`. 61 records stayed baselined for a full
minute with the whole review chain installed, and the next commit to land got exactly one task,
six seconds later.

This is the charter equivalent of `sync.Once`, and it is an install step rather than a rule
because a rule cannot know it is the first firing. The two pieces verse already has are the
write-once (`verse data … --put --create`) and the latch (`!not.has-path` over the charter's own
slice), and neither can bootstrap itself: something has to create the thing being latched on.

It also does NOT need ancestry, which is what makes it expressible. "Review commits after H0"
needs an ordering a charter does not have — no comparison on shas, no timestamp in the commit
payload — but "everything reflected right now is accounted for, and only what appears later is
new" is a set, not a point, and the record per commit already IS that set.

It supersedes lowering `commitDepth`: there is no need to shrink a window that costs seconds to
derive.

### The derive must not name the flow's state

The first attempt at the above failed, and the reason is worth more than the fix. Rule 1 derived
`reviewed: pending` along with the facts. **A derive is a tony patch merged at depth, not a
replace** — it overwrites exactly the fields it NAMES — so every install rewrote every record's
`reviewed` and re-armed every review that had already run. The baseline lasted until the sweep
reached it; the new commit's spawn was starved behind 131 re-gathers.

So rule 1 now states only what it owns, and rule 2 starts on `!not.has-path reviewed`. The start
state is that nobody has written one, and an absence is nobody's to overwrite.

**Which spelling of absence** is not a detail: `!not {reviewed: null}` cannot be stored — verse
writes absence as a bare null and hides every bare null on read, so the durable rule comes back
as `!not {}` and selects nothing, working until the first restart and then silently stopping.
That warning is in `comment-drift.tony` and it cost a drive an evening. `!not.has-path` is the
spelling that survives, driven in `docs/working/verse-dev/restart-latch.sh`: a commit is picked
up, verse goes down and comes back, the stored rule still reads `!not.has-path reviewed`, and
the next commit is picked up too.

This was a latent defect in the flow before any of this: at width one, a charter edit re-armed a
single review and nobody noticed. Issue `2c6z0ahwh12kraxbgnn0`.

## The remaining risks

### 1. A `failed` review is not re-armed

A runner has no queue and no capacity — it takes a spawn or it does not (`docs/runners.md`).
What it will not take fails, `onFailed` writes `reviewed: failed`, and nothing re-arms it.
**Accepted for now**: `failed` is a sync state a person can read, and it is the same state the
flow already reaches at one commit when a runner is down. The retry mechanism — a `failed` →
`pending` rule loops with no backoff and no try count; a `!deadline` gate has a clock but asks a
person — is a separate decision and does not gate this work.

### 2. N reviews in parallel over one ledger

Every commit of a push gathers the same `outstanding` list and reviews against it, concurrently.
Two commits that both touch a lying comment each file a finding for that symbol. They do not
collide — findings use `key: $item.symbol`, whose id is composed under each review's own subject
— so the ledger gets one finding per (commit, symbol), which is arguably the truth and is
certainly more rows than today. Worth seeing before deciding it is wrong.

### 3. The flow reviews its own commits

The train lands `verse/auto/*` on main, which produces commits, which get reviewed. That is
correct — a fix commit should show the drift resolved — and it has already happened on staging:
the flow corrected its own fix, once, and did not loop. Bounded, since each commit is reviewed
exactly once. A merge commit (two parents) is a shape the review prompt has never been given.
Taken later if it wants taking.

## Order of work

1. ~~**Harness first.**~~ Done. `drive-drift.sh` splits `move_head` into `land` (writes
   `git:commit:verse/<sha>`) and `push` (land, then move the tip). Scenario 3 asserts both
   commits of one push are reviewed.
2. ~~**The local charter**~~ Done. `drift-local.tony`: `head-moved` → `commit-landed` on
   `git:commit:*` where `repo: verse`, and `verse-dev.drift.head` → `.commit` throughout.
3. ~~**Prove it.**~~ Done — 11/11, scenario 3 flipped from `~ KNOWN DEFECT` to ✓, ~20s per run.
4. ~~**The real charter**~~ Done. `comment-drift.tony` carries the same shape.
5. **The two-step install**, above — a script or a documented sequence, and whichever of
   `verse data --put --if` at a place or a loop over `--paths` does the baseline in one go.
6. **Staging**, in that order, watching that the review rules arm on new commits only.

## Arranging the ledger by symbol, and why it is a source's job

Risk 2 above is one finding per (commit, symbol) — the same lying comment filed once per commit
that touched it. The alternative is to arrange the ledger BY SYMBOL: one entity at
`verse-dev.drift.finding.<symbol>`, which is `id:` where the rule uses `key:` today. Concurrent
reviews then converge on one row, a symbol's status has one place to live, and "outstanding" is
a set rather than a bag.

What it costs is the finding's LIFETIME. Keyed under a review, a finding is at least attached to
something whose provenance is known. Arranged by symbol, deciding a finding is dead means
knowing the symbol is gone — which needs the live symbol set of the codebase, and nothing in the
verse has it. A charter cannot enumerate symbols; a SOURCE can. A code source reflecting
`code:symbol:<repo>/<symbol>` and COVERING that kind would retract a symbol that no longer
exists, and a finding derived from the symbol entity would go with it — which is the ordinary
retraction path rather than a GC pass anybody has to write.

So: by-symbol is the better arrangement and it is blocked on a Go source, not on the charter.
Noted here and not attempted.

## What this does not address

- **GC.** Records accumulate one per commit forever; commits are covered by nothing, so the
  source never retracts them and neither does anything else. Deliberately after the live flow
  works, assuming an infinite store.
- **A finding's lifetime.** Nothing closes a finding except a later review saying so, and that
  is unchanged here — see the by-symbol section for where it goes.
