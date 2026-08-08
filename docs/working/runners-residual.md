# Runners: what is left

Status: residual of a landed feature — two open corners of something that runs.
[Runners](../runners.md) is built and this is not a plan to change it. What is here is the part
that holds by convention rather than by construction, and the question the vocabulary has not had
to answer yet. Both still stand: there is no `GET /runner` descriptor, and `local` is still only
the meaning of absent.

## The corner that is convention rather than construction

**The contract does not say HOW control is applied**, and a url chosen per spawn is what makes
that reachable.

An answer accepted for a brokered task is *queued* for a worker that must dial in and pull it,
and the question record says so — `delivered: false` until the acknowledgement clears it. verse
learns which case it is by asking the handle, in Go (`RelaysControl() bool`); `broker.handle`
returns true, and **`runner.HTTP`'s handle does not implement it at all**, so anything reached
over a URL is treated as applying control synchronously.

That holds by *configuration*, not by construction: the runner faces anyone binds do apply
control synchronously, and nothing detects one that queues. The exposure is that the url is
chosen per spawn by anyone who can write an entity, rather than once by whoever deploys verse.

The answer this relies on is that **the runner refuses**. A real runner knows what it is and
what it can host; verse does not, and cannot be made to without either a contract change or a
probe. So a runner handed a launch it cannot host with the semantics verse expects returns an
error, and the onus sits with whoever deployed it — which is the drive, which is the party that
knows. A refused spawn comes back as a failed handle carrying the runner's own words, onto the
flow — so the refusal is a mechanism rather than a convention.

A ban would be worse than the risk. "A runner entity may not point at a queuing runner" means a
drive's broker is reachable only by making it the whole verse's runner — not "you cannot deploy
a broker", but "you cannot have one for some of the work", which is exactly the all-or-nothing
per-spawn selection is for.

Where it should end up is a descriptor on the contract — `GET /runner`, saying what this is and
how control is applied — which would answer both this and the install probe in one call, and
would let a broker stop refusing. The residual under either: several workers stand behind one
broker name, so "which worker took it" is a question the runner name cannot answer, and a drive
debugging a stuck task will ask it.

## Whether `local` should be nameable

`local` is the meaning of *absent*, which keeps every runner entity a reachable address. The cost
is that a charter cannot say "explicitly in-process" — it can only decline to say anything. If a
deployment ever has a default that is not local, that distinction starts to matter, and
`runners:local:local` (or a reserved kind, as `resolvers:instance` is) becomes worth having.
