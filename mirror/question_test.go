package mirror

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/task"
)

// pose publishes one question, the way the registry's OnPost does.
func pose(t *testing.T, store entity.Store, id, gate, taskID string, step task.Step) {
	t.Helper()
	origin, _, _ := strings.Cut(id, task.GateSep)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: id, Origin: origin, Gate: gate, Subject: `demo.pr."1"`, Step: step},
		Tool:     "write",
	}, taskID)
}

// writeStep is the step a write of path is; runStep the step running a command is.
func writeStep(path string) task.Step {
	return task.Step{Name: task.StepFilesWrite, Fields: map[string]string{"path": path}}
}
func runStep(command string) task.Step {
	return task.Step{Name: task.StepCommandsRun, Fields: map[string]string{"command": command}}
}

// ids returns the question ids a pattern picks, so a test can say what a rule would fire on.
func ids(t *testing.T, store entity.Store, p entity.Pattern) []string {
	t.Helper()
	p.Path = []*ir.Node{ir.FromString(entity.VerseRoot), ir.FromString(AskSystem),
		ir.FromString("*")}
	var out []string
	for _, ref := range p.Select(store) {
		out = append(out, ref.Name())
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
		writeStep("cfg/app.tony"))
	pose(t, store, "run-a~d2", "shell-guard", "run-a",
		runStep("curl api.example"))
	pose(t, store, "run-b~d1", "write-guard", "run-b",
		writeStep("cfg/other.tony"))

	// By the run that raised it: `from` names it, so one run's questions are selectable without a
	// second field to join on.
	if got := ids(t, store, entity.Pattern{Predicate: entity.Obj{FromField: FromTask("run-a")}.Payload()}); len(got) != 2 {
		t.Fatalf("selected %v, want both of run-a's questions", got)
	}

	// By what the action would DO. This is the one a supervisor cares about: `step: files.write`
	// is the sentence a charter rule says, and it has to be true of the record without the
	// reader knowing the shape of a Go Step. It picks both writes and leaves the command alone,
	// which is the discrimination that makes it worth saying.
	if got := ids(t, store, entity.Pattern{
		Predicate: entity.Obj{"step": task.StepFilesWrite}.Payload(),
	}); len(got) != 2 {
		t.Fatalf("selected %v on step: files.write, want both writes and not the command", got)
	}

	// And by the path, which is the same fact reached the other way — a rule that guards a
	// directory says it in the step's fields.
	only(t, ids(t, store, entity.Pattern{
		At:        []string{"fields"},
		Predicate: entity.Obj{"path": "cfg/app.tony"}.Payload(),
	}), "run-a~d1")
}

// The negative that matters. A tool nothing could classify derives no effects, and the
// temptation is to skip the record or to write `effects: {}`. Either one loses the question:
// the first hides it from the docket entirely, the second makes an unclassifiable action look
// like a harmless one. It publishes what it knows and says nothing it does not.
func TestAQuestionWithNoEffectsIsStillOnTheDocket(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "run-a~d1", "unknown-tool", "run-a", task.Step{})

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
// A firing asks a new question after an answer (trigger.Engine.askingID); a poser that posts over
// its own settled id is still asking, and a write merges, so `status: pending` would land on top
// of a verdict, a `by` and an `answeredAt` that stayed. What a person read on the docket was a
// question to decide, already stamped with who decided it and what they said.
func TestReposingOverASettledQuestionClearsTheAnswer(t *testing.T) {
	store := entitytest.Store(t)
	pose(t, store, "guard~1", "approval", "", task.Step{})
	AnswerQuestion(store, "guard~1", task.Resolution{
		Proceed: true, By: "human:scott", Steer: []string{"ship it"}})

	pose(t, store, "guard~1", "approval", "", task.Step{}) // posed again under the id it had

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
	pose(t, store, "guard~2", "approval", "", task.Step{})
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

// poseTo publishes one question the gate named answerers for.
func poseTo(t *testing.T, store entity.Store, id string, answerers ...string) {
	t.Helper()
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: id, Origin: "run-a", Gate: "write-guard",
			Subject: `demo.pr."1"`, Answerers: answerers},
		Tool: "write",
	}, "run-a")
}

// docket is a store that keeps records of who may answer, which is where waitingOn is read from:
// a store with no attribution has no such records, and nothing to say.
func docket(t *testing.T) entity.Store {
	t.Helper()
	return entity.As(entitytest.Store(t), entity.SystemDocket)
}

func waitsOn(t *testing.T, store entity.Store, id string) []string {
	t.Helper()
	e, ok, err := store.Get(QuestionRef(id))
	if err != nil || !ok {
		t.Fatalf("reading question %s: ok %v, %v", id, ok, err)
	}
	return entity.Strs(e.Payload, WaitingOnField)
}

func same(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("waits on %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("waits on %v, want %v", got, want)
		}
	}
}

// Who a question waits on is on the question, because nobody outside the system can work it out:
// a gate that named answerers holds it out to them, one that named nobody to whatever the docket
// holds out, and a group stands for the people who act as themselves.
func TestAQuestionSaysWhoItWaitsOn(t *testing.T) {
	store := docket(t)
	entitytest.Set(t, store, entity.GroupRef("payments"),
		entity.Obj{entity.MembersField: []any{"user:ana@example.com", "group:leads"}}.Payload())
	entitytest.Set(t, store, entity.GroupRef("leads"),
		entity.Obj{entity.MembersField: []any{"user:bo@example.com"}}.Payload())

	poseTo(t, store, "run-a~d1", "user:scott@example.com", "group:payments")
	same(t, waitsOn(t, store, "run-a~d1"),
		[]string{"user:scott@example.com", "user:ana@example.com", "user:bo@example.com"})

	// Named nobody, about something: whoever may write what it is about, as it is written
	// (9ce3bva0, D3). There is no list of them to write down, and a reader can see that this is
	// not a person.
	poseTo(t, store, "run-a~d2")
	same(t, waitsOn(t, store, "run-a~d2"), []string{`audience:demo.pr."1"`})

	// Named nobody, about nothing: whoever the docket holds questions out to.
	if err := entity.SetChildGrants(store, entity.VerseRef(AskSystem),
		entity.Levels{entity.ReadWrite: {"user:*"}, entity.Read: {entity.Everyone}}); err != nil {
		t.Fatal(err)
	}
	PostQuestion(store, task.Pending{Question: task.GateQuestion{ID: "run-a~d3", Origin: "run-a",
		Gate: "write-guard"}, Tool: "write"}, "run-a")
	same(t, waitsOn(t, store, "run-a~d3"), []string{"user:*"})
}

// It is present tense: a settled question waits on nobody, and what was asked of whom stays on
// `answerers`, who decided on `by`.
func TestASettledQuestionWaitsOnNobody(t *testing.T) {
	store := docket(t)
	poseTo(t, store, "run-a~d1", "user:scott@example.com")
	AnswerQuestion(store, "run-a~d1", task.Choose("proceed", "user:scott@example.com"))

	if got := waitsOn(t, store, "run-a~d1"); len(got) != 0 {
		t.Errorf("a settled question waits on %v", got)
	}
	e, _, _ := store.Get(QuestionRef("run-a~d1"))
	if got := GranteesTo(e.Payload); len(got) != 1 {
		t.Errorf("to.grantees %v, want the gate's own kept", got)
	}
}

// The words a gate wrote ride the question, so a presenter that may not read the subject can
// still say what is being decided.
func TestAQuestionCarriesTheWordsItWasAskedIn(t *testing.T) {
	store := docket(t)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: "run-a~d1", Origin: "run-a", Gate: "write-guard",
			Subject: `demo.pr."1"`, Pose: "Deploy *the migration* to staging?"},
		Tool: "write",
	}, "run-a")

	e, ok, err := store.Get(QuestionRef("run-a~d1"))
	if err != nil || !ok {
		t.Fatalf("reading the question: ok %v, %v", ok, err)
	}
	if got := entity.Str(e.Payload, "pose"); got != "Deploy *the migration* to staging?" {
		t.Errorf("pose %q, want the words the gate wrote", got)
	}
}

// A question carries the one line its subject says about itself, so a reader who may not read the
// subject can still say what is being decided.
func TestAQuestionCarriesWhatItsSubjectSaysOfItself(t *testing.T) {
	store := docket(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"),
		entity.Obj{"says": "bump go-tony to v0.0.234\nand fix the two call sites"}.Payload())
	poseTo(t, store, "run-a~d1", "user:scott@example.com")

	e, _, _ := store.Get(QuestionRef("run-a~d1"))
	if got := entity.Str(e.Payload, "about"); got != "bump go-tony to v0.0.234" {
		t.Errorf("about %q, want the subject's first line", got)
	}
}

// A subject that says nothing about itself leaves the question bare rather than refusing it: it
// is a convention an entity opts into, not a schema.
func TestASubjectThatSaysNothingLeavesTheQuestionBare(t *testing.T) {
	store := docket(t)
	entitytest.Set(t, store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	poseTo(t, store, "run-a~d1", "user:scott@example.com")

	e, _, _ := store.Get(QuestionRef("run-a~d1"))
	if got := entity.Str(e.Payload, "about"); got != "" {
		t.Errorf("about %q, want nothing", got)
	}
}

// AN ANSWERED QUESTION STAYS ANSWERED whatever reaches it after: a frame reporting the same asking
// again does not make it pending, and a settling that read it pending a moment before does not
// make it orphaned. Each write's precondition says "not answered and not orphaned"; with only the
// second half reaching the store, both went through (git-issue 874azhtjh12krn1xqsn0).
func TestAnAnsweredQuestionStaysAnswered(t *testing.T) {
	const ann = "user:ann@example.com"
	ev := task.Event{Kind: task.EventPending, ID: "t1~d1", Gate: "ship"}
	answered := func() entity.Store {
		store := docket(t)
		if !PostFrameQuestion(store, ev, "t1", "", []string{ann}, nil, nil, nil) {
			t.Fatal("not posed")
		}
		AnswerQuestion(store, ev.ID, task.Choose(task.Proceed, ann))
		return store
	}
	status := func(store entity.Store) string {
		e, _, _ := store.Get(QuestionRef(ev.ID))
		return entity.Str(e.Payload, "status")
	}

	store := answered()
	PostFrameQuestion(store, ev, "t1", "", []string{ann}, nil, nil, nil) // late: the same asking, reported again
	if s := status(store); s != QuestionAnswered {
		t.Errorf("a late frame left the answered question %s", s)
	}
	store = answered()
	settlePending(store, QuestionRef(ev.ID), entity.Obj{"status": QuestionOrphaned, "why": "the task ended"})
	if s := status(store); s != QuestionAnswered {
		t.Errorf("a settling after the answer left the question %s", s)
	}
}

// ONLY THE BALLOTS FOR THE ASK AS IT STANDS COUNT: one naming an earlier edit does not.
func TestTallyCountsOnlyTheCurrentEdit(t *testing.T) {
	p := entity.Obj{
		"edit": 2,
		"ballots": entity.Obj{
			"user:a": entity.Obj{"choice": "proceed", "edit": 1},
			"user:b": entity.Obj{"choice": "proceed", "edit": 2},
		},
	}.Payload()
	if got := Tally(p)["proceed"]; got != 1 {
		t.Fatalf("tally %d, want the one ballot for edit 2", got)
	}
	unedited := entity.Obj{"ballots": entity.Obj{"user:a": entity.Obj{"choice": "proceed"}}}.Payload()
	if got := Tally(unedited)["proceed"]; got != 1 {
		t.Fatalf("a question nobody edits counts %d, want its ballot", got)
	}
}

// AN OPTION IS RECORDED WITH THE NEED THAT COUNTS. A task's `ask` names its options and no
// threshold; one ballot settles each (task.Need), and the record says one, not zero.
func TestAnOptionWithNoNeedIsRecordedAsOneBallot(t *testing.T) {
	store := entitytest.Store(t)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: "run-a~1", Origin: "run-a", Gate: "ask",
			Options: []task.Option{{ID: "staging"}, {ID: "prod", Need: 2}}},
		Tool: "ask",
	}, "run-a")
	e, ok, _ := store.Get(QuestionRef("run-a~1"))
	if !ok {
		t.Fatal("the question was not posed")
	}
	opts, _ := entity.ToObj(e.Payload)["options"].([]any)
	var got []any
	for _, o := range opts {
		got = append(got, o.(map[string]any)["need"])
	}
	if len(got) != 2 || got[0] != int64(1) || got[1] != int64(2) {
		t.Fatalf("needs recorded as %v, want [1 2]", got)
	}
}

// A QUESTION A FRAME POSES SAYS WHOM IT WAITS ON, as one the registry poses does: a remote
// runner's questions are on `verse question` and in Slack as a local one's are.
func TestAFramePosedQuestionSaysWhomItWaitsOn(t *testing.T) {
	store := docket(t)
	const ann = "user:ann@example.com"
	ev := task.Event{Kind: task.EventPending, ID: "t1~d1", Gate: "ship"}
	if !PostFrameQuestion(store, ev, "t1", "", []string{ann}, nil, nil, nil) {
		t.Fatal("not posed")
	}
	same(t, waitsOn(t, store, ev.ID), []string{ann})
}

// THE RECORD SAYS WHAT THE ACTION IS. `tool` says who proposed it and `effects` what verb it
// derives to; `action` is the one line an answerer reads — and it is absent, not empty, for an
// action that said nothing about itself, by the same rule as every other field here.
func TestAQuestionCarriesTheAction(t *testing.T) {
	store := entitytest.Store(t)
	PostQuestion(store, task.Pending{
		Question: task.GateQuestion{ID: "run-a~d1", Origin: "run-a", Gate: "shell"},
		Tool:     "run_bash",
		Action:   "go test ./...",
	}, "run-a")
	e, ok, _ := store.Get(QuestionRef("run-a~d1"))
	if !ok {
		t.Fatal("not posted")
	}
	if got := entity.ToObj(e.Payload)["action"]; got != "go test ./..." {
		t.Errorf("action is %v, want the command", got)
	}

	// From a remote runner's frame, the same.
	PostFrameQuestion(store, task.Event{Kind: task.EventPending, ID: "run-b~d1", Gate: "write", Tool: "apply_patch",
		Action: "update docs/x.md", Text: "verb write"}, "run-b", "", nil, nil, nil, nil)
	e, ok, _ = store.Get(QuestionRef("run-b~d1"))
	if !ok {
		t.Fatal("the frame's question was not posted")
	}
	if got := entity.ToObj(e.Payload)["action"]; got != "update docs/x.md" {
		t.Errorf("action from a frame is %v, want the patch's paths", got)
	}

	pose(t, store, "run-c~d1", "x", "run-c", task.Step{})
	e, _, _ = store.Get(QuestionRef("run-c~d1"))
	if _, present := entity.ToObj(e.Payload)["action"]; present {
		t.Error("an action that said nothing has an action field")
	}
}
