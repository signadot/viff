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

## The three risks, in the order they will bite

### 1. The install storm, and it is not just cost

50 commits are reflected the moment the source starts. Installing this charter makes all 50
pending at once, and a runner **has no queue and no capacity**: it takes a spawn or it does not
(`docs/runners.md`). What it does not take fails, `onFailed` writes `reviewed: failed`, and
nothing re-arms it — so a storm does not merely cost tokens, it permanently marks commits as
failed at whatever rate the runner could not absorb.

Two halves, and both are needed:

- **Bound the window at install.** `Git.commitDepth` is the knob (50 today, not configurable
  from a source entity — check). Set it small for the first install, let the flow catch up, then
  raise it. This is operational and honest; there is no charter-grammar way to say "only recent
  ones", because a charter cannot order shas and the payload carries no timestamp.
- **Make a failed review re-armable**, which is a defect the flow already has at N=1 (a runner
  down when one commit lands is the same permanent `failed`). Two candidate mechanisms, neither
  free: a rule that patches `failed` → `pending` loops instantly with no backoff and no counter,
  and there is no arithmetic in a patch to hold a try count; a gate with `!until [… !deadline]`
  has a clock but asks a person. **This wants its own decision before the storm can be called
  handled** — file it and settle it separately.

### 2. N reviews in parallel over one ledger

Every commit of a push gathers the same `outstanding` list and reviews against it, concurrently.
Two commits that both touch a lying comment each file a finding for that symbol. They do not
collide — findings use `key: $item.symbol`, whose id is composed under each review's own subject
— so the ledger gets one finding per (commit, symbol), which is arguably the truth and is
certainly more rows than today. Worth seeing before deciding it is wrong.

### 3. The flow reviews its own commits

The train lands `verse/auto/*` on main, which produces commits, which get reviewed. That is
correct — a fix commit should show the drift resolved — but it means every auto-fix push costs
another review, and a merge commit (two parents) is a shape the review prompt has never been
given. Bounded, since each commit is reviewed exactly once, but it is a feedback edge that does
not exist today.

## Order of work

1. **Harness first.** `drive-drift.sh` `move_head` becomes `land_commit`: write
   `git:commit:verse/<sha>` and then move `git:head:verse`. Scenario 3 already writes both
   commits and moves the tip once, and its assertion flips from `~ KNOWN DEFECT` to two tasks
   and two findings. Scenarios 1 and 2 are unchanged in meaning. ~15s per run.
2. **The local charter** (`drift-local.tony`): subject swap and the `drift.head` → `drift.commit`
   rename. This is the whole change; if it does not fall out in three edits, the shape is wrong.
3. **Prove it.** Scenario 3 green, 1 and 2 unregressed.
4. **The re-arm decision** (risk 1's second half) — separately, and before this goes to staging,
   because staging is where a storm is expensive.
5. **The real charter** (`comment-drift.tony`): the same three edits, plus `head-moved` deleted.
6. **Staging with `commitDepth` lowered**, then raised once the backlog is reviewed.

## What this does not address

- **GC.** Records accumulate one per commit forever; commits are covered by nothing, so the
  source never retracts them and neither does anything else. Deferred deliberately.
- **A finding's lifetime.** Nothing closes a finding except a later review saying so, and that
  is unchanged here.
