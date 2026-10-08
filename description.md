# verse/remove-connect

Branch `verse/remove-connect`, worktree `.worktrees/remove-connect`, cut from `claude/verse-docs-iteration-a55dc9` at a39baa35 (PR #5) so the docs removal lands on the site organisation that is going out, not on main's older index pages.

The idea, its status at removal, and the next-step designs are written up in Notion under "Verse Umbrella" (page "verse connect: the idea, its status at removal, and what was next"), because the git issues about it were scrubbed from origin on 2026-10-07 and the Notion page is the one live copy of `4eg94w0j`.

## What goes

`verse connect` and `verse disconnect`: the command, its content controller, the per-file validator rule, and the federation of a working tree.

Code:

- `cmd/verse/connect.go` whole; the two entries in `main.go`; the `verse up` warning in `docd.go` about a missing mount face; the `client.go` comment that names it.
- `io/federation.go`, `io/meta.go`, `io/content.go`, `io/stat_unix.go`, `io/stat_other.go` and their tests (`federation_test.go`, `content_test.go`), if nothing else uses the stat helpers.
- `versed/server.go`: `storeContentReader`, and the scoped engine's spawner gets a nil content reader (a scoped task reads the local root, as a baseline task does); `versed/readfail_test.go` (it tests the reader); the `versed/io.go` comment.
- `task/harness/content_test.go`: the "federated" cases.
- `smoke.sh`: the connect step. The check that a scope's `flow suspend` of baseline's package is refused stays, with the scope made by a scoped write instead of by connect.
- `api/api.go` comment on `/status` `docd.mount`; comments in `deploy/build.tony`, `deploy/source/docd-service.yaml`, `deploy/README.md` (the tailnet paragraph).

Docs:

- `docs/reference/connect.md` becomes `docs/reference/scopes.md`: the scopes half stays (scopes, the charter a scope runs, what a scope owns), the federation half goes. Same for `docs/platform/connect.md` and `docs/design/reference-connect.md`.
- `docs/content-federation.md` deleted; `docs/seams.md` §3 removed.
- `docs/glossary.md`: `federation` removed, `scope` and `baseline` point at the scopes page.
- Every other mention: `README.md`, `docs/README.md`, `docs/platform.md`, `docs/reference/README.md` (command table), `mkdocs.yml`, `reference/up.md`, `io.md`, `change-events.md`, `reference/entities.md`, `reference/gates.md`, `reference/charter.md`, `tutorial.md`, `platform/architecture.md`, `deployment-and-steering.md`, `control.md`, `reference/perms.md`, `reference/store.md`, `reference/tasks.md`, `task-control.md`, `controllers.md`.

## What stays, and is a decision

Scopes stay: tasks, charters, perms and the charter-trial proposal (jnvbwvja) all use them, and the open scope issues (yjn5qagj, f80cejye) stand.

Three things have no in-tree user once connect is gone, and this branch keeps them unless told otherwise:

1. The store's mount support: `entity.MountRoot`, `IsMountOwned`, `MountKPath` and the table of mounted places, which names `local.dir-content-`. The composed-watch tests pin why `MountRoot` exists.
2. docd's mount face: `verse up --docd-mount-addr`, `--docd-mount-advertise-addr`, `docd.mount` on `/status`, and the deploy manifests that pass them.
3. `task.ContentReader`, the harness seam a federated `read_file` went through, and `docs/controllers.md`, the page on writing a controller.

Removing those three is removing the content-federation component whole (Bird's Eye View, component 2), not only `verse connect`. If that is wanted, it is a second commit on this branch.

## Trailers where it lands

`Tier: contract, docs`, `Breaks: verse connect and verse disconnect are gone; a scope is made by any scoped write and dropped with POST /scope/delete`.