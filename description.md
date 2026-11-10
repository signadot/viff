# mirror: a worker's decision frame that arrives before the answer is recorded is written over it, and the question says the runner decided

A question a person answered can end up recorded as answered by the runner. `TestWordsToATaskWaitingOnYourTurnAnswerIt` fails on it under load, and is the test most often named as a flake in recent commits (five, the latest on 2026-11-09).

## What happens

The answer route claims the question (`followed`), delivers the answer to the task, and then records it (`AnswerQuestion`: status, `by`, ballot). The delivery makes the worker send a decision frame, and `settleRelayed` (`pkg/mirror/mirror.go`) decides what that frame means by reading the record: answered with a `by` means the route accepted an answer and the frame is an acknowledgement; anything else means nobody was asked and the worker settled it.

The frame can arrive between the delivery and the route's record. `settleRelayed` reads a pending question, the route writes alice's answer, and `settleRelayed` then writes `by: runner:local` over it. The record keeps alice's ballot and alice's claim, and says the runner decided:

```
the turn settled reply by runner:local, want alice's reply:
  ballots: {user:alice@example.com: {choice: reply …}}  by: runner:local  followed: {by: user:alice@example.com …}
```

Reproduced by running the test in 24 processes at once, four times each: 22 of the 24 failed.

## Fix

The claim is how `settleRelayed` knows an answer was accepted. It is written before the delivery, so it stands before any frame the delivery causes. A claim whose delivery failed (`undelivered`) is not an answer that arrived, and what the worker then settles is still recorded as the worker's.

## Done when

- The stress run above passes: 24 of 24.