// Package tasktest is a conformance suite for producers of task.Snapshot.
//
// verse reports a task through one shape wherever it runs — `verse task`, `task get` and the
// mirrored entity all read task.Snapshot (`task get` as verse's api.Task, which carries
// it) — but that shape is PRODUCED independently by each runner: runnerd projects it from a
// hosted task, a remote runner from its own worker's reported state. Nothing made two
// producers agree. Every field is a plain struct member, so a producer that never sets one
// still compiles, and a producer that sets the wrong one type-checks perfectly.
//
// That is not a hypothetical. Across one afternoon: one producer narrowed a raised gate to
// id+name and dropped the --reason a resolver reads to decide; runnerd passed the selected
// driver to its builder and kept no record, so an auto-selected doer was observable nowhere;
// and one producer filed a failure reason as a Result because its wire carried both in one
// field, while an identical local failure reported an Err.
// Each was found by hand, by asking one producer a question and then asking the other.
//
// An interface alone does not fix this, and it is worth being precise about why, because the
// obvious fix was already in place: Err() IS a method on task.Handle, both producers were
// forced to implement it, and one producer implemented it as `return ""` with a comment
// explaining that the protocol had no error channel. The comment was wrong. A compiler can
// demand that a question be answered; it cannot tell that the answer is false.
//
// So this suite asserts BEHAVIOUR, over every producer, from the outside: drive a task to
// a known state and ask what it reports. A stub cannot pass "after a failure, Err is
// non-empty" no matter how well argued the comment above it is.
//
// Registering a producer is the whole cost of joining:
//
//	func TestConformance(t *testing.T) { tasktest.Run(t, &myProducer{}) }
//
// LIMIT, stated plainly: this only asserts invariants somebody thought to write. It would
// not have caught PendingGate.Origin being empty on a relayed leg, because that one is
// CORRECTLY empty — an executor raises a gate without deciding who answers it. Telling a
// structural blank from a dropped value still needs a person who knows what the field means.
// What the suite buys is that "silently different across runners" becomes "fails a named
// property", which is the class that actually cost us.
package tasktest

import (
	"testing"
	"time"

	"github.com/signadot/verse/task"
)

// Spec describes the task a producer should start, and how (or whether) to drive it to a
// terminal state. Outcome is part of the spec rather than a later call because the two
// producers differ on exactly this: a runnerd task begins executing the instant it is
// spawned and cannot be told to fail afterward, while a relayed one waits for a worker to
// report. Naming the intent up front is the only phrasing both can honor.
type Spec struct {
	Driver  string      // the driver name the caller asked for
	Outcome task.Status // "" leaves it running; Done or Failed drives it there
	Text    string      // the completion's answer, or the failure's reason
}

// Caps declares what a producer can be driven to do. A property whose capability is absent
// is skipped, loudly — a skip names what went untested, where a silently-narrowed assertion
// would report a pass the producer never earned.
type Caps struct {
	// FailureReason: a failure can carry a caller-chosen reason. Without it the suite still
	// requires that SOME account of the failure is reported, just not which words.
	FailureReason bool
	// CompletionAnswer: a completion can carry a caller-chosen answer.
	CompletionAnswer bool
	// ExecutorGates: a gate can be raised on a running task from outside its driver.
	ExecutorGates bool
}

// Producer is the adapter a runner supplies to join the suite.
type Producer interface {
	Name() string
	Caps() Caps
	// Start creates a task per spec and returns its id, having driven it to
	// spec.Outcome if one is named.
	Start(t *testing.T, spec Spec) string
	// RaiseGate opens a gate on a running task and returns its decision id. Called only
	// when Caps().ExecutorGates.
	RaiseGate(t *testing.T, id, gate, reason string) string
	// Snapshot returns the Snapshot this producer reports for id.
	Snapshot(t *testing.T, id string) (task.Snapshot, bool)
}

// Run executes every property against p.
func Run(t *testing.T, p Producer) {
	t.Helper()
	t.Run(p.Name()+"/driver-is-reported", func(t *testing.T) { driverIsReported(t, p) })
	t.Run(p.Name()+"/failure-is-an-err", func(t *testing.T) { failureIsAnErr(t, p) })
	t.Run(p.Name()+"/completion-is-a-result", func(t *testing.T) { completionIsAResult(t, p) })
	t.Run(p.Name()+"/gate-reason-survives", func(t *testing.T) { gateReasonSurvives(t, p) })
}

// The driver a spawn asked for is readable back off the task.
//
// It is the only durable record of WHO was picked when the doer came from a `provider`
// selection, and an auto selection deliberately opens no motion — so if the snapshot drops
// it, the choice is observable nowhere but a trace line.
func driverIsReported(t *testing.T, p Producer) {
	id := p.Start(t, Spec{Driver: "scripted"})
	inst := snapshot(t, p, id)
	if inst.Driver != "scripted" {
		t.Errorf("Driver = %q, want scripted — the spawn's chosen doer must be readable back", inst.Driver)
	}
}

// A failure is reported as an Err, never as a Result.
//
// "Why did this fail?" must be answerable from the same field regardless of where the work
// ran. Filing a reason as a Result also feeds it to the mirror's ParseResult, which mines
// `result` for structured data a failure sentence was never going to carry.
func failureIsAnErr(t *testing.T, p Producer) {
	const why = "the migration is not reversible"
	id := p.Start(t, Spec{Driver: "scripted", Outcome: task.Failed, Text: why})
	inst := waitTerminal(t, p, id)

	if inst.Status != task.Failed {
		t.Fatalf("Status = %q, want failed", inst.Status)
	}
	if inst.Err == "" {
		t.Error("Err is empty after a failure — a status says only THAT it ended, never why")
	}
	if p.Caps().FailureReason && inst.Err != why {
		t.Errorf("Err = %q, want the reported reason %q", inst.Err, why)
	}
	if inst.Result != "" {
		t.Errorf("Result = %q after a failure, want empty — a reason is not the task's answer", inst.Result)
	}
}

// A completion is reported as a Result, never as an Err — the same routing, from the other
// side, so the property cannot be satisfied by filing everything as an error.
func completionIsAResult(t *testing.T, p Producer) {
	if !p.Caps().CompletionAnswer {
		t.Skip("producer cannot carry a caller-chosen completion answer")
	}
	const answer = "LGTM — one nit on the token TTL"
	id := p.Start(t, Spec{Driver: "scripted", Outcome: task.Done, Text: answer})
	inst := waitTerminal(t, p, id)

	if inst.Status != task.Done {
		t.Fatalf("Status = %q, want done", inst.Status)
	}
	if inst.Result != answer {
		t.Errorf("Result = %q, want %q", inst.Result, answer)
	}
	if inst.Err != "" {
		t.Errorf("Err = %q after a completion, want empty", inst.Err)
	}
}

// A gate's reason reaches the snapshot.
//
// The reason is the entire point of raising a gate rather than deciding: it is what the
// resolver reads. It reached the read plane and was dropped from the snapshot, so
// `verse question` listed the escalation with a blank where the justification belonged.
func gateReasonSurvives(t *testing.T, p Producer) {
	if !p.Caps().ExecutorGates {
		t.Skip("producer cannot raise a gate from outside the driver")
	}
	const why = "prod, off-hours"
	id := p.Start(t, Spec{Driver: "scripted"})
	did := p.RaiseGate(t, id, "deploy-approval", why)

	inst := snapshot(t, p, id)
	var got *task.PendingGate
	for i := range inst.Pending {
		if inst.Pending[i].ID == did {
			got = &inst.Pending[i]
		}
	}
	if got == nil {
		t.Fatalf("Pending = %+v, want the raised gate %s", inst.Pending, did)
	}
	if got.Gate != "deploy-approval" {
		t.Errorf("Gate = %q, want deploy-approval", got.Gate)
	}
	if got.Reason != why {
		t.Errorf("Reason = %q, want %q — the justification a resolver reads must survive", got.Reason, why)
	}
}

func snapshot(t *testing.T, p Producer, id string) task.Snapshot {
	t.Helper()
	inst, ok := p.Snapshot(t, id)
	if !ok {
		t.Fatalf("no snapshot for %s", id)
	}
	return inst
}

// waitTerminal polls until the task reports a terminal status. A runnerd task runs
// asynchronously, so its terminal state is not readable the instant Start returns.
func waitTerminal(t *testing.T, p Producer, id string) task.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		inst := snapshot(t, p, id)
		if inst.Status.Terminal() {
			return inst
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached a terminal status (last: %q)", id, inst.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
