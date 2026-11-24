// Command runner is a viff runner you can copy and change: the smallest thing that serves
// the viff-facing task-control contract, so a drive can deploy somewhere to run work that
// the viff deployment does not otherwise need to know about.
//
//	openssl rand -hex 32 > runner.token
//	go run ./docs/reference/examples/runner -addr :9800 -token-file runner.token
//	viff runner add gpu http://localhost:9800 -token-file runner.token
//	viff task run -runner mirror.runners.gpu -agent sample "retrain the ranker"
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
// contract, tracks the running set, and streams the read plane, and its guard proves the runner
// to its viff.
//
// # What NOT to change
//
// Control is applied SYNCHRONOUSLY here — a cancel that returns has been applied — because
// that is what viff assumes of anything it reaches over a URL. A runner that instead QUEUES
// control for something that must dial in and collect it has no way to say so across this hop,
// so an operator would be told a task was cancelled while it kept running. If you build one
// of those, refuse the launch rather than accepting it: an error from Spawn travels onto the
// flow with its reason, and the drive that deployed the runner is the party that knows.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

func main() {
	addr := flag.String("addr", ":9800", "address to serve the task-control contract on")
	// A runner is never taken at its word: it proves itself to the one viff instance it serves with a
	// token, here a static one, which its registration names by hash (`viff runner add
	// -token-file`). The viff instance's url is also the name its tokens are issued under, so spell it
	// as `viff up` prints its issuer: localhost, not 127.0.0.1.
	viffURL := flag.String("viff-url", envOr("VIFF_URL", "http://localhost:9797"),
		"the viff instance this runner serves, as its issuer is spelled (VIFF_URL)")
	tokenFile := flag.String("token-file", os.Getenv("VIFF_TOKEN_FILE"),
		"the file holding this runner's static token, `openssl rand -hex 32`; required (VIFF_TOKEN_FILE)")
	flag.Parse()
	if *tokenFile == "" {
		log.Fatal(errors.New("runner: no -token-file: a runner proves itself to its viff instance with a token " +
			"(`openssl rand -hex 32 > runner.token`, and `viff runner add -token-file runner.token`)"))
	}
	guard, err := runnerd.NewGuard(runnerd.Viff{URL: *viffURL}, *tokenFile)
	if err != nil {
		log.Fatalf("runner: %v", err)
	}

	// runnerd is the contract: GET /describe for what is on offer, POST /tasks to launch,
	// GET /tasks[/{id}] to list and snapshot, GET /tasks/{id}/events for the read plane, and
	// the control POSTs. viff drives exactly this over HTTP, and its own in-process runner is
	// the same handler — so there is no privileged local path for this to differ from.
	d := runnerd.NewInProcessHandler(build)
	// What it offers, which viff reads when the runner is installed and checks each spawn
	// against: one agent, `sample`. Its tasks run in this process, unconfined, so the only
	// network it builds them is the host's (`open`), which it says under the network's
	// `builds`; a launch asking for `none` or a list of hosts is refused rather than run with
	// more than it asked for. It takes no setting and holds no other system. CHANGE THIS with
	// build.
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{{
		Agent: &runner.AgentOffer{Name: "sample"},
		Systems: map[string]runner.System{"network": {
			Builds: systems.Ceiling{{Attributes: map[string]*ir.Node{"mode": systems.OneOf(runner.NetworkOpen)}}},
		}},
	}}})
	log.Printf("runner listening on %s — install it with: viff runner add <name> http://<host>%s -token-file %s",
		*addr, *addr, *tokenFile)
	// The hello: the viff instance describes this runner as soon as the runner tells it it is here, and
	// until a registration names it, the runner keeps telling it.
	go guard.Announce(context.Background(), log.Printf)
	if err := http.ListenAndServe(*addr, guard.Wrap(d.HTTPHandler())); err != nil {
		log.Fatalf("runner: %v", err)
	}
}

// build turns one launch into one task. CHANGE THIS.
//
// The Launch carries what the spawn said: the prompt or the script, the agent and its settings,
// the environment the run may reach, the autonomy its steps are decided under, and the
// provenance. A runner that cannot honour something it was handed should say so — see the
// note on refusing, below.
func build(l runner.Launch, reg *task.Registry) *task.Task {
	// The supervisor decides what happens before each action: a step outside the launch's
	// environment is refused, and within it the autonomy says which are refused, asked about or
	// allowed. `reg` is the registry runnerd serves answers into, so a question becomes one an operator answers through
	// viff (`viff question`) while the task waits.
	//
	// The sample's `do` action is no step (task.StepOf), so nothing asks before it and the
	// sample runs to completion unattended. Propose `run_bash` with a `command` instead and
	// each action is a `commands.run` step: a spawn whose environment gives no `commands`
	// refuses each; one giving `commands: {}` with `autonomy: [{match: {system: commands}, then:
	// allow}, {then: ask}]` runs them, and an ask row on the step asks whom it names.
	//
	// The ceilings are this runner's own line over each system, read after the autonomy. A
	// system with none is closed, so the sample says it leaves `commands` open.
	sup := task.NewSupervisor(l.Environment, l.Autonomy, reg)
	sup.SetCeilings(map[string]systems.Ceiling{"commands": systems.Open()})
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

// envOr is the environment's value for key, or dflt where it sets none.
func envOr(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}
