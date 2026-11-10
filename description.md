# versed: two launches together, and the second is refused as "no runner offers it" while the first rewrites the runner's description

Two spawns that reach the same runner at once can fail the second with `no runner offers it … not described`. Two rules firing on one commit is enough. `TestATaskActingForTheAskerAsksThem` fails on it under load.

## What happens

Every launch asks the runner to describe itself again (`handshake`) and keeps what it said (`recordOffers` → `writeDescribed`). `writeDescribed` is two writes: remove `described`, then write it. Between them the registration has no description, and routing passes over a registration that is not current. The comment on `writeDescribed` calls that safe because a spawn arriving then "goes elsewhere or is refused naming it". With one runner, refused is what happens, and the task ends failed:

```
status: failed
err: "spawn for-the-asker@ask.one: no runner offers it: no runner offers scripts; passed over:
      verse.runners.local, not described (`verse runner check local` says why)"
```

The test waits for the task's question and reports `never asked`; a 40 second wait does not help, since the task is over. Reproduced by running the test in 20 processes at once, three times each: 5 of the 20 failed.

It also means every launch costs two writes to the registration, a kick of every engine and a pass over the waiting system charters, for a description that is the same as the one standing.

## Fix

`recordOffers` leaves the description standing where the runner says the same as it did. That is every launch but the one after the runner changed, so the window is met only when what the runner offers really is changing.

## Not in this

A runner whose description does change is still without one between the two writes. One write that replaces a value whole would close that; the store has none today.

## Done when

- The stress run above passes: 20 of 20.