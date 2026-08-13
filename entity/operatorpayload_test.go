package entity

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// A payload carrying a live merge operator does not just read back oddly — it makes the
// WHOLE STORE unreadable, permanently, at every path.
//
// A write is a patch, so a tag naming a merge operation is executed rather than stored. That
// much is documented (raw.go) and is why Raw/AsData exist. What was not written down is the
// blast radius: the operator lands in the log, and every later read replays the log to
// materialize the document — so from then on, reading ANY entity fails with "irtype patching
// null gave cannot patch with irtype operation". Not the poisoned entity. Any of them.
//
// A staging deployment did this, twice. Writes kept succeeding, which is the cruel part: the
// engine went on appending to a log nobody could ever read again, `verse entity get a:b:c` on
// an unrelated entity failed, and a wipe was the only way back. The tags came from an agent's
// answer — `answer.Extract` parses a driver's final message AS TONY, and an agent asked to
// satisfy a contract written in `!or` and `!irtype` sometimes echoes that shape back, so the
// document being stored was full of operators nobody wrote deliberately.
//
// This test is that outage in a few milliseconds, and it is here rather than in mirror/
// because the mechanism belongs to the store: any writer that forgets AsData does this, and
// the only real defence is that a data write should not be able to.
func TestAnOperatorTagInAPayloadDoesNotPoisonTheStore(t *testing.T) {
	s := openStore(t)

	// What ParseResult hands back when an agent echoes its contract: an ordinary document
	// whose values carry pattern tags.
	answer := ir.FromMap(map[string]*ir.Node{
		"status": ir.FromSlice([]*ir.Node{ir.FromString("open"), ir.FromString("resolved")}).WithTag("!or"),
		"says":   ir.FromString("what the comment claims, against what the code does").WithTag("!irtype"),
	})

	set(t, s, Ref{System: "task", Kind: "instance", ID: "run-1"}, Obj{
		"status": "done",
		"data":   AsData(answer), // the fix: what a projection stores is data
	}.Payload())

	// The entity itself round-trips.
	e, ok, err := s.Get(Ref{System: "task", Kind: "instance", ID: "run-1"})
	if err != nil || !ok {
		t.Fatalf("get the entity that was written: ok=%v err=%v", ok, err)
	}
	if got := ToObj(e.Payload)["status"]; got != "done" {
		t.Fatalf("status = %v, want done", got)
	}

	// And — the assertion this test exists for — so does everything else. An unrelated
	// entity written before it is still readable, which is precisely what stopped being
	// true in production.
	set(t, s, Ref{System: "demo", Kind: "pr", ID: "1"}, Obj{"stage": "open"}.Payload())
	if _, ok, err := s.Get(Ref{System: "demo", Kind: "pr", ID: "1"}); err != nil || !ok {
		t.Fatalf("an unrelated entity became unreadable: ok=%v err=%v", ok, err)
	}
	if n := len(s.List(nil)); n != 2 {
		t.Fatalf("List returned %d entities, want 2 — and note that a store which cannot "+
			"replay its own log reports this as an empty list rather than an error", n)
	}
}
