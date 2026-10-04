package trigger

import (
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A supervisor is a charter rule about questions. Nothing in the engine knows that — this is
// the ordinary `when` over an ordinary entity — and that is the whole claim: publishing the
// question as a record made supervision sayable in the grammar that already exists, rather
// than in a mechanism that has to be built to carry it.
//
// The rule is written the way a charter is, operators and all, because a shape that only
// works when a test constructs it in Go is not a shape an operator has.
func TestACharterRuleFiresOnAPendingQuestion(t *testing.T) {
	store := entitytest.Store(t)
	eng := New(store, task.AutoResolver{})
	eng.Start(t.Context())

	var trig Trigger
	if err := codec.Unmarshal([]byte(`{
		name: escalate-writes,
		condition: {
			subject: {path: "verse.ask.*", value:  {status: pending, effects: {verbs: [write]}}},
		},
		action: {patch: {escalated: true}},
	}`), &trig, codec.Tony); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := eng.AddTrigger(trig); err != nil {
		t.Fatalf("AddTrigger: %v", err)
	}

	pose := func(id, gate string, eff task.Effect) {
		mirror.PostQuestion(store, task.Pending{
			Question: task.GateQuestion{ID: id, Origin: "run-a", Gate: gate},
			Tool:     "act",
			Effects:  []task.Effect{eff},
		}, "run-a")
	}
	pose("run-a~d1", "write-guard", task.Effect{Verb: task.Write, Resource: task.File("cfg/app.tony")})
	pose("run-a~d2", "read-guard", task.Effect{Verb: task.Read, Resource: task.File("cfg/app.tony")})

	escalated := func(id string) bool {
		e, _, _ := store.Get(mirror.QuestionRef(id))
		return entity.ToObj(e.Payload)["escalated"] == true
	}
	deadline := time.After(2 * time.Second)
	for !escalated("run-a~d1") {
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.QuestionRef("run-a~d1"))
			t.Fatalf("the rule never fired on the write question; have %+v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
	// And not on the read. A supervisor that fires on everything is not a supervisor; the
	// discrimination has to come from the derived effects, which is the only thing on the
	// record that says what the action would do.
	if escalated("run-a~d2") {
		t.Fatal("the rule fired on a read question — verbs: [write] selected everything")
	}
}
