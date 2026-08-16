package entity

import (
	"strings"
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
// engine went on appending to a log nobody could ever read again, `verse state get a:b:c` on
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

	set(t, s, RefAt("task", "instance", "run-1"), Obj{
		"status": "done",
		"data":   AsData(answer), // the fix: what a projection stores is data
	}.Payload())

	// The entity itself round-trips.
	e, ok, err := s.Get(RefAt("task", "instance", "run-1"))
	if err != nil || !ok {
		t.Fatalf("get the entity that was written: ok=%v err=%v", ok, err)
	}
	if got := ToObj(e.Payload)["status"]; got != "done" {
		t.Fatalf("status = %v, want done", got)
	}

	// And — the assertion this test exists for — so does everything else. An unrelated
	// entity written before it is still readable, which is precisely what stopped being
	// true in production.
	set(t, s, RefAt("demo", "pr", "1"), Obj{"stage": "open"}.Payload())
	if _, ok, err := s.Get(RefAt("demo", "pr", "1")); err != nil || !ok {
		t.Fatalf("an unrelated entity became unreadable: ok=%v err=%v", ok, err)
	}
	if n := len(s.List(nil)); n != 2 {
		t.Fatalf("List returned %d entities, want 2 — and note that a store which cannot "+
			"replay its own log reports this as an empty list rather than an error", n)
	}
}

// The STORE refuses a match operator, so no writer can make the store unreadable.
//
// Twice before, an `!irtype` reached a write and cost a staging deployment: the write succeeds
// (the log appends before it applies), every later read replays the log and fails with "cannot
// patch with irtype operation", and from then on nothing can be read while writes go on
// succeeding. Both earlier fixes were at writers — the mirror escapes an agent's answer, a
// charter is stored raw — and a third writer found its way in anyway, which is the argument for
// putting the check where every write passes.
func TestTheStoreRefusesAMatchOperatorInAWrittenValue(t *testing.T) {
	for name, p := range map[string]Payload{
		"at the top": ir.FromString("x").WithTag("!irtype"),
		"in a field": ir.FromMap(map[string]*ir.Node{
			"says": ir.FromString("a string").WithTag("!irtype"),
		}),
		"nested deeper": ir.FromMap(map[string]*ir.Node{
			"answer": ir.FromMap(map[string]*ir.Node{
				"merged": ir.FromString("true").WithTag("!irtype"),
			}),
		}),
		"under a list": ir.FromMap(map[string]*ir.Node{
			"findings": ir.FromSlice([]*ir.Node{ir.FromMap(map[string]*ir.Node{
				"status": ir.FromSlice([]*ir.Node{
					ir.FromString("open"), ir.FromString("resolved"),
				}).WithTag("!or"),
			})}),
		}),
	} {
		err := CheckPatchable(p)
		if err == nil {
			t.Errorf("%s: accepted — this write makes the store unreadable", name)
			continue
		}
		// The message has to say what to do, because the caller is usually holding a pattern it
		// legitimately wants to STORE, and the answer is to escape it rather than drop it.
		if !contains(err.Error(), "AsData") {
			t.Errorf("%s: the refusal does not say how to store it as data: %v", name, err)
		}
	}
}

// …and it refuses nothing a correct write does. A merge operator IS the grammar, and escaped
// data is data however many operators it holds — which is what keeps AsData and Raw the right
// thing at the writers rather than a workaround for this check.
func TestTheStoreStillAcceptsEveryLegitimateWrite(t *testing.T) {
	pattern := ir.FromMap(map[string]*ir.Node{
		"says": ir.FromString("x").WithTag("!irtype"),
	})
	for name, p := range map[string]Payload{
		"ordinary data": ir.FromMap(map[string]*ir.Node{"state": ir.FromString("open")}),
		"layout tags": ir.FromMap(map[string]*ir.Node{
			"list": ir.FromSlice([]*ir.Node{ir.FromString("a")}).WithTag("!bracket"),
		}),
		"a pattern escaped as data": AsData(pattern),
		"a whole document stored raw": Raw(ir.FromMap(map[string]*ir.Node{
			"answer": ir.FromMap(map[string]*ir.Node{
				"merged": ir.FromString("true").WithTag("!irtype"),
			}),
		})),
	} {
		if err := CheckPatchable(p); err != nil {
			t.Errorf("%s: refused a legitimate write: %v", name, err)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
