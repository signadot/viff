# verse-runner replaces the coderunner, piece by piece, with main green at every step

The coderunner (docs/reference/examples/coderunner) is runnerd plus five things: a workspace per
task, a parent that holds the token and does push and fetch for the child, an egress proxy, a
config of environments, and a `command` driver for programs. Its parent proxy (hop.go) predates
runnerd's SubprocessHandler and is a second implementation of a process per task. Replacing the
coderunner with the verse runner is converging the two, one piece at a time, with the coderunner
importing each piece as it moves, until nothing is left of it but a config file. Every step lands
on main green with the coderunner still deployed; the coderunner is the oracle throughout.

Steps, each its own PR:

1. The git tools: push as the agent's delivery, author identity, fetch (from verse/codex, without
   the codex driver).
2. Workspace as a package: workspace.go, stage.go, first clone and object linking move to
   runner/workspace. A pure move.
3. The capability channel into runnerd: push and fetch become a parent-side seam on
   SubprocessHandler over the control socket; destinations, retirement, the lease and PR creation
   move to runner/delivery. The hop's lifecycle (a spawn answered before the workspace is ready,
   a child's exit remembered as a terminal status) comes across; the hop tests are the spec.
4. Egress: runnerd's bwrap sandbox gains the network choice (none, restricted, open); the proxy
   moves across to serve `restricted`.
5. Environments and drivers: the coderunner's config becomes the verse runner's; verserunner's
   drivers gain `command` and run llm and calls rooted at the workspace. `runnerd verse -config
   coderunner.tony` does what the coderunner does; its main.go goes.
6. The deploy swap: deploy/verse-runner from deploy/coderunner; both run on staging for one ship;
   the install moves; the old tier, the example, the Makefile targets and the walkthrough go. The
   deployed charters running unchanged on the new runner is the acceptance test.
7. The codex driver, after.

Kept as is: `environment:` on the spawn (charters unchanged through the swap; repos, credentials
and destinations stay in the runner's config), versed's spawn path, the gate credential,
`retain`. One binary: the verse runner is the coding runner.

Agreed with Daniel 2026-10-14. Relates to f4gyjeprha9xr8z2shn0.