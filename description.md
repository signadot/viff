# cmd/verse: task attach reads a line written before a gate as typed at it, and leaves the prompt open when input ends

`verse task run --attach` with its input piped can take a line written before a gate as an attempt to answer it, and can leave the gate's prompt open with nobody to type at it. `TestALineTypedBeforeAGateIsNotItsAnswer` fails on both under load (named in two commits, the latest on 2026-11-09).

## What happens

The attach loop selects over typed lines and stream frames. Three things go wrong when the gate's frame is handled before the input that was already there:

- **The reader starts late.** `readLines` is started after the stream has been asked for and answered, so frames can be waiting before the first line has been read.
- **A select with both ready takes either.** A line in hand can lose to the frame that raises the gate, and is then read as typed at the prompt: `hello` at a `[y/n]` prompt is "not an answer", the prompt is shown again, and the line never reaches the ask it was meant for.
- **End of input leaves the prompt up.** A gate's prompt opened before EOF is seen stays open after it. Nothing says how to answer the question from elsewhere, which a gate met after EOF does say.

The test's two failures:

```
never saw "answer it: verse question approve":   … ⏸ gate "autonomy[0]" (run_bash: echo ran) — approve? [y/n]
the attach never ended:                            … approve? [y/n]   ⏸ … approve? [y/n]
```

Reproduced by running the test in 24 processes at once: 1 or 2 of 24 fail.

## Fix

- Input is read from before the stream is asked for, into a channel with room, so what was written ahead is in hand when a frame arrives.
- Before a frame is handled, the lines in hand are taken first: they were typed before whatever the frame raises.
- At end of input an open prompt that is not a conversation's turn is closed, saying how to answer the question.

## Not in this

A line that the reader has not yet handed over when the frame is handled is still read as typed after it. For a person at a terminal that is correct. For piped input it is a window the first two changes make small and do not close.

## Done when

- The stress run above passes, six times each: 24 of 24.