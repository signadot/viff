# A scoped task's `then` finds its runner after the task retires

Follow-up from the #59 review (smaller points).

`taskRunner` (versed/route.go), called from `followAsk`, reads the raising task's record from baseline once its in-memory entry has gone (retired after `hostedRetentionDefault`, 1h, or lost at a restart).

So when a rule task in a named scope raises an ask with `then: {spawn: …}`, and the ask is answered more than an hour after the task ended, the `then` is marked failed: "task … has no record, so where what follows its ask runs cannot be held to its runner". Before #59 the `then` ran on the runner it named, with no record lookup.