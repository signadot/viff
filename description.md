# charters: a rule the system installs stands with no owner until a second write, and fires as nobody

Data should never exist that does not have an owner. A rule the system installs does, for a moment.

The install writes the rule's record in one write and its owner in a second. `trigger/charter.go`:

- `store.Write` writes the rule's record. A write by the system records no owner, since the system owns the root and a node reads as its parent's.
- `takeForTheSystem` then gives the record an owner record of its own, the system's (`entity.ClaimForSystem`).

A person's install has no such gap: their write records them. Between the system's two writes the rule stands, and the engine arms it. It acts as its place (`via:verse.trigger.rule."<pkg>:<rule>"`), and a place resolves to the owner on its own record (`entity/attribution.go`, `resolve`, which reads `recordAt(ref, false)`). With none there it acts as nobody, and every read it makes is refused:

    rule=live-made kind=unread why="reading what its condition reads: !get-path repo-charters.verse.state.main:
    refused: via:verse.trigger.rule.\"repo-charters.verse:live-made\" holds nothing at repo-charters.verse.state.main
    and reading it needs read; it has no owner on record, and a place acts as whoever owns it — install it again as somebody"

Once the second write lands, the rule's record is the system's (`owner: system:charter`) and its reads succeed. A level rule fires over the backlog at install, and its firing in that window is the only one it gets until what it reads changes. So a rule whose subject and reads already stand in their final state never acts.

## Seen on staging, 2026-11-01

The migration to the record (xv2pp5j0h12ksd50wxn0) removed the six repo-charters reconcilers by hand (`verse c repo-charters <repo> -r`) and left their data. versed then installed the new reconcilers as the system at start, every rule's record new:

- 121 refusals, all between 22:20:08 and 22:21:12, across the six installs and none after, from:
  - main-passed, main-not-denied, live-moved, live-made, candidate-superseded and candidate-issue, 12 each;
  - install 11, main-denied 10, main-refused 8, installed 8, propose 6, live-check 6.
- live-made never fired. Its candidate was answered yes and its `tree` and `sha` matched main's, but no repository got `refs/verse/live`.
- A versed restart reinstalls over records that already stand with their owners, so there was no window, and the backlog fired cleanly. live-made made the records within a minute, and the installs followed.

The refusal lines are attached (`versed-refusals-2026-11-01.log`).

An ordinary restart does not meet it, since every record already stands with its owner. A system install of a rule whose record is new does, wherever the data its rules select already stands. That covers a reconciler added for a repository whose data is there, a charter removed and installed again, and a migration like this one.

## The fix

The system's write of a rule's record carries its owner, as a person's does: one write, so no rule record ever stands without one. The same holds for every other node the system writes and then takes in a second step (`takeForTheSystem`, `entity.ClaimForSystem`, and `versed/charter.go`'s `takePackageAddresses`): the owner goes in the write that makes the node. Retrying refused firings, or ordering the backlog after the second write, would leave ownerless data standing in between, and is not the fix.
