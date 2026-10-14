// Package runner is the Runner API: the contract between verse and a runner, as the routes a
// runner serves and the requests and answers that are its own. A task is verse's, not a
// runner's: what a runner reports about one (task.Snapshot, task.Event) is the task's shape, in
// package task, which imports neither this package nor anything else of verse's. Both ends
// depend on it: verse's client of a runner (runner/client) and the runtime a runner is built on
// (runner/runnerd). A runner written in another language reads the same shapes from the schema,
// where each is published under its name.
//
// # Routes
//
// Each route's request and answer, and the file in this package that holds them. Each type's
// comment opens with the route it crosses on.
//
//	Route                                   Request              Response                  File
//	GET  /describe                          -                    Description               describe.go
//	POST /tasks                             task.SpawnRequest    SpawnResponse             spawn.go
//	GET  /tasks                             -                    []task.Snapshot
//	GET  /tasks/{id}                        -                    task.Snapshot
//	GET  /tasks/{id}/events?since=<cursor>  -                    SSE stream of task.Event
//	POST /tasks/{id}/say                    SayRequest           204                       control.go
//	POST /tasks/{id}/cancel                 CancelRequest        204                       control.go
//	POST /tasks/{id}/resolve                ResolveRequest       204                       control.go
//	POST /tasks/{id}/pause                  -                    204                       control.go
//	POST /tasks/{id}/resume                 -                    204                       control.go
//	PUT  /tasks/{id}/vector                 task.VectorRequest   204
//
// The launch (task.SpawnRequest) and the vector (task.VectorRequest) are still the task
// engine's; the launch is replaced here, and the vector removed, in the next step of the
// runners plan (2a).
//
// Bodies are tony by default; a request's Content-Type and Accept pick the format. A refusal
// is a status, and the client reads each as one of the errors in task/errors.go.
package runner

//go:generate go tool tony-codegen -dir .
