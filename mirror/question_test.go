package mirror

import (
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// pose publishes one question, the way the registry's OnPost does.
func pose(t *testing.T, store entity.Store, id, gate, taskID string, effs ...task.Effect) {
	t.Helper()
	origin, _, _ := strings.Cut(id, task.GateSep)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: id, Origin: origin, Gate: gate, Subject: "demo:pr:1"},
		Tool:     "write",
		Effects:  effs,
	}, taskID)
}

// ids returns the question ids a pattern picks, so a test can say what a rule would fire on.
func ids(t *testing.T, store entity.Store, p entity.Pattern) []string {
	t.Helper()
	p.System, p.Kind = QuestionSystem, QuestionKind
	var out []string
	for _, ref := range p.Select(store) {
		out = append(out, ref.ID())
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
		task.Effect{Verb: task.Write, Resource: task.File("cfg/app.tony")})
	pose(t, store, "run-a~d2", "net-guard", "run-a",
		task.Effect{Verb: task.Network, Resource: task.Resource{Kind: task.KindHost, Ref: "api.example"}})
	pose(t, store, "run-b~d1", "write-guard", "run-b",
		task.Effect{Verb: task.Write, Resource: task.File("cfg/other.tony")})

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
	writeAt(store, QuestionRef("run-a~d2"), entity.Obj{"resolver": "verse:resolvers:llm:reviewer"})

	only(t, ids(t, store, entity.Pattern{
		Predicate: entity.Obj{"resolver": "verse:resolvers:llm:reviewer"}.Payload(),
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

// A question posed AGAIN under an id that already settled must read as a question, not as a
// question and its own answer at once.
//
// A trigger gate's id is a function of (rule, gate, subjects), so a re-firing about the same
// subject re-poses the same one — and a write merges, so `status: pending` landed on top of a
// verdict, a `by` and an `answeredAt` that stayed. What a person read on the docket was a
// question to decide, already stamped with who decided it and what they said.
func TestReposingOverASettledQuestionClearsTheAnswer(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "guard~1", "approval", "")
	AnswerQuestion(store, "guard~1", task.Resolution{
		Proceed: true, By: "human:scott", Steer: []string{"ship it"}})

	pose(t, store, "guard~1", "approval", "") // the rule fires again

	e, ok, err := store.Get(QuestionRef("guard~1"))
	if err != nil || !ok {
		t.Fatal("no record")
	}
	got := entity.ToObj(e.Payload)
	if got["status"] != QuestionPending {
		t.Fatalf("status = %v, want pending — it is genuinely being asked again", got["status"])
	}
	for _, k := range []string{"verdict", "by", "say", "answeredAt"} {
		if got[k] != nil {
			t.Fatalf("%s = %v survived the re-posing: the docket shows a question already "+
				"stamped with its own answer (%+v)", k, got[k], got)
		}
	}
	// And says so, since the cleared fields no longer can. The earlier answer is not lost —
	// it is at the commit that made it.
	if got["askedAgain"] != true {
		t.Fatalf("nothing marks this as asked before: %+v", got)
	}
}

// The ordinary case is untouched: a first posing has nothing to clear and says nothing about
// having been asked before.
func TestAFirstPosingIsNotMarked(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "guard~2", "approval", "")
	e, _, _ := store.Get(QuestionRef("guard~2"))
	if got := entity.ToObj(e.Payload); got["askedAgain"] != nil {
		t.Fatalf("a first posing was marked as a repeat: %+v", got)
	}
}

// A withdrawn question says WHY it was withdrawn, and does not overwrite what was asked.
//
// `orphaned` used to have one cause — the asker died — and now has two: a task's loop withdraws
// a standing question when an operator intervenes, and there the asker is alive and somebody
// chose this. Same settling, and to whoever was about to answer, entirely different news. The
// status alone cannot tell them apart, so `why` does.
//
// It goes in `why` rather than `reason` because `reason` is the gate's own account of why it
// MATCHED, written when the question was posed. Overwriting it would trade what was being asked
// for why nobody answered, and the docket needs both.
func TestAWithdrawnQuestionSaysWhyAndKeepsWhatWasAsked(t *testing.T) {
	store := entitytest.Store(t)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: "run-1~d1", Origin: "run-1", Gate: "write",
			Reason: "tool write_file"},
		Tool: "write_file",
	}, "run-1")

	DropQuestion(store, "run-1~d1", "an operator said something while this was being asked about")

	e, ok, err := store.Get(QuestionRef("run-1~d1"))
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	got := entity.ToObj(e.Payload)
	if got["status"] != QuestionOrphaned {
		t.Errorf("status = %v, want %s", got["status"], QuestionOrphaned)
	}
	why, _ := got["why"].(string)
	if !strings.Contains(why, "operator") {
		t.Errorf("why = %q — a docket that says only `orphaned` reads as \"the task died\"", why)
	}
	if got["reason"] != "tool write_file" {
		t.Errorf("reason = %v, want the gate's own account of what was asked", got["reason"])
	}
	if got["orphanedAt"] == nil {
		t.Error("no orphanedAt: the settling has no time on it")
	}
}

// An answer still wins over a withdrawal that crosses it: DropQuestion touches only a question
// that is still pending, because what was DECIDED is the fact worth keeping.
func TestAWithdrawalDoesNotOverwriteAnAnswer(t *testing.T) {
	store := entitytest.Store(t)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: "run-1~d1", Gate: "write"},
	}, "run-1")
	AnswerQuestion(store, "run-1~d1", task.Resolution{Proceed: true, By: "human:scott"})

	DropQuestion(store, "run-1~d1", "an operator said something while this was being asked about")

	e, _, _ := store.Get(QuestionRef("run-1~d1"))
	got := entity.ToObj(e.Payload)
	if got["status"] != QuestionAnswered {
		t.Fatalf("status = %v, want the answer to survive the crossing withdrawal", got["status"])
	}
	if got["by"] != "human:scott" {
		t.Errorf("by = %v — the decision lost its author", got["by"])
	}
}
