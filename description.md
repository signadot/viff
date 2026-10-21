# a `$` in a bound value passes the runner's repo check and its ceilings

On `verse/environment` (#33), the checks a runner makes on a bound launch treat any value containing `$` as one still to be bound, and skip or loosen it:

- `runner.CheckRepos` skips the repo-name check (`runner/repo.go:69`);
- `systems.Block.check` skips an attribute's form check, and `checkAcross` skips the check-out-directory clash (`systems/environment.go`);
- `CeilingBlock.admits` reads the `$…` part as `**` and admits whatever intersects (`systems/ceiling.go:186`);
- `runner.Setting.Takes` accepts the value whole (`runner/describe.go:192`).

A firing binds payload text verbatim (`launch.expand`), so a payload containing `$` reaches the runner with a `$` in it. Probed with `launch.Spec.Bind` → `runnerd.CheckLaunch` → `workspace.FromLaunch`, under the ceiling `{repo: [github.com/acme/**], from: [main], fetch: {}}`:

- `from: $head` bound to `$x` is accepted, though the ceiling restricts `from` to `main` (`feature` is refused);
- `repo: $repo` bound to `$a/../..` is accepted by the name check and the ceiling, and the workspace checks it out as `..` from `https://github.com/../..`.

Install never needs the skips: `trigger.installLaunch` stands every `$` attribute in with a placeholder (`templatedEnvironment`) and drops `$` settings before any of these checks run, and a spawn written by hand or as an ask's `then` refuses `$` (`launch.Spec.CheckByHand`). `Environment.Reaches`, used by `runner.CheckReach` on the environment as written, keeps its own reading of `$` as unbound.

Fix: in these checks a `$` is a character, as it is everywhere a bound value is used.