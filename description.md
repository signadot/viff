# sweeps that file issues: logical races, docs conformance, and the filing act (working note)

Working note, 2026-09-13. The triage drive judges what lands in the tracker; this is the other
end — verse filing issues itself, from sweeps of the codebase given direction by what history
has just taught. Two halves of it exist: the drift drive is a sweep (a range reviewed, findings
in a ledger keyed by symbol, fixes proposed, main re-read to resolve), and the triage drive
judges any issue the machine files the way it judges yours, which is how junk would be seen.
What does not exist is the filing act, and a sweep with a theme and a scope.

## Why this codebase, and which class

verse has no transaction that spans two entities. Everything is one commit at one path, and the
model's promise is that rules react to commits, so every "and then" in the system is two commits
with a watcher between them: the owner record beside the entity, the latch beside the spawn, the
seed before the install, the mirror before the first write, the row the source writes and the
row a package derives from it. Each is right in one order and only that order, and nothing in
the substrate enforces the order. Idempotence and level-triggered replay make most of these cost
a re-run rather than a wrong answer; the ones that cost more are where the second act is also the
latch. These are LOGICAL races — `go test -race` sees none of them — and this week's fixes are
a checklist of their shapes:

- a latch written after the act rather than with it (the triage judge's `stage` in its hooks: a
  hook that timed out left the row selectable and a second judge spawned);
- a precondition somebody else must establish first (seed, then install: "a race the install
  wins");
- a watch opened after the write it had to see (the commit creating a node reaching a live watch
  before the record that would hide it; the perms mirror loaded after the first writes);
- one act assumed to be one commit (the entity and its owner record are two writes, and
  everything between them is a window);
- startup phases whose order is load-bearing and was not enforced until the retries had to
  carry it explicitly;
- a commit a rule was woken by and could not read at that moment, and never evaluated again.

## The detector that already exists

A test that passes alone and fails under the full suite is a logical race by definition. Two
stand today, `TestTheCausalChainCrossesTheLevel` and
`TestRemovingTheLastBindingSendsAQuestionToTheDocket`, and nobody has chased either. So the
sweep's mechanical method is the suite run repeatedly and beside itself — `-count` and two runs
in parallel, the closest thing to production load a laptop has — and what flakes is a finding,
with the test's name as its symbol. No tokens.

The reading method takes the shapes above as its hint and looks for the same shape elsewhere:
a hook that writes the latch, a derive whose consumer runs before the derive is durable, a watch
opened after a write, two writes treated as one. The direction from history is literal: the
hint can be built from the commit messages of the fixes, since this repository's messages say
what the race was in plain words.

## The shape

A package `acme.sweep`.

**The ask row** is the direction, written by a person, one line:

    # acme.sweep.ask.races-2026-09
    theme:  races
    method: both            # flakes | reading | both
    since:  d1780aa5        # or `whole`
    hint:   "six shapes from this week's fixes — see the note"

**The sweep rule** spawns a task on the coderunner for each ask without a report: `grant:
no-delegate`, a themed prompt, the answer contract the drift drive uses — `findings:
[{symbol, says, at}]` — derived into a ledger keyed by symbol, so a second sweep updates a
finding rather than filing it twice. For `flakes` the task runs the suite and reads the
failures; for `reading` it reads with the hint; `both` does both and merges.

**The filing rule** is off the ledger: a finding with `status: open` and no `issue` spawns the
filing, which writes the issue and patches its id onto the finding. Filing is the missing act:
a tool on the coderunner, `file_issue`, that writes `description.md` and `meta.tony` and pushes
`refs/issues/<xidr>` with the credential the runner already holds — a `Push` effect on a git
ref, so a gate matches it and a grant refuses it, and the machine needs nothing new. The filed
issue reflects back through the git-issue source within a minute, and the triage drive judges
it: the loop closes on its own.

**A gate on filing**, `ask`, with `answerers:` set to whoever wrote the ask row, until a few
sweeps have shown their findings are worth the tracker. Directed gates exist for this; it is
what keeps "automatic" from meaning "unread".

**The cumulative trigger.** "After substantial changes" is a fact in the store: the drift
range's `report.files`, summed since the last docs sweep. A rule that derives an ask row when
the sum crosses a line — `{theme: docs-conformance, since: <the last sweep's sha>}` — makes that
case automatic. The race sweep stays on the explicit ask; its signal is in a person's head and
in the closed issues, not in a count.

## Order

1. `file_issue` on the coderunner. Everything after it is charter, testable against the store.
2. The ask row and the `flakes` method, since it costs nothing and the two standing flakes are
   its first findings.
3. The ledger and gated filing.
4. The `reading` method with the shapes as hint.
5. Docs conformance on the cumulative trigger, mostly by pointing docs-drift at a themed range.

Read the first sweep's findings together before the gate comes off — the triage drive's first
two judgements were read the same way.

Related: docs/working/issue-triage.tony (the other end), docs/working/comment-drift.tony (the
ledger and the review shape this reuses), sfwhh9svh12ks45zmnn0 (the sixth shape),
4jhyq24nh12kszvjmsn0 in tony-format (a watch that outlives its client — the substrate's own
member of the class).