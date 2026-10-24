# charters: a suggested stale-review alert for example/app, DMed by the verse app

A starting point for Heidi (ENG-1296): a charter that tells a person, once each weekday morning, which pull requests on example/app have waited on their review for more than a day. Lightly tested; a suggestion to hand over, not a product.

## Plan

Beside the comment-drift charters in deploy/charters/example/repo/app, with a README of what to change (the person, the hour, the threshold, the prompt).

1. A schedule of the charter's own ticks on weekday mornings.
2. A rule on the tick gathers, from the github iod, the open pull requests whose `requestedReviewers` holds the person's login, and writes the list on an entity of the charter's own (gather, then spawn, as comment-drift does: a spawn binds the payload that fired it).
3. A rule on that entity spawns a claude-runner task with the list. It may `github.read-pr` and nothing that writes. It reads each one's reviews, decides which have waited more than a day, and answers with a digest under an answer contract.
4. The completion hook writes the digest as a message under `slack.Signadot.out` to `user:<email>`: the Slack iod's out half looks the email up, opens the DM and posts as the verse app, as gate-ask.tony does. Named by the tick's date, one DM a day; level-triggered, so a re-fire edits rather than repeats.

Combines the iod's requested reviewers (the set, for free) with the task verb (reviews, only for that set). No Slack verb is needed: the out half already DMs a person by email.

Check: the tony match for "this array holds this login"; whether the installer's grant reaches `slack.Signadot.out`.

Testing: install on staging, tick once by hand (`verse schedule tick`), watch one run DM a test recipient. That posts to Slack, so it is asked before it is done.

Depends on the iod's requested reviewers and on `github.read-pr`.