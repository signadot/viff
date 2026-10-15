// Package workspace is a task's git working directory: every repository a spawn names, cloned
// off a mirror the runner keeps, side by side under one root, each on the task's own branch. It
// is also where a task's work waits on its way out (stage.go), and the rules a delivery is
// checked against: which branches each repository permits a push to (Repo.Permit), and the
// provenance trailers every commit must carry.
//
// A checkout holds no credential and has no remote but the mirror. The runner pushes from the
// mirror with its own credential (Mirrors.Deliver), and nothing here reads a credential helper,
// an ssh agent or a `.git/config` remote.
package workspace
