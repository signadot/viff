# plan: the operator API reads like the Runner API (api/doc.go, named bodies, one shape per act)

## Goal

versed's HTTP API should read the way the Runner API does (`runner/doc.go`):
- one package comment holding the route table: route, request, response, file;
- one named type per request and answer, each type's comment opening with the route it crosses on;
- the client and the server importing the same declarations.

Today, of versed's 81 routes, 24 declare an answer type and 18 a request type. About 30 answers are map literals or unexported structs. Seven routes declare a body that differs from what the handler sends. Similar acts answer differently: two DELETEs answer a report where seven answer 204, each create answers its own partial record, and a list row and the single-record GET spell the same record differently.

**Out of scope:** publishing the surface as a document (`paths:` in the schema, query parameters and status codes, `verse api`). That stays with mp8n08pmh12krw2nhxn0. Also out of scope: a typed error body, emitting JSON, and renaming the record keys.

## Steps

The inventory behind this was read at a05be952. Step 1 starts by re-reading it at main.

**1. Say what is already true.** One commit, no wire change.
- Correct the seven wrong declarations:
  - `POST /tasks/{id}/say` answers `api.Asked`;
  - the resolvers' grant and revoke take `api.AnswerFor`;
  - `GET /asks/{id}/served` answers `api.AskServed`, and `GET /resolvers/{name}/asks` answers `api.ServedAsks`;
  - `GET /charter/{name}` answers a stratum for a level;
  - `GET /state?paths` answers `verse-paths`;
  - `POST /charter` and `/charter/check` take charter files;
  - cancel takes a request.
- Fix the stale comments: `server.go`'s registration block and its mount, the `api` package comment's `/gate/resolve` story, and `runner/control.go:52`.
- Remove the dead CLI code (`postGroup`, `lastPrintEmpty`, `elsewhereInPackages`).

**2. Name the bodies, and write `api/doc.go`.** No wire change: each new type has the keys its handler writes today.
- The stored records move into `api` (decision 3), and each listing row embeds its record.
- `api` gains the types the handlers already imply: `Health`, `GroupRow`, `Installed`, `Ticked`, the runner, iod and resolver rows, the account types, `WhoAmI`, `DocketRow` and `Recorded`.
- `api/api.go` is split by area, as `runner/` is.
- `api/doc.go` holds the route table, then short sections on names, the format, refusals and headers. It ends with the inconsistencies step 3 hasn't fixed yet.
- A test holds the table equal to the routes versed registers. The same kind of test holds `runner/doc.go` against both runner runtimes.
- The handlers, the CLI and the iods send and read the new types.

Steps 1 and 2 are one PR.

**3. One shape for one act.** Breaks the wire, so one commit per change, each with `Breaks:`, in a second PR:
- a DELETE answers per decision 2;
- a create answers the row its list shows;
- `GET /{name}` answers that same row;
- lists are bare arrays (`ServedAsks` unwrapped);
- the threshold 202 answers `Recorded{count, need, choice}`;
- cancel takes `api.Cancel{reason}`, sets `by` from the caller, and cancels through the handle for local and remote tasks alike;
- `?probe` takes an explicit true or false on lists and adds, with the default written down;
- the Runner API's two runtimes answer alike: 410 for autonomy on an ended task, and control routes by method.

Key casing is recorded as a rule: camelCase, with `child-grants` and `dial-in` the two exceptions.

**4. The client that proves it.** A `client.Verse` with one method per route, taking and returning api types, written against the table. The CLI and the iods move onto it as they're touched.

## Decisions

1. **Where work runs**, and so what a task record says about its runner (`Task.runner` holds a URL or `local`, `runnerName` a ref, and `status.runner.resume` a URL). Daniel will settle this with Scott; a spawn naming no runner runs inside versed today, on staging too. Step 3's runner fields wait for it, and nothing else does.
2. **A DELETE answers a report only when it cascades** (decided 2026-10-22). `DELETE /runners/{name}` and `DELETE /iods/{name}` answer one `api.Removed`; every other DELETE answers 204. The 409 without a flag stays.
3. **The stored records move into `api`** (decided 2026-10-22): `api.Runner` (was versed's `runnerEntity`), `api.Iod` (was `io.Entity`) and `api.Resolver` (was `resolver.Resolver`).
   - Each row embeds its record. tony-codegen flattens an embedded struct from the same package, so the wire stays flat. One from another package nests under its own key, which is why `api.Task` copies `task.Snapshot`'s fields.
   - Checks and defaults stay in `versed`, `io` and `resolver` as functions over the `api` type.
   - `io` already imports `api`, so `api` can't import `io`.
   - The resolver's record holds a `trigger.Match`, so `api` would import `trigger`. Step 2's first commit confirms that, and the flattening, on one type before moving the rest.