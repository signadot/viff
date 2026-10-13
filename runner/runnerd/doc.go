// Package runnerd hosts a runner: the tasks it runs, and what it offers. A runner's tasks are
// handled by one of two task handlers, which differ in where a task runs:
//
//   - InProcessHandler: in the handler's own process.
//   - SubprocessHandler: in a process of its own, a child of the handler's, in a Sandbox (bwrap);
//     the handler passes verse's requests on to it. The task's process holds its one task with
//     an InProcessHandler (ServeTask).
//
// A runner is embedded in another program, or standalone, a program of its own. `verse up`
// embeds the runners verse ships with an InProcessHandler, with no sandbox, and calls Spawn,
// Lookup and the question hooks (OnQuestion, OnCheck, OnDrop) directly. `runnerd` (cmd/runnerd)
// is a standalone runner with a SubprocessHandler.
//
// Both handlers serve the task contract over HTTP (HTTPHandler) — spawn tasks, stream their
// events (SSE), resolve their gates by id, steer them, rebind their vector, pause and cancel
// their subtrees — and GET /describe. Load reads the configuration every runner shares.
//
// How a task is built is injected as a Builder, so the handler carries no coupling to any
// particular agent. The runners verse ships are its subpackages (verserunner).
//
// Control ops are task- or subtree-scoped (never per-child); the read plane is drop-with-gap.
//
// A runner is a program somebody deploys rather than a library verse is built with, for the
// reason a source is (cmd/iod): verse reaches it over a URL, and a runner registered from
// elsewhere is no different from one shipped here.
package runnerd
