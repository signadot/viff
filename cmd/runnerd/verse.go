package main

import "github.com/signadot/verse/runner/runnerd/verserunner"

// The verse runner: verse's own agent, with its model reached through Portkey, scripts, and the
// programs its configuration names. Each task runs in a process of its own, in the sandbox
// -sandbox names, and in a workspace of the environment it names when the configuration
// declares environments.
func verseMain(args []string) error { return verserunner.Main(args) }
