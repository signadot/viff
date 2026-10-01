# proposal: repo charters 2: the review environment — a charter contract, adapted per review tool by a package over its source io

Part of j5vbwvja. **Built for GitHub** (on main as of 3f2f5fd5).

**The review happens in the tool the project already uses.** Verse does not do code review. It puts its question and its hold where the change is reviewed, and hears the answer there.

## As built (GitHub)

There is no separate review package. The reconciler's own rules derive inputs for the github iod's out half, through the package's outboxes.

- **The pull request to main that changes the charters folder:**
  - The question is a comment, kept in step in place. People answer with `/verse proceed` or `/verse deny`, counted only from a linked account.
  - Verse's review requests changes until the question is answered yes and the charter check passes, then is dismissed. Verse never approves.
  - Two commit statuses go on the head: `verse`, the answer, and `verse/check`, the check.
  - A pull request pushed on to other charters is asked about those, in the same comment. One that leaves the charters alone has its hold dismissed.
- **The pull request onto `verse/live`,** for a change that reached main unasked: a branch at main's commit, opened by the github iod, so the diff is what is on main against what runs.
- **The github iod's `-tree` reports what an open pull request changes,** against its head's merge base: `changes.<name>`. A pull request branched before the charters last changed proposes nothing.
- **The App** is verse's identity on GitHub. GitHub honours its request for changes as a merge block where reviews are required, even beside another approval. It can dismiss its own.
- **Not built:** Gerrit, and any review tool other than GitHub. A GitHub App iod (check runs, rulesets) stays deferred (8k4azhtj).

## Open

- The charter check's refusal on the pull request is hard to read: g32ed7ha.
- Verse's main requires no reviews, so the hold is advisory there.
