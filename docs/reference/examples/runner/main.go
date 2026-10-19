// Command runner is a verse runner you can copy and change: the smallest thing that serves
// the verse-facing task-control contract, so a drive can deploy somewhere to run work that
// the verse deployment does not otherwise need to know about.
//
//	go run ./docs/reference/examples/runner -addr :9800
//	verse runner add gpu http://localhost:9800
//	verse task run -runner verse.runners.gpu -agent sample "retrain the ranker"
//
// It is a real program and it compiles with the tree, which is the point: a stub in a fenced
// block goes stale the first time a signature moves and nobody finds out. See
// ../../../runners.md for what a runner IS, and ../../../task-control.md for the contract.
//
// # What to change
//
// Two functions, and they are the two questions a runner answers. The Driver decides what the
// work IS — one action at a time, until it says it is done — and the Executor carries each
// action out. Everything else here is plumbing you can leave alone: runnerd serves the
// contract, tracks the running set, and streams the read plane.
//
// # What NOT to change
//
// Control is applied SYNCHRONOUSLY here — a cancel that returns has been applied — because
// that is what verse assumes of anything it reaches over a URL. A runner that instead QUEUES
// control for something that must dial in and collect it has no way to say so across this hop,
// so an operator would be told a task was cancelled while it kept running. If you build one
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

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

func main() {
	addr := flag.String("addr", ":9800", "address to serve the task-control contract on")
	flag.Parse()

	// runnerd is the contract: GET /describe for what is on offer, POST /tasks to launch,
	// GET /tasks[/{id}] to list and snapshot, GET /tasks/{id}/events for the read plane, and
	// the control POSTs. verse drives exactly this over HTTP, and its own in-process runner is
	// the same handler — so there is no privileged local path for this to differ from.
	d := runnerd.NewInProcessHandler(build)
	// What it offers, which verse reads when the runner is installed and checks each spawn
	// against: one agent, `sample`. Its tasks run in this process, unconfined, so the only
	// network it builds them is the host's (`open`); a launch asking for `none` or a list of
	// hosts is refused rather than run with more than it asked for. CHANGE THIS with build.
	d.Describe(runner.Description{Offers: []runner.Offer{{
		Agent: "sample",
		With:  map[string]runner.Accepts{"network": runner.AcceptsOneOf(runner.NetworkOpen)},
	}}})
	log.Printf("runner listening on %s — install it with: verse runner add <name> http://<host>%s",
		*addr, *addr)
	if err := http.ListenAndServe(*addr, d.HTTPHandler()); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

// build turns one launch into one task. CHANGE THIS.
//
// The Launch carries what the spawn said: the prompt, the doer (an agent by name, or a
// script) and what it works with, the policy its steps are decided under, and the
// provenance. A runner that cannot honour something it was handed should say so — see the
// note on refusing, below.
func build(l runner.Launch, reg *task.Registry) *task.Task {
	// The supervisor decides what happens before each action: the launch's policy says which
	// steps are refused, asked about or allowed (a verb no row names is refused), and `reg` is the registry
	// runnerd serves answers into, so a question becomes one an operator answers through
	// verse (`verse question`) while the task waits.
	//
	// The sample's `do` action is no step (task.StepOf), so nothing asks before it and the
	// sample runs to completion unattended. Propose `run_bash` with a `command` instead and
	// each action is a `commands.run` step: a spawn saying nothing refuses each, one saying
	// `policy: [{match: {system: commands}, then: allow}, {then: ask}]` runs them, and an ask
	// row on the step asks whom it names.
	sup := task.NewSupervisor(l.Policy, reg)
	return task.NewTask(l.ID, sup, &planDriver{prompt: l.Prompt}, &echoExecutor{})
}

// planDriver is a placeholder for whatever decides the work: an LLM, a script, a queue client.
// This one splits the prompt on semicolons and asks for each part in turn, which is enough to
// see a task run end to end without pretending to be intelligent.
//
// The contract is the whole of it: Next returns the next action and `true`, or `false` when
// the work is done. Returning an error fails the task, with the error as the reason.
type planDriver struct {
	prompt string
	steps  []string
	i      int
}

func (d *planDriver) Next(ctx context.Context, prev task.StepResult) (task.ToolUse, bool, error) {
	if d.steps == nil {
		for _, s := range strings.Split(d.prompt, ";") {
			if s = strings.TrimSpace(s); s != "" {
				d.steps = append(d.steps, s)
			}
		}
		if len(d.steps) == 0 {
			d.steps = []string{d.prompt}
		}
	}
	// Cancellation is noticed at the loop's boundary, between steps, not inside Next. A driver
	// that blocks here without selecting on ctx is one the task can never be cancelled out
	// of, so it would produce no terminal at all and whatever is waiting on it waits forever.
	if err := ctx.Err(); err != nil {
		return task.ToolUse{}, false, err
	}
	if d.i >= len(d.steps) {
		return task.ToolUse{}, false, nil // done: the loop reports `done`
	}
	step := d.steps[d.i]
	d.i++
	return task.ToolUse{Name: "do", Input: map[string]any{"step": step}}, true, nil
}

// echoExecutor is a placeholder for whatever carries an action out: a shell, an API call, a
// tool the drive owns. CHANGE THIS.
//
// The StepResult it returns is what the driver sees as `prev` on the next step, so this is the
// loop's feedback path — an executor that discards what it learned leaves the driver deciding
// blind.
type echoExecutor struct{}

func (echoExecutor) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	step, _ := tu.Input["step"].(string)
	fmt.Fprintf(os.Stdout, "runner: %s\n", step)
	return task.StepResult{Use: tu, Output: "did: " + step}, nil, nil
}
