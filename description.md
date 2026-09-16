# watch: a path reaching into an array delivers nothing, while a read at the same path answers

entity.KPathAdmits (entity/doc.go:443-453) answers false for an index or a keyed element -- "an index or a keyed element admits none, since no address is spelled that way". It backs Reaches, Commit.Touches, Subtree.covers and versed.selector.matchRef (versed/server.go:1567), so a watch whose ?at reaches into an array matches no change and delivers nothing, silently and forever.

The read plane answers the same path. GET /state?at=demo.pr."1".votes[0] reads the element: the handler splits at LiteralPrefix, reads the holding node, and entity.Reach walks the rest through entity.step (versed/server.go:1821-1834, entity/doc.go:205-250). cmd/verse/arrayaccess_test.go:27 pins it end to end, and a write lands at the same address (arrayaccess_test.go:49).

So the two faces disagree about what an address is, and they are pinned not to: TestStateAndWatchNameTheSameSet (versed/payloadfilter_test.go:496), with the reasoning at versed/server.go:1546 -- they must agree "in what they answer, not in what they pick".

## What is deliberate, and what is not

walkChange (entity/change.go:45-51) stops at the node holding the array on purpose: "a change inside one is a change to the node it is inside, and an address cannot name further." That is a coherent position about what a CHANGE is -- the array is the unit that changed.

What is not coherent is the answer a watcher gets for saying so. A watch at an element is accepted and then silent, where the same path read answers. Silence is the one answer that cannot be told from "nothing has happened yet".

## The shapes available

1. Refuse it. A watch whose path reaches into an array is invalid_path, or is answered at the node holding the array with that said. The watcher learns immediately, which is what the read plane already does for a path that cannot mean anything.
2. Honour it at the holding node: a watch at votes[0] fires when the array changes, carrying the array. The address names where the watcher looked, the event names what changed. This keeps walkChange as it is.
3. Make an element a change unit: walkChange descends arrays and an address can name an element. That is the largest of the three, and it asks whether votes[0] is disjoint from votes[1] -- the question entity/write.go:26-30 keeps out of the address grammar on purpose.

Whichever is chosen, the invariant is the test to write: a path the read answers is a path the watch either serves or refuses, and never one it accepts and ignores.

## Why now

docs/working/paths-listing-plan.md (branch paths-cli) makes --paths list addresses inside arrays, because a listing that hides what a read reaches is the silent wrongness --paths exists to prevent. That plan therefore prints addresses which can be read but not watched, and says so. This issue is the other half.