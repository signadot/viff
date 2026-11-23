// Package runner is the Runner API: the contract between verse and a runner, as the routes a
// runner serves and the requests and answers that are its own. A task is verse's, not a
// runner's: what a runner reports about one (task.Snapshot, task.Event) is the task's shape, in
// package task, which imports neither this package nor anything else of verse's. Both ends
// depend on it: verse's client of a runner (pkg/runner/client) and the runtime a runner is built on
// (pkg/runner/runnerd). A runner written in another language reads the same shapes from the schema,
// where each is published under its name.
//
// # Routes
//
// Each route's request and answer, and the file in this package that holds them. Each type's
// comment opens with the route it crosses on.
//
//	Route                                   Request         Response                File
//	GET  /describe                          -               Description             describe.go
//	POST /tasks                             Launch          task.Snapshot           launch.go
//	GET  /tasks                             -               []task.Snapshot
//	GET  /tasks/{id}                        -               task.Snapshot
//	GET  /tasks/{id}/events?since=<cursor>  -               SSE stream of task.Event
//	POST /tasks/{id}/say                    SayRequest      204                     control.go
//	POST /tasks/{id}/cancel                 CancelRequest   204                     control.go
//	POST /tasks/{id}/resolve                ResolveRequest  204                     control.go
//	POST /tasks/{id}/pause                  PauseRequest    204                     control.go
//	POST /tasks/{id}/resume                 ResumeRequest   204                     control.go
//	PUT  /tasks/{id}/autonomy               AutonomyRequest 204                     control.go
//
// # Names
//
// What the API returns is named for what it is: Description, task.Snapshot, task.Event. The body
// of a command is named for the command, <Command>Request, and says who gave it (`by`). A task
// starts from a Launch: the spawn a charter wrote, as verse resolved it for one firing, with
// verse's references filled in, and an id. "Spawn" is verse's word,
// for the charter's declaration and the act of firing it; "launch" is the runner's, for the
// concrete request it receives. POST /tasks answers with the task's snapshot: 201 when it
// started the task, 200 when the task was already running.
//
// The launch's autonomy is the task's (task.Autonomy), since the task's steps are decided under it,
// and PUT /tasks/{id}/autonomy replaces it on a running task. A runner checks a launch as it
// arrives and refuses what it can't honour with `400` and the reason (runnerd.CheckLaunch), and
// a new autonomy the same way (runnerd.CheckAutonomy).
//
// # The lease
//
// A task runs only while somebody reads it. Its lease is the launch's (Launch.Lease, LeaseOf),
// and counts from the launch and again from each GET /tasks/{id}/events the runner answers. A
// runner answers that route at once, and ends a task whose lease runs out as failed and
// orphaned, on its terminal frame and its snapshot (docs/runners.md).
//
// # Answers
//
// Every answer a runner's own handler gives on these routes carries AnswerHeader, its refusals
// included; a door in front of it, such as its auth, need not. What verse acts on as a fact
// about a task, a 404 for it and an accepted subscribe, it takes only from an answer that
// carries it: anything in front of a runner can answer HTTP.
//
// Bodies are tony by default; a request's Content-Type and Accept pick the format. A refusal
// is a status, and the client reads each as one of the errors in pkg/task/errors.go.
package runner

//go:generate go tool tony-codegen -dir .
