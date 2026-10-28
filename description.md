# deploy: staging's `repos` is readable by nobody but whoever ran repopulate

`deploy/repopulate.sh` takes `repos` as whoever runs it and grants only `child-owner` beneath it to the git iod. The repo-charters reconciler's questions live under `repos`, and the Slack ask names one by its path, so a reader who is not the owner is refused:

```
$ verse d repos."verse.merge:verse".trigger.train-run.merge-to-main~1dbcb0a3
refused: user:victor@example.com holds nothing at repos.… and reading it needs read; the nearest owner on record is user:walter@example.com at repos
```

Fix: the record holds `read` for `*` on the node and beneath it, beside the iod's `child-owner`. Applied live on staging 2026-10-28 with two `--grant` calls; the script sets the same three lines so a repopulate keeps it.