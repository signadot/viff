# residual: suspension — automatic holds, a lapsed holder, and the system charter

# Suspending: what is left

Status: residual of a landed change. What runs is described in [control](../control.md) — a run's
hold and a durable suspension, addressed by `verse flow --task | --charter | --subject` — and in
[the trigger model § Suspending](../trigger-model.md#suspending), for what a suspension does to
rules, gates, hooks and a lift. It answers an earlier issue ("control over
subjects and charters").

What is here is what the design and its two plans named and did not build, and what driving
`verse flow` against a verse turned up and left, so a known gap does not become an unknown one.

## 1. Automatic suspension

A holder can be the system, and the holder map is what lets a system's hold stand beside a
person's: a resume made by the system removes only its own. Nothing makes one yet. The first
candidate is the cycle throttle — a trip that suspends the rule, held by the system and lifted by
the system when the chain is repaired, rather than pacing it for ever — and after it a charter
suspended after repeated failed firings, or a subject while its source reports an error. Each is
engine code calling the route's function with a system principal, never a rule writing the record,
and none may lift automatically in a loop: a lift is a restart's worth of evaluation.

## 2. A scope deleted with its suspensions

Landed: `POST /scope/delete` cancels the scope's live tasks (`Engine.CancelLive`) before the
overlay and its suspension records go.

## 3. A parked launch across a restart

A hook's spawn parked by a suspension lives in a per-engine list. A restart before the lift loses
it: the hook's writes landed, and what they would have started does not. It is what a hook due
during a restart loses today, and closing it means a parked launch with a record, restored the way
a provider selection is resumed.

## 4. A lapsed holder's held work

What a suspension covers is asked each time work would make progress, so a holder whose authority
lapses stops covering new work at once. What was already held — a paused task, a parked launch, a
withdrawn question — was pushed once, and nothing releases it until somebody resumes. Making the
release follow the perms mirror is a watch on the holders' authority, and was left.

## 5. Suspending the system charter from outside

A rule the system installed (`by: system:charter`) is the system's alone to suspend. An operator
who needs one stopped edits the file the deployment names and re-applies; in an incident that may
be too slow, and what an operator's override of the system's rules should be is undecided.

## 6. Stored names are references

A suspension names a charter's address, a rule and a subject durably. Moving a package, renaming a
rule, or respelling an address grammar leaves one naming nothing, which fails **open**; the listing
marks it `covers: false`, and that is all. Every such change owes "respell the suspensions" in its
notice, as it does for grants, and nothing checks that it did.

## 7. The operator's hold across a restart

Whose hold a run is under lives in the engine that tracks it, and the task's record is written from
there. A restart re-attaches a surviving task and holds it if a suspension covers it, but does not
restore an operator's hold: a remote task stays paused at its runtime, with nothing on verse's side
saying whose hold it is, and a suspension's lift over it would not know to leave it. Restoring it
means carrying the mark across the restart, which the record does until the engine next writes it.

## 8. A run's hold keeps no reason

`--why` is asked of a durable suspension, which records it per holder, and not of `verse flow
suspend --task`: a run's hold is an operator mark on the engine tracking the task, and has nowhere
to keep one. So `heldBy: [operator]` says whose hold it is and not what it is for, and a second
operator finding a run held cannot ask the record why. Keeping it wants a place beside the mark on
the task's record, and the mark itself surviving a restart (§7) first.

## Found along the way

Read while designing suspensions or driving them, and not fixed by them.

- **A hook whose spawn needs a provider selection blocks its engine's reap loop** until somebody
  answers. A suspension parks such a launch before the selection is posed, so it is not made worse;
  unsuspended, the stall stands.
- **`reconcileTasks` adopts a surviving task before the charter is loaded**, so its hooks may never
  be re-attached at restart.
- **`clearPriorAnswer` removes a question's deadline `within` and `default` on an answered
  re-pose**, so a re-asked gate's deadline settles on no option. A suspension's re-pose is of an
  unanswered question and does not reach it.
- **The cycle throttle's "aborts if its chain breaks"** is wired only through the dormant dispatch
  loop; in versed a paced firing always waits its full interval.
- **A subject selector ignores a leading `verse` segment** when it is short enough for the three
  dimensions: `Match.MatchRef` compares system, kind and id and not the root, so `--subject
  'verse.task.*'` covers the world's `task.instance.x` too. A deeper selector is a path and
  compares the root. It is the flow selector's reading generally, and a suspension inherits it.
- **`still` and `Within` compare spellings.** A glob inside a segment contains only the same glob:
  lifting `demo.pr.hot-1` beside a standing `demo.pr.hot-*` does not say the pair is still held,
  though it is — the refusal a resumed run meets, and `flow show`, do say so.
- **A suspend's answer counts `holding` before a question's withdrawal lands.** The withdrawal is
  the firing's own goroutine noticing the cancel, so `verse flow suspend --charter walk:ship`
  answered `holding: {}` and the listing a moment later `questions: 1`.