# The spawn says which repositories a task works in, and what it may push

Toward the spawn spec (Notion "The spawn spec", 3f408fc654be814c823cdb22431f227e), the workspace, git and GitHub axis first. Today a spawn names an `environment:` and the runner's config holds the repositories, their bases and the destinations work may go to; a task pushes by naming a destination. Under the spec the spawn carries its repositories, with access, a starting branch and the branches the run may push to, and a task pushes a branch that the parent checks against that allowance. Authorization moves from the runner's config to the charter, which is where `$subject.head` lives.

The general part lives in `runner/workspace` and `runner/delivery`, for any runner; the verse runner (and the codex runner next) are consumers of it. The minted task credential (`credential: {audience}`) is untouched.

Commits, each green, each its own PR:

1. The spawn carries `repos: [{repo, access, from, push: {new, existing, force}}]` on launch.Spec, api.Spawn, task.Launch and the wire. Bind expands `repo`, `from` and `existing`. Install refuses `push` without `access: write`, an access other than read or write, and a pattern that does not parse. versed carries it; nothing reads it yet.
2. runner/workspace builds a Repo from a spec repo (directory = the name part, `owner/name` = github.com, `from` omitted = the mirror's HEAD) and an allowance replaces destinations, `auto` and the retire glob; runner/delivery's credential request names repo, branch and delete, the parent checks the allowance before staging and releasing, the push tool takes those three and may be called any number of times, and the closing delivery (Finisher) goes. The parent-side and task-side glue move out of verserunner into runner/delivery. For this one commit an environment's repos are read as a spawn's would be, so the deployed config and the charters stay green.
3. The verse runner builds the workspace from the spawn's repos. Environments lose `repos` and `credentials`; a write under a restricted egress that cannot reach the forge is refused at admission; the Verse-Environment trailer goes. Deployed config, example config, both comment-drift charters, descriptor and docs move to the new shape.
4. Pull requests are tools: `create-pr` and `update-pr` from pull.go, gated as a create on a `pull` resource, with the task's branch as head.

Branch `verse/verse-runner` in `.worktrees/verse-runner`, on top of the steps of sjw7pv0wh12ksxvysxn0.