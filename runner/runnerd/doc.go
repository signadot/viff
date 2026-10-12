// Package runnerd hosts a runner: the tasks it runs, and what it offers. A runner is reached in
// one of two ways, and both use the same Daemon:
//
//   - over HTTP, as a program of its own (cmd/runnerd): Handler serves the task contract —
//     spawn tasks, stream their events (SSE), resolve their gates by id, steer them, rebind
//     their vector, pause and cancel their subtrees — and GET /describe. Load reads the
//     configuration every runner shares.
//   - embedded, by a verse that runs work in its own process (`verse up`): Spawn, Lookup and the
//     question hooks (OnQuestion, OnCheck, OnDrop) are called directly.
//
// How a task is built is injected as a Builder, so the daemon carries no coupling to any
// particular agent. The runners verse ships are its subpackages (verserunner).
//
// Control ops are task- or subtree-scoped (never per-child); the read plane is drop-with-gap.
//
// A runner is a program somebody deploys rather than a library verse is built with, for the
// reason a source is (cmd/iod): verse reaches it over a URL, and a runner registered from
// elsewhere is no different from one shipped here.
package runnerd
