# verse gate is confusing

Two things about `verse gate` that read wrong from outside, met in one sitting:

    $ verse gate rollout-9e30c4b5
    at: verse.gate.question.rollout-9e30c4b5
    payload:
      answeredAt: "2026-09-10T17:18:56Z"
      by: human:ana
      delivered: false
      gate: rollout
      status: answered
      verdict: canary
      ...
    $ verse gate
    []

## You can GET what you cannot LIST

One command, one noun, two populations. `verse gate <id>` reads any question ever recorded;
bare `verse gate` lists the PENDING ones. Both are defensible on their own and together they
say the docket is empty about a gate the same command just printed in full.

Nothing in the output marks which population a record belongs to, and nothing in the empty
listing says what it is empty OF. A reader's first conclusion is that something is broken —
the question was lost, or the listing is — and the only way to find out otherwise is to read
the source.

## `delivered` is a distinction the reader does not have in mind

`delivered: false` is the whole story of that record and it is one field among fifteen,
spelled as a bare boolean with no hint that it is the interesting one.

What it means: the answer was ACCEPTED and the gate closed — nobody else may answer — but the
worker that must act on it dials in and has not yet pulled it. It is sitting in the task's
inbox. `AnswerQuestion` REMOVES the field when the answer went straight out, `QueuedAnswer`
sets it false while it waits, so ABSENT means delivered. A reader who has not read
mirror/question.go will not reconstruct that, and the polarity is the kind that gets read
backwards: `delivered: false` looks like a failure rather than a stage.

The field exists for a good reason, stated where it is written: it "is what let an answer
evaporate with a worker that never came back, while every surface said the question was
settled." That reason is only half spent. The record is now honest, and no surface reads it —
`verse gate` lists pending only, `verse status`'s `gates:` counts pending, and nothing
anywhere lists an answer that was accepted and never collected. So the state it was added to
make visible is still visible only to somebody who already knows the id and asks by hand.

An undelivered answer also has no clock of its own. The `deadline` on the record governs
ANSWERING; if no worker ever dials the task's dialURL, the answer sits in the inbox
indefinitely, the task stays `running`, and nothing says so.

## Roughly what would fix it

  - the empty listing says what it is empty of, and says whether other questions exist;
  - answered-but-undelivered is listable — a line in `verse gate`, or `--undelivered`, and a
    count beside `gates:` in `verse status`;
  - the record says its stage in words rather than by the presence and polarity of a boolean.

Read-side only. The data is already there and already correct.