# coderunner: a foreign agent's run narrates nothing, so the only thing compensating for an opaque leaf is unbuilt

Finding 6 of the coderunner drive, kept whole because the note that held it leaves
with the docs/working cleanup and this half is not done.

## The finding, as the drive recorded it

`--attach` shows a boundary, the gate, the decision — and then nothing at all for as long as the
agent works. The driver invokes the binary with `--output-format json`, which returns one
document when the whole turn finishes, so the executor has nothing to emit until it is over. An
operator watching a ten-minute task sees a task that might be dead.

This is worse than an ergonomic gap, and the design note said so before the code did:

> Narration onto the read plane. The agent's streaming output maps onto `exec` frames, so `verse
> task get` shows which commands ran inside the foreign agent — the observability that a
> containment-by-absence design otherwise gives up.

The reasoning still holds and the code went the other way. verse gates the *invocation* and not
the agent's steps — deliberately, because it cannot see inside one — so **narration is the entire
compensating mechanism**. Without it, "verse supervises this work" is true only at the boundary
where the work starts.

## Why it is not mechanical

`--output-format stream-json` is the fix, and it carries one real decision rather than being a
flag change: how much to emit. Every tool use is faithful and chatty, and a task's read plane has
a bounded retain window. `launch.Spec.Retain` is per-spawn for exactly this reason — *"a task
that narrates every token and one that reports four boundaries an hour want windows two orders of
magnitude apart"* — and a streaming foreign agent is the first thing in the tree that is
emphatically the former.

So the question is what an observer needs rather than what the agent said, and that is a
judgement to make once, here, rather than per charter.

## Why it matters beyond the drive

This is the price of the opaque leaf, and the drive's own summary of it was that the price is
payable and unpaid: narration and a readable workspace both exist as designs. It bears directly
on whether a charter author can work through a foreign agent rather than verse's own loop —
choosing `driver: claude` today buys one gate on the invocation and then silence.

Related: `km5mzagq` (the coderunner's other residuals). Finding 7 — nothing says *where* the work
is happening — is the same complaint from the other side; the drive recorded them as one.