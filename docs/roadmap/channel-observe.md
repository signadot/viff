# Named persistent channels

Status: idea, not designed. It comes out of the flow tree —
[`verse flow show`](../tutorial.md#4-steer-flows), and the chain addressing behind it in
[completion hooks](../completion-hooks.md#addressing-the-chain-is-a-kpath). Once processes hang
off an address, the obvious next relation is what they are saying to each other, and through
what.

## The idea

Agents need to talk — to other agents, to humans, over spans longer than any one process —
and they need somewhere to do it that outlives the talking.

The working example is a git issue. It has a name, it persists, several parties append to
it over time (some agent, some human), and anyone can pick the thread up later without having
been present for it. This repo already works that way: an upstream defect goes into one living
tracking issue that later sessions append to rather than filing anew, and a verification result
lands as a comment on the issue that prompted it. The issue is the channel; the agents are
correspondents on it.

That is the primitive: named, persistent, content-bearing. Verse has neither half of it,
though it has near-misses on both sides.

## What verse has that is close, and why each is not it

- `say` — `EvSay` is "an utterance in the process's conversation" and even carries `By`, so
  the *conversational* shape is there. But a say is addressed to a live process and lives on
  the read plane: when the process ends, the thread is gone. Conversation without persistence.
- `motion:channel:<id>` — verse already spends the word "channel," and a motion really is a
  place several parties post to and that anyone can discover as an entity. But it is a
  governed decision: ordered options, thresholds, terminal-once, closed the moment an
  outcome commits, and its id is *hashed from a seed* rather than named
  (`motionChannelID`, `trigger/motion.go`). Multi-party and persistent, but no free content and
  no name a person chose.
- entities — named and durable, but last-write-wins *state*, not a thread.

The gap is the middle of those three: persistent like an entity, conversational like a say,
and named — which nothing currently is.

## The same move already has a precedent here

`expose` is `connect` with exactly this change applied to a different noun: keep the federation
mechanics, give the thing a name, make it outlive the session, drop the process-tied nonce
([expose](expose.md)). A named channel is that move applied to `say`. If `expose` lands first, its
named-durable-handle machinery is likely the same machinery — worth building them in an order
that finds out.

## Where the content lives

*Index, not warehouse* is the doctrine this idea presses hardest on, and there are two readings:

- Federated channel — the thread lives in its own system (a git issue, a PR, a Slack
  thread); verse gives it an address, indexes it, and serves content on demand, exactly as
  `connect` does for a working tree. The motivating example *is* this one: the git issue is
  already the durable store, and what verse would add is an address in the verse and the ability
  to match on it.
- Native channel — verse owns the thread. Simpler, and necessary for agent-to-agent chatter
  with no external home, but it makes the verse a warehouse for conversation, which is the thing
  it has been most careful not to become.

The federated reading is truer to the model, and it is also what the example actually was. The
native one is probably unavoidable eventually; the question is whether it arrives as a
*channel that happens to be local* rather than as a second mechanism.

## A thread may already be expressible as history

Before inventing an append operation, note that an entity's successive payloads are a
sequence: `Store.Since(rev)` replays them from a cursor, and every existing observer — watch,
triggers, cursors, gap markers — already speaks it. So "post" could be a `Set` (via `Modify`, so
concurrent posters do not clobber) and "read the thread" a `Since` over that ref, with no new
operation in the store.

The catch is retention, and it is store-dependent: `MemStore` keeps a bounded history and
answers `Since` with `ok=false` once the revisions asked for have been dropped
(`entity/store.go`). A durable channel cannot be built on a window that can silently close — so
this direction is worth taking only as far as the logd-backed store's retention really goes.
Anything a reader must be able to find *later* is exactly the case where a bounded buffer fails.

## The relation back to the flow tree

This is where the idea started. Once channels are named entities, `flow show` can hang them off
the process leaves: which channels this process is attached to, and therefore which other
flows it is talking to. A point-in-time dump is then a communication graph — processes,
channels, and the edges between them — assembled the same way the tree is: publish the address,
and the relation is a query rather than an index.

And subscription needs no new mechanism at all. If a channel is an entity, "wake me when someone
posts here" is a trigger whose `when` matches it. An agent's inbox is a charter.

## Not this: the control graph

A related but different idea, worth keeping distinct: recording which entities each process
*read and wrote*, to get provenance and contention. That is a control relation — it says who
touched what, not what was said — and it needs different things (writes are visible on the
change stream but anonymous, since `entity.Event` carries no actor; reads leave no trace at all).
Useful, but it answers "who acted on this" rather than "who is talking to whom", and conflating
the two would make a graph that shows neither.

## Open questions

- Addressing. What `system:kind:id` is a channel? The name is the id, so the question is
  really which system it lives under, and whether a federated channel and a native one share it.
- Who may post. A channel is a capability. `process.Grant` is *tool*-shaped (`GateMatch`
  predicates over what a process may do), so "may post to this channel" is a new axis rather
  than a reuse of the authority one.
- Addressee. Does a post name who it is for, and is that just a field — or does turn-taking
  need real structure once more than two correspondents share a thread?
- Retention and closure. A git issue closes. Does a channel? Terminal-once is what makes a
  motion a decision rather than a conversation, so a channel probably wants the opposite default
  — but "open forever" is also how a channel becomes a warehouse.
