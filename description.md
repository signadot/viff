# verse/remove-connect

Branch `verse/remove-connect`, worktree `.worktrees/remove-connect`, cut from `claude/verse-docs-iteration-a55dc9` at a39baa35 (PR #5) so the docs removal lands on the site organisation that is going out, not on main's older index pages. One commit, 26e41eb0, unpushed.

The idea, its status at removal, and the next-step designs are written up in Notion under "Verse Umbrella" (page "verse connect: the idea, its status at removal, and what was next", 3f308fc654be8182b633f3104b5ec659), because the git issues about it were scrubbed from origin on 2026-10-07 and the Notion page is the one live copy of `4eg94w0j`.

## What went

`verse connect` and `verse disconnect`: the command, its content controller, the per-file validator rule, and the federation of a working tree. With them, everything that existed for them:

- `cmd/verse/connect.go`; `io/federation.go`, `io/meta.go`, `io/content.go`, the stat helpers and their tests.
- The harness's content reader: `task.ContentReader`, the `content` argument on `Runner.Spawn`, the taskd `Builder` and `harness.Config`; `read_file` reads the task's root.
- The store's mount support: `entity.MountRoot`, `IsMountOwned`, `MountKPath`, the mounted-places table, `rootFor`/`rootForAt`, the level walk over a mounted place (`walkPathsAt`, `memberAddress`) and the mount refusals in `PathsMatching`, `watchPattern` and `Since`; `entity/composedwatch_test.go` and `onelength_test.go`. Every path renders under the store's one root (`pathAt`).
- `verse up --docd-mount-addr` and `--docd-mount-advertise-addr`, `docd.mount` on `/status` (`api.Docd` is the client face alone), the admin face's `docd-mount` address. `verse up` still reserves an ephemeral port for docd's own mount listener when it spawns `o sys up`, because docd defaults that listener to a fixed port; verse never dials it.
- Deploy: versed's mount args, the `docd-mount` Service and its tailnet device; docd's StatefulSet keeps `-docd-mount-addr` because it is docd's flag.
- `smoke.sh`: the connect step is a scopes step: a scoped write lands in the scope and not in baseline, and a scope's `flow suspend` of baseline's package is refused naming baseline.
- Docs: `reference/connect.md` is `reference/scopes.md` (the scopes half kept), `platform/connect.md` is `platform/scopes.md`, `design/reference-connect.md` is `design/reference-scopes.md`; `content-federation.md`, `controllers.md` and seams §3 deleted; glossary loses `federation` and `controller`; `change-events.md` keeps the composed-watch hazard as a property of docd that verse avoids by mounting nothing; `io.md` keeps "a Source is not a docd controller".

## What stays

Scopes: tasks, charters, perms and the charter-trial proposal (jnvbwvja) all use them, and the open scope issues (yjn5qagj, f80cejye) stand. With `disconnect` gone, `POST /scope/delete` is the one way to drop a scope and no CLI verb does; that belongs to yjn5qagj.

## Trailers

Declared on 26e41eb0: `Tier: contract, docs`; `Breaks:` names the two commands, the two flags, `docd.mount` and how a scope is now made and dropped.

## Check

`make check` green; `go test ./...` green except `TestEveryDeployedCharterInstalls` and `TestIssueNextProposesOnTheTick`, which fail on the untouched branch head too (they read `deploy/charters/verse/repo/verse/{comment-drift,issue-next.tony}`, absent on the branch and on main); `./smoke.sh` 64 checks.
