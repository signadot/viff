# charters: drift.tony's install line and comment-drift-states.md follow the charters to .verse/charters, as a pull request that exercises repo-charters

Two things were left out of hehx7ddch12ks2sgs1n0 on purpose, because each is a change to the tree the git io reflects (`-tree .verse/charters`), and so a change the repo-charters reconciler asks about:

- `.verse/charters/comment-drift/drift.tony:8` still says `verse charter -a -d deploy/charters/verse/repo/verse/comment-drift`.
- `deploy/charters/verse/repo/verse/comment-drift-states.md`, the drive read as state machines, is the only file left under that directory; drift.tony says it sits beside the `comment-drift/` directory, which is `.verse/charters/`.

Both move in one commit, as a pull request to main, which is the first change to `.verse/charters` since the reconciler went up: the github io reports the tree the pull request proposes, the question is asked on it, verse requests changes until it is answered yes and the check says the tree installs, and on merge main carries an answered tree that installs with no second question (docs/reference/examples/repo-charters/repo-charters.tony). The install changes no rule: the comment line and the markdown are the only differences, and a `.md` in the directory is not a charter file (io/gitinstall.go, charterDocs).

What to watch for: the `verse` and `verse/check` statuses on the head; the ask on the docket; after the merge, `verse/live` moving to the merge commit and `repo-charters.verse.running` naming the new tree.