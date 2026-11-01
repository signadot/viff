# charters: a rule installed over data that stands is refused its first firings, and does not fire again

A charter installed where none stood, over data its rules already select, has those rules' first firings refused. Each one reads as its place, and the place has no owner on record yet:

    rule=live-made kind=unread why="reading what its condition reads: !get-path repo-charters.verse.state.main:
    refused: via:verse.trigger.rule.\"repo-charters.verse:live-made\" holds nothing at repo-charters.verse.state.main
    and reading it needs read; it has no owner on record, and a place acts as whoever owns it — install it again as somebody"

Once the install is done, the rule's record has its owner (`verse perms --at 'verse.trigger.rule."repo-charters.verse:live-made"'` says `owner: system:charter`), and reads succeed. But a level rule's firing over the backlog is the only one it gets until what it reads changes. So a rule whose subject and reads are already in their final state never acts, and nothing says so after the install. Each refusal is a WARN, and every one of them is in the install's own window.

## Seen on staging, 2026-11-01

The migration to the record (xv2pp5j0h12ksd50wxn0) removed the six repo-charters reconcilers by hand (`verse c repo-charters <repo> -r`) and left their data. versed then installed the new reconcilers as the system at start:

- 121 refusals, all between 22:20:08 and 22:21:12, across the six installs and none after, from:
  - main-passed, main-not-denied, live-moved, live-made, candidate-superseded and candidate-issue, 12 each;
  - install 11, main-denied 10, main-refused 8, installed 8, propose 6, live-check 6.
- live-made never fired. Its candidate was answered yes and its `tree` and `sha` matched main's, but no repository got `refs/verse/live`.
- A versed restart, which reinstalls the same charters over rules that now stand with their owners, fired the backlog cleanly. live-made made the records within a minute, and the installs followed.

The refusal lines are attached (`versed-refusals-2026-11-01.log`).

## Who meets it

- Not an ordinary restart: the rule records stand already, with their owners.
- A first install of a charter over data that stands before it: a reconciler added for a repository whose data is already there, a charter removed and installed again, or a migration like this one.
- The same race applies wherever a fresh install's backlog is evaluated before the rule's owner record is written. It is not specific to the system's installs, but the system's are the ones seen.

## What would fix it

The backlog should be evaluated as the owner the install will record, so no firing runs with no owner. Either write each rule's owner record before its backlog fires, or evaluate the backlog after the install has written them. Retrying refused firings is not the fix: the refusal is a fact of an order that should not happen.