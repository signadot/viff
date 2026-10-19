// Package systems is the systems a task acts on, each described once, as data: `git`, `github`,
// `git-issue`, `github-issue`, `linear-issue`, `slack`, `question`, and the three a runner
// intercepts inside a run, `files`, `commands` and `agent`. A SYSTEM IS A NAME WITH
// VERBS, and each verb has the params a call carries. Everything that names what a task does
// names it by this pair: a tool is `<system>-<verb>` (`git-push`, what a doer calls), a step is
// `<system>.<verb>` (`git.push`, what a gate matches), and a step's fields are the verb's params.
//
// The documents (documents/*.tony) are the one place a system is said. Verse installs them in
// its store at start, under `verse.systems.<name>`, so what a charter may name is readable where
// the charter is; a runner's code claims the verbs it implements by asking for their names
// (Tool, Step), and Check at its start says which documented verb nothing claimed, so a
// document and the code that honours it cannot drift apart silently. The package depends on
// nothing of verse's: package task reads it for the steps, and a runner for the tools.
package systems

//go:generate go tool tony-codegen -dir .
