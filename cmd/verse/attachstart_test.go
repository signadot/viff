package main

import (
	"strings"
	"testing"
)

// `task run -attach` PRINTS THE RUN FROM ITS START.
//
// A live attach joins a stream at its next frame, and the task it is attaching to was running
// before the request reached it — so a run that answered in one turn printed from cursor 2, its
// first frame gone. This command spawned the task, so the whole run is what was asked for: the
// stream is read from cursor 0, which replays what is retained before going live.
func TestTaskRunAttachPrintsFromTheStart(t *testing.T) {
	serve(t)
	out, err := runCLI(t, "task", "run", "-f", spawnFile(t, `{script: "echo hi", environment: {systems: {network: {mode: open}}}}`), "--attach", "--as", "user:t")
	if err != nil {
		t.Fatalf("task run -attach: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "cursor: 1") {
		t.Fatalf("the run's first frame was not printed:\n%s", out)
	}
}
