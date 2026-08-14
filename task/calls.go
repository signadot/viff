package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/parse"
)

// A Call is one entry of a call list: an action to take, and which of its outcomes end the
// list. It is the unit of the `calls` driver — work with one right answer, proposed in
// advance rather than decided a turn at a time by a model.
//
// It lives in `task` rather than in a runtime because both verse and a coding runner need it,
// and the case it exists for is the runner's mechanical tasks: a merge is four git commands,
// and driving them with an LLM pays a model to rediscover them, hunt the filesystem for a
// credential, and occasionally do something else entirely.
//
// What it is NOT is an escape from supervision. Every entry becomes an ordinary ToolUse, is
// derived and gated like any other action, and the executor cannot tell which driver proposed
// it. That is the property that makes this cheap: nothing downstream changes.
type Call struct {
	Tool  string
	Input map[string]any

	// Fatal names the outcomes of THIS call that end the list, out of the three below.
	//
	// It is per call because the rule `scripted` inherits — an Err ends the run, a Blocked
	// does not — is right for two reasons that do not always agree. A refusal is a decision
	// about ONE call: a sweep deleting ten refs where the remote declines one should delete
	// the other nine. A merge is the opposite: if `git merge --ff-only` did not merge, the
	// push that follows it must not happen. Both are call lists, and only the author knows
	// which.
	//
	// nil (absent) means ALL THREE: a list is ordered because its calls depend on each
	// other, so stopping is the safe read, and the caller that wants otherwise says so.
	// Empty-but-present (`fatal: []`) means none of them — a list that runs to the end
	// whatever happens, which is a thing to be able to say and not the thing to default to.
	Fatal []StepOutcome
}

// StepOutcome is one way an executed call can turn out badly. Three, and the third is the
// interesting one.
type StepOutcome string

const (
	// FatalErr is StepResult.Err: the step failed.
	FatalErr StepOutcome = "err"
	// FatalBlocked is StepResult.Blocked: it was refused, by a gate or by the executor
	// itself (a runtime tool declining, which is what push.go's refusal already is).
	FatalBlocked StepOutcome = "blocked"
	// FatalNonzero is a command that ran and exited non-zero.
	//
	// It is a member of this vocabulary because THE TWO EXECUTORS ALREADY DISAGREE about a
	// non-zero exit, and a call list is the first thing that would notice. versed's
	// shellExec folds it into Err deliberately — a failure nothing reports is a failure
	// that reads as success. The harness's fsExec reports the exit code as data, equally
	// deliberately, because the agent reads it and decides what to do. Neither is wrong for
	// its own driver, and reconciling them would change what a model sees in a run_bash
	// result. So a call names the outcome it cares about, both executors keep their
	// reporting, and a list does not have to know which one it is running on.
	FatalNonzero StepOutcome = "nonzero"
)

func (o StepOutcome) valid() bool {
	switch o {
	case FatalErr, FatalBlocked, FatalNonzero:
		return true
	}
	return false
}

// outcomes reports how r turned out, worst first — the order they are reported in, so a call
// that failed AND was left with a non-zero exit is described as having failed.
func outcomes(r StepResult) []StepOutcome {
	var out []StepOutcome
	if r.Err != nil {
		out = append(out, FatalErr)
	}
	if r.Blocked {
		out = append(out, FatalBlocked)
	}
	if r.Exit != nil && *r.Exit != 0 {
		out = append(out, FatalNonzero)
	}
	return out
}

// fatalFor reports the first outcome of r this call declared fatal.
func (c Call) fatalFor(r StepResult) (StepOutcome, bool) {
	for _, o := range outcomes(r) {
		if c.Fatal == nil { // absent: all three
			return o, true
		}
		for _, f := range c.Fatal {
			if f == o {
				return o, true
			}
		}
	}
	return "", false
}

// describe says what happened, for the error that ends the run. It carries the executor's own
// words where there are any: a push refused for a destination the environment does not declare
// should say that, not "blocked".
func describe(o StepOutcome, r StepResult) string {
	switch o {
	case FatalErr:
		return "failed: " + r.Err.Error()
	case FatalBlocked:
		if n := strings.TrimSpace(r.Note); n != "" {
			return "refused: " + n
		}
		return "refused"
	case FatalNonzero:
		return fmt.Sprintf("exited %d", *r.Exit)
	}
	return string(o)
}

// callsDriver walks a fixed list, one call per boundary.
type callsDriver struct {
	calls []Call
	i     int
}

// NewCallsDriver returns a driver that proposes calls in order.
func NewCallsDriver(calls []Call) Driver { return &callsDriver{calls: calls} }

// Next proposes the next call, unless the last one ended the list.
//
// A fatal outcome FAILS the run rather than completing it early, which is `scripted`'s
// argument applied to a list: there is nobody to re-plan, so a list that did not run to its
// end is not a run that succeeded, and a trigger's onDone firing on it would be reporting work
// that did not happen. The error names which call and why, because a list is opaque from the
// outside — "call 2 of 4 (push): refused" is the whole of what a reader gets.
func (d *callsDriver) Next(_ context.Context, prev StepResult) (ToolUse, bool, error) {
	if d.i > 0 {
		c := d.calls[d.i-1]
		if o, fatal := c.fatalFor(prev); fatal {
			return ToolUse{}, false, fmt.Errorf("call %d of %d (%s): %s",
				d.i, len(d.calls), c.Tool, describe(o, prev))
		}
	}
	if d.i >= len(d.calls) {
		return ToolUse{}, false, nil
	}
	c := d.calls[d.i]
	d.i++
	return ToolUse{Name: c.Tool, Input: c.Input}, true, nil
}

// NewCallsTask builds a task that runs a call list carried in the launch's prompt.
//
// It is the shared builder both runtimes call, and the two refusals below are why it exists as
// a constructor rather than as a driver anyone can wire up:
//
//   - AN ANSWER CONTRACT FAILS THE SPAWN. There is no model to ask again, so a contract this
//     task cannot possibly satisfy would be silently dropped — and a contract dropped is worse
//     than none, since the charter downstream is written believing it holds. Launch.Answer says
//     as much: a runtime whose driver cannot enforce a contract must fail the spawn.
//   - A LIST THAT WILL NOT PARSE FAILS THE SPAWN, rather than running the prefix that did.
//
// Both fail as a HOSTED task (NewFailed) rather than as a nil: the spawn errors where it can be
// read, with `task get` able to say why, while the engine keeps running.
func NewCallsTask(l Launch, reg Resolver, exec Executor) *Task {
	if l.Answer != nil {
		return NewFailed(l.ID, reg, "calls: a call list cannot enforce an answer contract (no model to repair one)")
	}
	calls, err := ParseCalls(l.Prompt)
	if err != nil {
		return NewFailed(l.ID, reg, "calls: "+err.Error())
	}
	vec := l.Vector
	if vec == nil {
		vec = &Vector{Default: Ask}
	}
	return NewTask(l.ID, NewSupervisor(vec, reg), NewCallsDriver(calls), exec)
}

// ParseCalls reads a call list from a tony document:
//
//	calls:
//	- tool: run_bash
//	  input: {command: "git merge --ff-only origin/topic"}
//	- tool: push
//	  input: {destination: main}
//	  fatal: [err]
//
// The whole source must be the document. A prompt with prose around a list is refused rather
// than scavenged, because the failure mode of scavenging is a list that silently lost an entry.
//
// This is where a call list lives until it is a field on the spawn — see
// docs/working/calls-driver-plan.md, step 3.5, which is also where per-value `$subject`
// expansion and a fire-time check come from. Until then the document is checked here, at the
// spawn, which is late but is still before anything runs.
func ParseCalls(src string) ([]Call, error) {
	n, err := parse.Parse([]byte(src))
	if err != nil {
		return nil, fmt.Errorf("not a tony document: %w", err)
	}
	var doc map[string]any
	if err := gomap.FromTonyIR(n, &doc); err != nil {
		return nil, fmt.Errorf("not a tony map: %w", err)
	}
	raw, ok := doc["calls"]
	if !ok {
		return nil, errors.New(`no "calls:" list`)
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New(`"calls:" must be a list`)
	}
	if len(list) == 0 {
		return nil, errors.New(`"calls:" is empty`)
	}
	out := make([]Call, 0, len(list))
	for i, e := range list {
		c, err := parseCall(e)
		if err != nil {
			return nil, fmt.Errorf("call %d of %d: %w", i+1, len(list), err)
		}
		out = append(out, c)
	}
	return out, nil
}

func parseCall(e any) (Call, error) {
	m, ok := e.(map[string]any)
	if !ok {
		return Call{}, errors.New("must be a map with a tool")
	}
	tool, _ := m["tool"].(string)
	if strings.TrimSpace(tool) == "" {
		return Call{}, errors.New(`no "tool:"`)
	}
	c := Call{Tool: tool}
	if v, ok := m["input"]; ok {
		im, ok := v.(map[string]any)
		if !ok {
			return Call{}, fmt.Errorf("%s: input must be a map", tool)
		}
		c.Input = im
	}
	if v, ok := m["fatal"]; ok {
		fs, ok := v.([]any)
		if !ok {
			return Call{}, fmt.Errorf("%s: fatal must be a list", tool)
		}
		// Non-nil even when empty: `fatal: []` is a statement, and the difference between
		// it and an absent field is the difference between "nothing stops this list" and
		// "anything does".
		c.Fatal = []StepOutcome{}
		for _, f := range fs {
			s, _ := f.(string)
			o := StepOutcome(s)
			if !o.valid() {
				return Call{}, fmt.Errorf("%s: fatal %q is not one of err, blocked, nonzero", tool, s)
			}
			c.Fatal = append(c.Fatal, o)
		}
	}
	return c, nil
}
