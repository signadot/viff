# task: a withdrawn question leaves the open set

Follow-up from the #59 review (smaller points).

`p.open` (task/event.go) is cleared by `EventDecision` alone, not by the new `EventWithdrawn`.

So a reader more than 256 frames behind is re-sent the withdrawn question as pending. The mirror then sets the task record's `pending` to a dead question until the next decision, and a resolver following the stream sees it as open.