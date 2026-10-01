# proposal: repo charters 4: the charter trial — candidate charters run in a scope

Part of j5vbwvja. **A rough design; nothing is built.** This is a nice to have: parts 1 to 3 are built and running without it. It would improve what reviewers see on the pull request and what the ask reads.

Proposed: **a trial run.** Verse runs the proposed charters in a sandbox (a verse scope) against real events, and reports what they *would* have done next to what today's charters did. Trials would be managed by tags: `verse/trial-<nonce>` at `<commit>` runs the charters at that commit in a scope, and deleting the tag drops the scope. A review would start one for its proposal. A person could push such a tag to try something without proposing it.

## What is known about verse

- A rule in a scope fires on baseline writes, so a trial would react to live events, with no scoped sources or replay.
- A scope runs only the rules installed into it.
- Iods do not read scopes, so a sink (the Slack iod) does not see a scope's writes.
- Outboxes exist (84a83gkw). Whether a package installed in a scope may declare `sends:`, and whether an out half would read such an outbox, is unchecked.
- The scope gaps are open: yjn5qagj (scopes are not recorded, listed or reclaimed) and f80cejye (a scope name nobody made leaks a session and an attribution).

## What we'd like to discuss

- **Is the trial run worth building now,** or should the gaps blocking it close first?
- **Who may start a trial?** A trial runs proposed charters, so starting one is a real permission (2dchqs8j).

## Open questions

1. **What runs.** The candidate tree alone, or what runs with the candidate's diff applied. The latter is preferred, so that the comparison is like for like.
2. **Effects.** A trial's sink writes would be inert. Tasks spawned in the scope act directly and are not. How trial tasks would be contained is open.
3. **Reaching out on purpose.** Posting the report into the pull request needs either an iod that reads a named scope, or the reconciler carrying the scope's output across. The latter is preferred, since it keeps iods scope-unaware.
4. **The report.** The scope's writes would be diffed against what the live charters did over the same events.
5. **Lifecycle.** Trial tags would be reconciled to scopes on start, with a lease or TTL for trials nobody tears down.
6. **Time.** The charters are pinned at a commit, but the world is live. A scope pinned to a point in time would be a logd/go-tony ask.
