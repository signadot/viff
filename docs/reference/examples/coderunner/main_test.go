package main

import (
	"slices"
	"testing"

	"github.com/signadot/verse/runner/runnerd/verserunner"
)

// The runner starts each task process as its own executable with verserunner.TaskArgs, and here
// that executable is this program: it has to answer to those arguments as runnerd does, or a
// task process would read the runner's own flags, look for a configuration file inside the
// sandbox, and exit before serving anything.
func TestTheTaskProcessIsAnsweredAsRunnerdWould(t *testing.T) {
	args, ok := taskProcess(append([]string{"coderunner"}, verserunner.TaskArgs...))
	if !ok || !slices.Equal(args, verserunner.TaskArgs[1:]) {
		t.Fatalf("taskProcess(coderunner %v) = %v, %v; want the runner's arguments through, as a task process",
			verserunner.TaskArgs, args, ok)
	}
	if _, ok := taskProcess([]string{"coderunner", "-config", "x.tony", "-addr", ":9802"}); ok {
		t.Fatal("the runner's own command line was taken for a task process")
	}
}
