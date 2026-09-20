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
//
//tony:schemagen=verse-call,notag
type Call struct {
	Tool  string         `tony:"field=tool"`
	Input map[string]any `tony:"field=input,omitzero"`

	// Fatal names the outcomes of THIS call that end the list: `err`, `blocked`, `nonzero`.
	//
	// It is per call because the rule `scripted` inherits — an Err ends the run, a Blocked
	// does not — is right for two reasons that do not always agree. A refusal is a decision
	// about ONE call: a sweep deleting ten refs where the remote declines one should delete
	// the other nine. A merge is the opposite: if `git merge --ff-only` did not merge, the
	// push that follows it must not happen. Both are call lists, and only the author knows
	// which.
	//
	// A POINTER, because the field has three states and a plain slice has two:
	//
	//	(absent)       all of them — a list is ordered because its calls depend on each
	//	               other, so stopping is the safe read for a call that says nothing
	//	fatal: []      none of them — this call ends nothing, whatever happens to it
	//	fatal: [err]   these
	//
	// The empty case is a real thing to say — it is what a sweep says — and it is not the
	// default, so the two must not collapse. A plain slice collapses them in whichever
	// direction the tag picks: with `omitzero` an empty list never crosses the wire, and
	// without it nil and empty both emit `[]`. Requires go-tony ≥ v0.0.131, where a pointer
	// carries the distinction and a scalar written into it is an error rather than an
	// invented empty.
	Fatal *[]StepOutcome `tony:"field=fatal,omitzero"`
}

// DriverCalls is the driver name a spawn writes to get a call list. It is a `task` word rather
// than a runtime's because every runtime that builds one has to agree on it — and because a
// spawn carrying `calls:` selects it without writing it, which only works if there is one name.
const DriverCalls = "calls"

// Fatals is what a caller in Go writes for the field: `Fatals(FatalErr)` for one, `Fatals()`
// for none, and a nil Fatal for the default. It exists so the three states are said in words
// at a call site rather than by taking the address of a slice literal.
func Fatals(os ...StepOutcome) *[]StepOutcome {
	if os == nil {
		os = []StepOutcome{}
	}
	return &os
}

// CheckCalls is what a call list has to satisfy before anything is built from it, and it is
// exported because the engine checks it at FIRE TIME — before a spawn crosses to a runner —
// alongside the answer contract and the named vector. A charter that wrote something it cannot
// mean fails where its author can see it, rather than on a machine they do not have.
//
// What it cannot check is the tool NAMES: `push` belongs to a runner's vocabulary and not to
// the engine's, so an unknown one is refused at the far end (harness.Calls) on the same
// argument `driver:` and `environment:` already travel by.
func CheckCalls(cs []Call) error {
	if len(cs) == 0 {
		return errors.New("no calls")
	}
	for i, c := range cs {
		if strings.TrimSpace(c.Tool) == "" {
			return fmt.Errorf("call %d of %d: no tool", i+1, len(cs))
		}
		if c.Fatal == nil {
			continue
		}
		for _, f := range *c.Fatal {
			if !f.valid() {
				return fmt.Errorf("call %d of %d (%s): fatal %q is not one of %s, %s, %s",
					i+1, len(cs), c.Tool, f, FatalErr, FatalBlocked, FatalNonzero)
			}
		}
	}
	return nil
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
		if c.Fatal == nil { // absent: all of them
			return o, true
		}
		for _, f := range *c.Fatal {
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

// NewCallsTask builds a task that runs the call list its launch CARRIES.
//
// The list is a field (Launch.Calls, launch.Spec.Calls) rather than a document inside the
// prompt, and the difference is what a reader gets: the engine can check it at fire time, a
// charter can be shown it, and a value with a quote in it is a value rather than something that
// has to survive being spliced into text and parsed back out. See
//
// It is the shared builder both runtimes call, and the refusal below is why it exists as a
// constructor rather than as a driver anyone can wire up: AN ANSWER CONTRACT FAILS THE SPAWN.
// There is no model to ask again, so a contract this task cannot possibly satisfy would be
// silently dropped — and a contract dropped is worse than none, since the charter downstream is
// written believing it holds. Launch.Answer says as much: a runtime whose driver cannot enforce
// a contract must fail the spawn.
//
// It fails as a HOSTED task (NewFailed) rather than as a nil: the spawn errors where it can be
// read, with `task get` able to say why, while the engine keeps running.
func NewCallsTask(l Launch, reg Resolver, exec Executor) *Task {
	if err := CheckCalls(l.Calls); err != nil {
		return NewFailed(l.ID, reg, driverName(l)+": "+err.Error())
	}
	return NewCallsTaskFrom(l, l.Calls, reg, exec)
}

// NewCallsTaskFrom is the same, for a caller whose list is not the launch's — the scripted
// driver, whose one call is built from a shell command.
//
// THE VECTOR COMES FROM THE SPAWN, and a spawn that names none gets ASK. That is the whole of
// what a call list is allowed to assume about its own authority: the charter that wrote the
// list is the party that knows whether it should run unattended, and a driver deciding for
// itself is how `scripted` came to be exempt from a vector it was handed and ignored.
//
// Ask rather than auto because it is what an `llm` spawn already lives with — harness.Vector()
// asks on write, shell and delegate — and a list of shell commands nobody vetted is not the
// case for a weaker default than the one an agent gets.
func NewCallsTaskFrom(l Launch, calls []Call, reg Resolver, exec Executor) *Task {
	if l.Answer != nil {
		return NewFailed(l.ID, reg, driverName(l)+": cannot enforce an answer contract (there is no model to repair one)")
	}
	vec := l.Vector
	if vec == nil {
		vec = &Vector{Default: Ask}
	}
	return NewTask(l.ID, NewSupervisor(vec, reg), NewCallsDriver(calls), exec)
}

// driverName is what to call this task in a refusal — the driver the spawn asked for, since
// "calls" is a word the author of a `driver: scripted` charter never wrote.
func driverName(l Launch) string {
	if n := strings.TrimSpace(l.Driver); n != "" {
		return n
	}
	return "calls"
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
// The whole source must be the document. Prose around a list is refused rather than scavenged,
// because the failure mode of scavenging is a list that silently lost an entry.
//
// It is NOT how a list reaches a task — that is the `calls:` field, and there is deliberately
// only one wire form. This is for a person with a list in a file or in their hands: `verse task
// run --driver calls "$(cat merge.tony)"` parses here, client-side, and sends the field. So a
// bad list is a message on the terminal of whoever typed it.
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
			return Call{}, fmt.Errorf("%s: fatal is a list of %s, %s, %s (or [] for none)",
				tool, FatalErr, FatalBlocked, FatalNonzero)
		}
		// Non-nil even when empty: `fatal: []` is the statement that this call ends nothing,
		// which is a different thing from not writing the field at all.
		out := make([]StepOutcome, 0, len(fs))
		for _, f := range fs {
			s, _ := f.(string)
			out = append(out, StepOutcome(s))
		}
		c.Fatal = &out
	}
	if err := CheckCalls([]Call{c}); err != nil {
		// One call, so the "call 1 of 1" prefix says nothing; the rest is what went wrong.
		return Call{}, errors.New(strings.TrimPrefix(err.Error(), "call 1 of 1: "))
	}
	return c, nil
}
