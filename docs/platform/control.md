# Control on the platform

This page is how the runtime holds and suspends work: the routes of a hold, and what a
suspension does to rules. What a user types and sees is in [control.md](../control.md).

## A hold on a run

The routes of the runtime, `POST /tasks/{id}/pause` and `POST /tasks/{id}/resume`, are the same
hold.

The engine that tracks the run remembers whose hold it is, and a restart does not restore it.

## Suspending

A suspension is one record naming a charter (or every charter), a rule of it (or all), and a
subject (or every subject), and it holds the work about that subject in every form until it is
lifted. What an operator does with one, and how it differs from holding a run, is in
[a suspension](../control.md#a-suspension). This section is what a suspension does to rules.

```tony
# verse.trigger.suspension."repos.verse.drift.**:*@git.*.head"
charter: repos.verse.drift      # an installed charter's address; "*" for every charter; "" for baseline's
recursive: true               # …and every charter installed beneath it
rule: "*"                     # a rule's name in it, or "*"
subject: git.*.head           # an address, * open in any segment, covering at or beneath; "*" for every subject
held:
  user:you@example.com:
    why: "reading the drift charter's range logic before it runs again"
    since: "2026-09-14T03:10:00Z"
    occurrence: 41872         # the store revision the hold landed at
```

The record is keyed by what it covers, addresses parsed and rendered by segment, so two spellings
of one selector are one record, and suspending the same thing twice adds a holder to it.

**A rule about to fire** is asked in three places. At the top of evaluation, before the rule's
watermark moves, a rule suspended about every subject is skipped; so the revision it skipped is
still one a re-arm's replay is admitted at. Per subject, a firing about a suspended subject is
refused before the cycle throttle sees it, so a firing that was not going to happen counts no
trip; and again on entry to a firing, since a paced firing waits out the throttle first. Every
spawn and every charter-authored write pass a backstop. Nothing asks in subject selection, which
is what decides a question has gone moot: a suspension there would moot work rather than hold it,
and a rule that stops selecting its subject during a suspension ends that firing as it would have.

**A firing at its gate** has its question withdrawn — orphaned, with the suspension as its `why`,
ballots and deadline stamp kept — and waits holding its flight. When the suspension lifts it asks
the same question under the same id if the rule still selects the subject, with the time its clock
had left.

**A completion hook or a `seq` step** lands its writes, since recording what a task did is not
progress, and parks what it would start, so a hook asks nothing while suspended and the
engine's reap loop never waits on one.

**A lift is ordered.** The record is deleted, and the suspension drains: new firings about its work
are still refused while what it held is released — tasks resume, parked launches start in order,
withdrawn questions are asked again — and only then is it removed and every rule it covered armed
again, its watermark first moved below the head, the path an install and a restart take. So a rule
acts on the world as it stands when the suspension lifts, and nothing that happened meanwhile is
replayed: a level rule still matching acts once, one whose condition came and went does nothing,
and an `on` edge that rose during the suspension is not seen, as an install over a verse where the
predicate already holds sees none.

**Whose it is.** Each scope's suspensions are one table in memory, written only by the route that
writes their records and loaded before the scope's tasks are re-attached or its charter armed;
until it has loaded it holds everything. A rule `derive` aimed at `verse.trigger.suspension` is
refused at install, since no table would see it. What a suspension covers is decided each time it
would apply, against the holder's `control` over the work's charter or at its subject, and a rule
the system installed (owned by `system:charter`) is the system's alone to suspend
([perms](../perms.md#suspending)).
