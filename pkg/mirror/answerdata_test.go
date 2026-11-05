package mirror

import (
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

// An agent's answer is parsed as tony, so an agent that echoes its contract hands the store
// a document made of merge operators. This is the guard that it lands as data.
//
// It is worth stating what "echoes its contract" means, because it sounds like a badly
// behaved agent and is not. A contract is a PATTERN — `status: !or [open, resolved]`,
// `says: !irtype string` — and the driver shows it to the model as the shape its answer must
// have. A model handed a shape and asked for an instance of it will sometimes return the
// shape. That is a reasonable misreading of an ambiguous instruction (it is why
// Contract.Example exists), and the cost of it must not be an unreadable store.
func TestAnAnswerEchoingItsContractLandsAsData(t *testing.T) {
	// A model's final message, contract shape and all.
	const said = "Here is the result:\n\n```\nstatus: !or [open, resolved]\nsays: !irtype string\n```\n"

	d := ParseResult(said)
	if d == nil {
		t.Fatal("no document extracted from an answer that carries one")
	}

	// Stored, and then read back — the read is the assertion, since an operator that reached
	// the log takes every later read with it, not only this entity's.
	s := entitytest.Store(t)
	ref := entity.VerseRef(System, InstanceKind, "run-1")
	entitytest.Set(t, s, ref, entity.Obj{"status": "done", "data": d}.Payload())

	if _, ok, err := s.Get(ref); err != nil || !ok {
		t.Fatalf("get the task record: ok=%v err=%v", ok, err)
	}
	other := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, s, other, entity.Obj{"stage": "open"}.Payload())
	if _, ok, err := s.Get(other); err != nil || !ok {
		t.Fatalf("an unrelated entity became unreadable after the answer was stored: ok=%v err=%v", ok, err)
	}
}

// The ordinary case pays nothing: an answer with no tags is stored exactly as it was
// written, so `.[data.x]` reads what the agent said and not around an escape.
func TestAnOrdinaryAnswerIsUnchanged(t *testing.T) {
	d := ParseResult("done\n\n```\n{\"status\": \"fixed\", \"file\": \"docs/runners.md\"}\n```\n")
	if d == nil {
		t.Fatal("no document extracted")
	}
	obj := entity.ToObj(d)
	if obj["status"] != "fixed" || obj["file"] != "docs/runners.md" {
		t.Fatalf("the answer did not survive intact: %v", obj)
	}
	if strings.Contains(d.Tag, "raw") {
		t.Fatalf("an untagged document was escaped anyway: tag %q", d.Tag)
	}
}
