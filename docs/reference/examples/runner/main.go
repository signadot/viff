// Command runner is a verse runner you can copy and change: the smallest thing that serves
// the verse-facing process-control contract, so a drive can deploy somewhere to run work that
// the verse deployment does not otherwise need to know about.
//
//	go run ./docs/reference/examples/runner -addr :9800
//	verse runner add pool:gpu http://localhost:9800
//	verse process run --runner runners:pool:gpu "retrain the ranker"
//
// It is a real program and it compiles with the tree, which is the point: a stub in a fenced
// block goes stale the first time a signature moves and nobody finds out. See
// ../../../runners.md for what a runner IS, and ../../../process-control.md for the contract.
//
// # What to change
//
// Two functions, and they are the two questions a runner answers. The Driver decides what the
// work IS — one action at a time, until it says it is done — and the Executor carries each
// action out. Everything else here is plumbing you can leave alone: processd serves the
// contract, tracks the running set, and streams the read plane.
//
// # What NOT to change
//
// Control is applied SYNCHRONOUSLY here — a cancel that returns has been applied — because
// that is what verse assumes of anything it reaches over a URL. A runner that instead QUEUES
// control for something that must dial in and collect it has no way to say so across this hop,
// so an operator would be told a process was cancelled while it kept running. If you build one
// of those, refuse the launch rather than accepting it: an error from Spawn travels onto the
// flow with its reason, and the drive that deployed the runner is the party that knows.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
)

func main() {
	addr := flag.String("addr", ":9800", "address to serve the process-control contract on")
	flag.Parse()

	// processd is the contract: POST /processes to spawn, GET /processes[/{id}] to list and
	// snapshot, GET /processes/{id}/events for the read plane, and the control POSTs. verse
	// drives exactly this over HTTP, and its own in-process runner is the same handler — so
	// there is no privileged local path for this to differ from.
	d := processd.New(build)
	log.Printf("runner listening on %s — install it with: verse runner add <kind>:<name> http://<host>%s",
		*addr, *addr)
	if err := http.ListenAndServe(*addr, d.Handler()); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

// build turns one launch into one process. CHANGE THIS.
//
// The Launch carries what the spawn said: the task, the driver name a charter asked for, the
// frame it should be read in, the tool set it may use, and the envelope (vector, grant) verse
// already resolved. A runner that cannot honour something it was handed should say so — see
// the note on refusing, below.
func build(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
	// The supervisor decides what happens at a gate, and the vector is the DEFAULT for gates
	// this launch did not narrow — verse overrides it from the spawn when the charter said
	// `vector:`, so this is the floor rather than the rule.
	//
	// Auto so the sample runs to completion unattended. Change the default to process.Ask and
	// every action raises a gate instead: `reg` is the registry processd serves answers into,
	// so those become questions an operator answers through verse (`verse gates`), and the
	// process waits. That is the one-word difference between a runner that acts and a runner
	// that asks.
	sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, reg)
	return process.NewProcess(l.ID, sup, &planDriver{task: l.Task}, &echoExecutor{})
}

// planDriver is a placeholder for whatever decides the work: an LLM, a script, a queue client.
// This one splits the task on semicolons and asks for each part in turn, which is enough to
// see a process run end to end without pretending to be intelligent.
//
// The contract is the whole of it: Next returns the next action and `true`, or `false` when
// the work is done. Returning an error fails the process, with the error as the reason.
type planDriver struct {
	task  string
	steps []string
	i     int
}

func (d *planDriver) Next(ctx context.Context, prev process.StepResult) (process.ToolUse, bool, error) {
	if d.steps == nil {
		for _, s := range strings.Split(d.task, ";") {
			if s = strings.TrimSpace(s); s != "" {
				d.steps = append(d.steps, s)
			}
		}
		if len(d.steps) == 0 {
			d.steps = []string{d.task}
		}
	}
	// Cancellation is noticed at the loop's boundary, between steps, not inside Next. A driver
	// that blocks here without selecting on ctx is one the process can never be cancelled out
	// of, so it would produce no terminal at all and whatever is waiting on it waits forever.
	if err := ctx.Err(); err != nil {
		return process.ToolUse{}, false, err
	}
	if d.i >= len(d.steps) {
		return process.ToolUse{}, false, nil // done: the loop reports `done`
	}
	step := d.steps[d.i]
	d.i++
	return process.ToolUse{Name: "do", Input: map[string]any{"step": step}}, true, nil
}

// echoExecutor is a placeholder for whatever carries an action out: a shell, an API call, a
// tool the drive owns. CHANGE THIS.
//
// The StepResult it returns is what the driver sees as `prev` on the next step, so this is the
// loop's feedback path — an executor that discards what it learned leaves the driver deciding
// blind.
type echoExecutor struct{}

func (echoExecutor) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	step, _ := tu.Input["step"].(string)
	fmt.Fprintf(os.Stdout, "runner: %s\n", step)
	return process.StepResult{Use: tu, Output: "did: " + step}, nil, nil
}
