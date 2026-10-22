# repo-charters: verse/live holds only the charters, so a candidate PR's diff is exactly what would install

**What happens:** the reconciler's candidate pull request onto `verse/live` is a branch at main's commit, so its diff is main against whatever `verse/live` was last moved to. `verse/live` moves only when the charters change, so every commit that leaves them alone puts it further behind main. PR #36 (2026-10-22) asked to install tree 1fff798c, and it showed **453 files across 18 commits**: everything since #24 (40934c34). Two of those files decide anything, `.verse/charters/comment-drift/{drift,merge}.tony`. A reviewer can't see what they're asked to approve. This happens on every candidate, not just this one: the diff is the size of main's history since the charters last changed.

**Proposal:** `verse/live` is its own history holding only `.verse/charters`, which is what runs.
- A candidate is one commit on `verse/live` whose tree is main's charters tree. Its message names main's commit, for example `charters: install tree <tree> from <main sha>`. The candidate branch is at that commit, so the PR diff is exactly the change that would install.
- The git iod still reflects `tree.charters` on every ref and installs from `verse/live`'s commit, as it does now.
- The install record, the tag (`live-verse/<tree>`) and the commit status name `verse/live`'s commit. Each one keeps main's commit beside it, since that's the one a person can check out.
- A rollback to a tree answered before is a new commit on `verse/live` with that tree. It doesn't reset the branch.
- Merging stays by GitHub's own reckoning: the candidate's head is a child of `verse/live`, so moving `verse/live` to it merges the PR.

**Rejected alternative:** fast-forwarding `verse/live` along main whenever main's charters tree matches what runs. That keeps today's model, but a PR still carries every unrelated commit made since the charters last changed, and nearly every commit to main costs a branch move.

**To decide (Scott and Daniel):**
- whether the first install creates `verse/live` as an orphan branch or rewrites the existing one (it exists in signadot/verse at 40934c34 and is protected by a ruleset);
- what the git iod's out half needs to make a commit with a given tree on a branch, where today it only moves a branch to a given sha.

Touches: system/repocharters/charter.tony.tmpl (the candidate, live-made and live-moved rules), the git iod's out half (io/gitout.go), and the README and reference-example prose that says verse/live is "the candidate's commit".