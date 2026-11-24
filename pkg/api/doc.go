// Package api is the Operator API: viffd's HTTP surface, as the routes it serves and the
// requests and answers that are its own. The CLI, the iods and any program that drives a viff
// import these declarations, and viffd decodes and writes them, so a field renamed on one side
// is a build failure on the other rather than a key dropped in silence. A runner's API is
// package runner's. An iod's registration and the rows about it are package io's, and a
// resolver's package resolver's, since each record lives in the package that owns it.
//
// # Routes
//
// Each route's request and answer, and the file in this package that holds them; a type from
// another package is named with it. Each type's comment opens with the route it crosses on.
// GET /api serves the same table from the registrations (internal/viffd/server.go), and a test holds this
// one to it (internal/viffd/apidoc_test.go). A response of `204` is no body.
//
//	Route                                     Request               Response                     File
//
//	The viff
//	GET    /status                            -                     Status                       status.go
//	GET    /healthz                           -                     Health                       status.go
//	GET    /api                               -                     Routes                       routes.go
//	GET    /schema                            -                     the schema document
//	GET    /schema/{name}                     -                     one definition of it
//	GET    /whoami                            -                     WhoAmI                       accounts.go
//	POST   /scope/delete?scope=               -                     204
//
//	The store
//	GET    /state?at=&paths=                  -                     entity.Doc
//	GET    /watch?at=&since=                  -                     SSE stream of entity.Event
//	GET    /entity?at=                        -                     entity.Entity
//	POST   /entity                            PutEntity             entity.Entity                entity.go
//	DELETE /entity?at=                        -                     204
//	GET    /perms?at=                         -                     PermsRecord                  perms.go
//	GET    /perms/records                     -                     []PermsRecord                perms.go
//	PUT    /perms                             PermsEdit             PermsRecord                  perms.go
//	GET    /groups                            -                     []GroupRow                   perms.go
//	POST   /groups                            Group                 GroupRow                     perms.go
//	GET    /groups/{name}                     -                     GroupRow                     perms.go
//	DELETE /groups/{name}                     -                     204
//
//	Charters and schedules
//	GET    /charter?at=                       -                     the charter tree
//	GET    /charter/{name}?at=                -                     trigger.Trigger
//	POST   /charter?at=&prune=                -                     Installed                    charter.go
//	POST   /charter/check?at=                 -                     CharterCheck                 charter.go
//	DELETE /charter/{name}?at=                -                     204
//	DELETE /charter?at=                       -                     204
//	GET    /schedules                         -                     []ScheduleRow                schedules.go
//	POST   /schedules/{id}/tick               ScheduleTick          Ticked                       schedules.go
//
//	Flows
//	GET    /flows                             -                     []FlowRow                    flows.go
//	GET    /flow/tree                         -                     []trigger.FlowTree
//	POST   /flow/suspend                      FlowSelect            FlowRow                      flows.go
//	POST   /flow/resume                       FlowSelect            FlowRow                      flows.go
//	POST   /flow/say                          FlowSelect            Steered                      flows.go
//	POST   /flow/cancel                       FlowSelect            Steered                      flows.go
//
//	Tasks
//	GET    /tasks?runner=                     -                     []Task                       tasks.go
//	POST   /tasks                             Spawn                 Spawned                      tasks.go
//	GET    /tasks/{id}                        -                     Task                         tasks.go
//	GET    /tasks/{id}/events?since=          -                     SSE stream of task.Event
//	POST   /tasks/{id}/say                    Say                   Asked                        tasks.go
//	POST   /tasks/{id}/cancel                 Cancel                204                          tasks.go
//	POST   /tasks/{id}/pause                  -                     204
//	POST   /tasks/{id}/resume                 -                     204
//	POST   /tasks/{id}/resolve                Resolve               Recorded                     asks.go
//	PUT    /tasks/{id}/autonomy               SetAutonomy           204                          tasks.go
//
//	Asks
//	GET    /asks?waiting=me|all=|served=|ask=  -                    []AskRow                     asks_record.go
//	POST   /asks                              Ask                   Asked                        asks.go
//	GET    /asks/{id}                         -                     entity.Entity
//	PATCH  /asks/{id}                         AskChange             Asked                        asks.go
//	DELETE /asks/{id}                         -                     204
//	POST   /asks/{id}/answer                  Resolve               Recorded                     asks.go
//	POST   /asks/{id}/say                     GateSay               204                          asks.go
//	GET    /asks/{id}/served                  -                     AskServed                    asks.go
//
//	Runners
//	GET    /runners?probe=                    -                     []RunnerRow                  runners.go
//	POST   /runners?probe=                    Runner                RunnerRow                    runners.go
//	GET    /runners/{name}                    -                     Runner                       runners.go
//	GET    /runners/{name}/check              -                     RunnerCheck                  runners.go
//	POST   /runners/hello                     -                     RunnerCheck                  runners.go
//	DELETE /runners/{name}?cancel=|leave=     -                     Removed                      runners.go
//
//	Iods
//	GET    /iods?probe=                       -                     []io.Row
//	POST   /iods?probe=                       io.Entity             io.Row
//	GET    /iods/{name}                       -                     io.Entity
//	GET    /iods/{name}/check                 -                     io.Checked
//	DELETE /iods/{name}?forget=|keep=         -                     Removed                      runners.go
//	POST   /iods/{name}/hint?at=              -                     204
//	POST   /iods/{name}/pass                  io.OutPass            204
//	GET    /outboxes?to=                      -                     []Outbox                     iods.go
//
//	Resolvers
//	GET    /resolvers                         -                     []resolver.Row
//	POST   /resolvers                         resolver.Resolver     resolver.Row
//	GET    /resolvers/{name}                  -                     resolver.Resolver
//	DELETE /resolvers/{name}                  -                     204
//	POST   /resolvers/{name}/grant            AnswerFor             204                          resolvers.go
//	POST   /resolvers/{name}/revoke           AnswerFor             204                          resolvers.go
//	GET    /resolvers/{name}/asks             -                     []ServedAsk                  asks.go
//
//	Accounts
//	GET    /accounts                          -                     []Account                    accounts.go
//	POST   /accounts/{provider}               AccountClaim          AccountClaimed               accounts.go
//	POST   /accounts/github/verify            AccountVerify         Account                      accounts.go
//	POST   /accounts/{provider}/heard         AccountHeard          Account                      accounts.go
//	POST   /accounts/{provider}/link          AccountLink           Account                      accounts.go
//	GET    /accounts/{provider}/who?sub=      -                     Account                      accounts.go
//	DELETE /accounts/{provider}/{sub}         -                     204
//	GET    /.well-known/openid-configuration  -                     JSON, as OIDC has it
//	GET    /.well-known/jwks.json             -                     JSON, as OIDC has it
//
// An answer to a question (POST /asks/{id}/answer, /tasks/{id}/resolve) answers 204 when it
// settles the question, and 202 and Recorded while the question waits for more answers.
//
// Most routes take `?scope=`, which names the copy-on-write scope a request reads or writes;
// absent is baseline. The query parameters shown are the ones a route's meaning turns on.
// `?probe` is `true` or `false`, and anything else is refused: an install probes unless told
// false, and a listing only when told true, since a reachability column is as of now.
//
// # Names
//
// What a route answers is named for what it is. A record a route installs is named for the thing
// (Runner, io.Entity, resolver.Resolver), and a GET by name answers exactly that record, so it
// can be edited and installed again: the install refuses a field it doesn't know. A listing row
// embeds the record and adds what viff knows of it now (RunnerRow, io.Row, resolver.Row,
// GroupRow), and an install answers the row its listing shows, with what a runner described and
// any warning the install has for the operator. A claim of an account is not an install, so it
// answers what to do next (AccountClaimed). A registration this viff can't decode is listed as
// a row saying why (`unreadable`), its GET by name answers 500, and its removal takes it out by
// its name. GET /asks answers each ask as the keys api.AskRecord names, and only those: a check in
// viffd's tests holds every ask they leave to the type, and viffd logs a key it drops. Every ask
// says its parties the same way: who raised it (`from`), whom it is put to (`to`), and who settled
// it (`by`). A request
// that is an act is named for the act (Resolve, Say, SetAutonomy, FlowSelect), and says who did it
// where the act is recorded.
//
// # Format
//
// Bodies are tony. A request may be JSON, which is valid tony, but an answer is never JSON: a
// tagged document has no faithful JSON rendering (see package codec). `?format=`, Content-Type
// and Accept pick between tony and YAML. The two OIDC documents are JSON because OIDC says so.
// A listing is a bare array of its rows, never an object wrapping one.
//
// # Refusals
//
// A refusal is a status and a sentence in plain text, the sentence written for the person who
// sent the request. The statuses carry meaning a program may act on: 400 a body or a query that
// does not read; 403 a principal who may not; 404 nothing there; 409 a precondition that did not
// hold, or a removal of something that still holds work, until a flag says what to do with it;
// 410 a task that has ended; 422 a task that can't be steered; 502 a runner, iod or store that
// did not answer; 503 not ready yet, with Retry-After where waiting is the answer.
//
// # Headers
//
// Viff-Principal names who a request is from, where the viff takes names as asserted or beside
// a verified bearer token (PrincipalHeader). Viff-Version is the build each side was made from
// (VersionHeader). Viff-Resolver makes a request as a resolver its sender installed
// (ResolverHeader). Viff-For-Principal is the account an io saw, for a person it relays
// (ForPrincipalHeader). Viff-Task is the task a runner carries a request for, beside the
// runner's own token (TaskHeader).
//
// # Not yet one shape
//
// Some acts still answer differently from their neighbours, and are listed here until they are
// made to agree:
//   - GET /state answers entity.Paths with `?paths`, GET /charter/{name} answers a
//     trigger.Stratum for a level's name, and GET /asks answers the docket's DocketRows with
//     `?waiting=me`, `?all=true` or `?served=true`.
//   - POST /tasks answers 200 where the Runner API answers 201.
//   - GET /runners/{name}/check writes what the runner described; POST /runners/hello is the
//     same check asked by the runner itself, with its workload token.
package api
