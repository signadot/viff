# a landing batch lands branches the docket never listed

Approving one train landed two commits. The motion's subject was `acme.merge.train.f871375e…`, whose `branches` held exactly one ref; `git log` after the landing shows

    38a402ce ```json                                                    (the approved car)
    72f2fd2c comment-drift on acme.drift.head.head-moved@git:head:verse        (not in the batch)

The second is a leftover branch from the previous charter whose proposal this same charter had already marked `state: stale` — meaning it had been assessed as unable to land — and whose retirement spawn had failed earlier, so the ref was still in the clone.

WHY: `train-run`'s call list iterates the refs it finds — `for b in $(git -C verse for-each-ref … 'refs/remotes/mirror/verse/auto/*/*')` — rather than the branches the train listed. The charter says so deliberately ("the branches are not interpolated into the command, they are FOUND in the clone; the train's `branches` field is for the person reading the docket"), and the reasoning holds for the case it was written for: a static call list cannot carry a dynamic set.

What it costs is the gate itself. The docket renders `branches` and a person answers about THAT list; what the approval releases is a merge of whatever `verse/auto/*` refs exist at the moment the task runs — including ones the charter has retired, and including any that appear between the vote and the run. A gate whose question and whose effect are different sets is a gate that cannot be answered honestly, and it is the load-bearing part of this whole design.

Not a rebase problem: the rebase loop is what makes a batch of siblings land at all, as noted earlier. It is a question of WHICH refs the loop is given.

Fix directions, none free: pass the approved list to the task (a call list cannot interpolate a set, so this wants either a tool that takes a list argument or a driver that can); or have the task read the train entity itself and iterate that; or make `proposal-outrun`'s branch deletion reliable so a stale ref cannot be lying around — which narrows the window without closing it, since a proposal appearing after the vote still lands unasked.

Observed on the first batch the range-review charter landed.