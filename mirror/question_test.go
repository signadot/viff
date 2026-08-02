package mirror

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/process"
)

// pose publishes one question, the way the registry's OnPost does.
func pose(t *testing.T, store entity.Store, id, gate, processID string, effs ...process.Effect) {
	t.Helper()
	origin, _, _ := strings.Cut(id, process.GateSep)
	PostQuestion(store, process.Pending{
		Question: process.GateQuestion{ID: id, Origin: origin, Gate: gate, Subject: "demo:pr:1"},
		Tool:     "write",
		Effects:  effs,
	}, processID)
}

// ids returns the question ids a pattern picks, so a test can say what a rule would fire on.
func ids(t *testing.T, store entity.Store, p entity.Pattern) []string {
	t.Helper()
	p.System, p.Kind = QuestionSystem, QuestionKind
	var out []string
	for _, ref := range p.Select(store) {
		out = append(out, ref.ID)
	}
	return out
}

func only(t *testing.T, got []string, want string) {
	t.Helper()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("selected %v, want exactly [%s]", got, want)
	}
}

// The point of the record is that a plain pattern reaches it. Nothing here calls a registry
// or asks a daemon what it is blocked on: a question is an entity, so the same predicate a
// charter's `when` becomes selects one.
func TestAPatternSelectsPendingQuestions(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "run-a~d1", "write-guard", "run-a",
		process.Effect{Verb: process.Write, Resource: process.File("cfg/app.tony")})
	pose(t, store, "run-a~d2", "net-guard", "run-a",
		process.Effect{Verb: process.Network, Resource: process.Resource{Kind: process.KindHost, Ref: "api.example"}})
	pose(t, store, "run-b~d1", "write-guard", "run-b",
		process.Effect{Verb: process.Write, Resource: process.File("cfg/other.tony")})

	// By the run that raised it. The id carries its origin, so one run's questions are
	// selectable without a second field to join on.
	if got := ids(t, store, entity.Pattern{Predicate: entity.Obj{"origin": "run-a"}.Payload()}); len(got) != 2 {
		t.Fatalf("selected %v, want both of run-a's questions", got)
	}

	// By what the action would DO. This is the one a supervisor cares about: `verbs: [write]`
	// is the sentence a charter rule says, and it has to be true of the record without the
	// reader knowing the shape of a Go Effect. It picks both writes and leaves the network
	// question alone, which is the discrimination that makes it worth saying.
	if got := ids(t, store, entity.Pattern{
		At:        []string{"effects"},
		Predicate: entity.Obj{"verbs": []any{"write"}}.Payload(),
	}); len(got) != 2 {
		t.Fatalf("selected %v on verbs: [write], want both writes and not the network gate", got)
	}

	// And by the path, which is the same fact reached the other way — a rule that guards a
	// directory says it in paths, not in resources.
	only(t, ids(t, store, entity.Pattern{
		At:        []string{"effects"},
		Predicate: entity.Obj{"paths": []any{"cfg/app.tony"}}.Payload(),
	}), "run-a~d1")
}

// A question is addressable, which is what makes routing one to a supervisor expressible at
// all: the resolver it is addressed to is on the record, so a resolver selects its own docket
// with the same machinery and sees nobody else's.
func TestAPatternSelectsByResolver(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "run-a~d1", "write-guard", "run-a")
	pose(t, store, "run-a~d2", "write-guard", "run-a")
	writeAt(store, QuestionRef("run-a~d2"), entity.Obj{"resolver": "resolvers:llm:reviewer"})

	only(t, ids(t, store, entity.Pattern{
		Predicate: entity.Obj{"resolver": "resolvers:llm:reviewer"}.Payload(),
	}), "run-a~d2")

	// The unaddressed one is not addressed to "": the key is absent, which is what a later
	// layer reads as UNBOUND. Selecting on the empty string must not find it, or "nobody has
	// claimed this" and "someone claimed it with a blank name" would be the same state.
	if got := ids(t, store, entity.Pattern{Predicate: entity.Obj{"resolver": ""}.Payload()}); len(got) != 0 {
		t.Fatalf("selected %v on resolver: \"\", want none — absent is not empty", got)
	}
}

// The negative that matters. A tool nothing could classify derives no effects, and the
// temptation is to skip the record or to write `effects: {}`. Either one loses the question:
// the first hides it from the docket entirely, the second makes an unclassifiable action look
// like a harmless one. It publishes what it knows and says nothing it does not.
func TestAQuestionWithNoEffectsIsStillOnTheDocket(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "run-a~d1", "unknown-tool", "run-a")

	e, ok, _ := store.Get(QuestionRef("run-a~d1"))
	if !ok {
		t.Fatal("a question whose tool derived no effects was not published at all")
	}
	got := entity.ToObj(e.Payload)
	for _, k := range []string{"id", "gate", "subject"} {
		if got[k] == nil || got[k] == "" {
			t.Fatalf("%s is missing from an unclassified question: %+v", k, got)
		}
	}
	if _, present := got["effects"]; present {
		t.Fatalf("effects rendered for an action that classified to nothing: %+v", got)
	}

	// And it must not be reachable by a rule about effects. A guard on writes that caught
	// the unclassifiable would be a guard that claims to know what it does not.
	if got := ids(t, store, entity.Pattern{
		At:        []string{"effects"},
		Predicate: entity.Obj{"verbs": []any{"write"}}.Payload(),
	}); len(got) != 0 {
		t.Fatalf("selected %v on verbs: [write], want none", got)
	}
}
