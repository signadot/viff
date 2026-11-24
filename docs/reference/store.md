# The store

Viff coordinates everything through one [store](../glossary.md#store). The store is a tree of
[data](../glossary.md#data). It can be watched, and a reader can follow it with a cursor. You do
not have to touch the store to *use* viff, because `up` brings it up for you. But the deeper
properties of viff come from the store, and this page is the one place that states them.

By default the store is [docd](https://signadot.github.io/tony-format/docd/), the tony-format
document store, over logd. docd gives viff four things:

| Property | What it means |
|---|---|
| **Copy-on-write scopes** | A [scope](../glossary.md#scope) is an overlay. Its writes do not touch [baseline](../glossary.md#baseline), and one operation drops it ([scopes.md](scopes.md#scopes)). |
| **Conditional and coordinated writes** | A write is an atomic compare-and-swap (CAS). Two writers that advance one record cannot lose each other's fields ([data](entities.md#conditional-writes)). |
| **Edge-faithful watches** | The change stream *is* the patch that each writer applied. No reader rebuilds, reorders or drops an edit. |
| **Compactions** | A compaction keeps the log bounded and keeps the current state. |

With no store flags, `up` runs `o sys up` to bring up logd and docd on a new temporary data
directory. It points the store at the client face of docd. The store flags change this:

| Flag | What it does |
|---|---|
| `-data <dir>` | uses a durable directory |
| `-docd-addr` | attach to a docd that is already running |
| `-docd-advertise-addr` | publish a different address on `/status`. Use it when a store client beside viff reaches docd at another address than viff does. |
| `-logd-config <file>` | the logd configuration, in tony, for the daemons that `up` starts, in place of the one viff instance writes. Viff refuses it beside `-docd-addr`, which starts nothing. |

See [up.md](up.md#the-store).

## The mapping

The store keeps its tree in logd under a prefix of its own. The prefix belongs to the store,
and nobody addresses it as a level. The node at `demo.pr.1` is the node at that
[kinded path](../glossary.md#kinded-path), whatever it holds. The store reads and writes with
Match and Patch. One logd watch drives both the change stream and the
[revision](../glossary.md#revision) cursor. That tree is all that `viff data` publishes. The
document that `viff data` answers with *is* this tree, at the same
[address](../glossary.md#address) ([data](entities.md#reading)).

**Absence is a missing key, not a null.** A removal is `!delete`. logd executes it by taking
the key out, so a deleted address reads back not_found. A null is a node like any other. The
same holds for an address that nobody has written, at every length, whether or not an ancestor
of it resolves. A read answers null if and only if somebody wrote a null there. A
*materialized* subtree has no removals in it. `walkEntityDoc` reads a materialized subtree, and
a read and a snapshot see one. The *delta* of a [commit](../glossary.md#commit) spells a removal
with the `!delete` tag.

A `FromCommit` replay watch serves `Since` directly. So a long hold costs a cursor and not a
buffer. Commit numbers increase, but they are **sparse**. The engine therefore treats a
revision as an opaque cursor and reconciles with `Since`. A write is atomic through CAS
(`PatchIf`), so two concurrent modifies do not lose updates. `Op` is `set` or `delete`. A create
is the same as an update, because logd delivers the patch that the writer applied, unchanged.

## The reactive grain is the node, not the transaction

The rule in this section is subtle, and much of viff depends on it.

One docd commit can patch many nodes atomically. The store splits each commit into **one
change event for each node that changed**. `changesAt` does the split: it compares the document
before the commit with the document after it, at the depth that the watching question asks
about. All the events of one commit share the revision of that commit.

Because of this split, the [subject](../glossary.md#subject) of a [rule](../glossary.md#rule)
is always one node. Viff dispatches one edge for each node. So `subject`, and the flow keyed
on it, is never ambiguous, even under a broad match. A broad pattern gives one
[firing](../glossary.md#firing) for each matching change.

The split loses less than it seems to, because the whole commit lands before any of its events
dispatch. The store first advances its watched document by the whole patch. Then it walks the
document for events. It reads the `to` of each event out of the document that has already
advanced. So when the edge for one node fires, every node that changed with it already has its
new value. The level state on the event shows the new value. The store shows it too, when a
gate or a set-valued subject reads the store at fire time. A rule can say "when A opens, and B
is green". It does not matter whether B went green in the same commit or an hour earlier.

What the split does lose is the commit as a *unit*. Two nodes that change together give two
firings, not one. A rule that matches both nodes fires twice, and you must remove duplicates
yourself. No rule can ask "did these change together?". The shared revision is the only trace,
and nothing groups events by it. This needs a change only if a reaction ever needs the group
itself, and not the joint state. See [change events](../change-events.md).

## What follows for a charter

Three properties of the store make the reactive loop converge. An author of a
[charter](../glossary.md#charter) must know all three:

- **A write of the same value is inert.** A write that gives a field the value it already holds
  makes no commit, so it starts no evaluation. This property stops a completion
  [hook](../glossary.md#hook) that states a subject's stage again from looping.
- **A watch is level-triggered when it opens.** So an installed rule acts on the backlog that
  stands in the store. After a restart, viff reaches the same verdicts again. They do not
  depend on what this engine saw. For this reason a rule needs a
  [latch](../glossary.md#latch) and not an edge; see [convergence](../convergence.md).
- **A write is a patch, so a write cannot carry a match operator as data.** `!irtype`, `!or`,
  `!glob`, `!let` and the rest of the match vocabulary are operations. When a payload carries
  one, the store executes it and does not store it. The write itself does not fail. Every later
  read fails, so the store becomes unreadable, and not only that data wrong. Viff refuses such
  a write, and names the operator and the field.

  Instead, mark the subtree as data. Use `entity.Raw` (`!raw`) on the way in. A charter rule
  already uses this: a rule has many operators, and the store keeps it unchanged under that
  escape. `entity.AsData` does the same for a record that you read, edit and put back.
  `entity.Modify` does this for its callers. For the longer version, read
  [the charter](charter.md#the-charter-is-durable). A charter is the first payload that
  needed this escape.
