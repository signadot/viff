# gates: an orphaned question can be neither answered nor removed — it stands on the docket forever

Found on the first live run of both io halves (docs/working/work/io-live-run.md, finding 5).

A question whose engine awaiter is gone — a restart, a rule that no longer fires — stays pending
on the docket forever, and there is no surface that can end it.

- `verse gate <id> -answer deny -as <them>` answers `no such question`. The record stands, is
  `status: pending`, and is readable by the person it waits on; what `answerQuestion` cannot find
  is an awaiter, since `posed(store, id, raised)` comes back false and `raised()` — the engine's
  `Reg.Pending()` and `taskHoldingGate` — knows nothing about it. Two absent things, one message.
- `verse d verse.gate.question.<id> -rm` is refused: *"is verse's own: the entity write is for
  what the world is about, and what verse writes about its own running is written by the verbs
  that own it (charter -a, resolver add, runner add, source add, gate approve)"* — and
  `gate approve` is the verb that just 404'd.

So the docket is the one collection in verse with rows nobody can remove. The only thing that
clears one is a restart, which withdraws orphans at startup; a verse that does not restart
accumulates them.

Where: versed/server.go `answerQuestion` (the `posed`/`raised` pair and the 404 it shares with a
question that never existed), and the entity route's refusal of verse's own subtree.

Cost: two questions were observed in this state, one of them from the previous day. With the gate
charter installed, each one is also a question record read per person per pass, forever. They are
not delivered any more (fixed in `93092c10`), but the docket a person opens still lists a question
they cannot answer and cannot dismiss.

Worth deciding together: whether an orphan should be WITHDRAWN when it is noticed rather than
listed (the engine already has `untilMoot` and a withdrawal reason), or whether the docket should
admit an answer for a question nobody is waiting on — recording the decision on the record even
though no firing will hear it. The first looks right: the answer would reach nobody, and a docket
whose rows are all answerable is the property worth keeping.