package mirror_test

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// noRecords is a store that refuses every write to the perms mirror, as a store under load
// refuses a write now and then.
type noRecords struct {
	entity.Store
	refused int
}

func (n *noRecords) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref.Under(entity.VerseRoot, entity.PermsSystem) {
		n.refused++
		return false, 0, errors.New("storage_error: the store refused the write")
	}
	return n.Store.Write(w)
}

// A QUESTION WHOSE RECORD DID NOT LAND IS NOT POSED. One standing with no record is answered by
// whoever the docket's row admits, which is every person — so when the one write that says who
// may answer was refused and the error thrown away, a gate held out to named people became one
// anybody could settle, for good, with nothing in any log. It waits instead: nobody is asked
// until it is posed again.
func TestAQuestionWhoseRecordDidNotLandIsNotPosed(t *testing.T) {
	inner := &noRecords{Store: entitytest.OpenStore(t)}
	store := entity.As(inner, entity.SystemDocket)
	q := task.GateQuestion{
		ID: "naming.for-verse:verse.trigger.shortlist.approve~1", Gate: "approve",
		Origin: "shortlist", At: "naming.for-verse", Subject: "naming.for-verse.lumen",
		Answerers: []string{"user:judy@example.com", "user:sybil@example.com"},
	}
	mirror.PostQuestion(store, task.Pending{Question: q}, "")
	if inner.refused < 2 {
		t.Errorf("the record was tried %d time(s), want more than one: a store refuses a write now and then", inner.refused)
	}
	if _, ok, err := inner.Store.Get(mirror.QuestionRef(q.ID)); err != nil || ok {
		t.Fatalf("the question stands with no record of who may answer it (ok=%v err=%v)", ok, err)
	}
	// …and when the store answers, posing it again poses it.
	mirror.PostQuestion(entity.As(inner.Store, entity.SystemDocket), task.Pending{Question: q}, "")
	if _, ok, _ := inner.Store.Get(mirror.QuestionRef(q.ID)); !ok {
		t.Error("the question was not posed once its record could be written")
	}
	view := entity.As(inner.Store, entity.SystemDefault)
	if ok, _ := entity.Holds(view, "user:mallory@example.com", entity.ReadWrite, mirror.QuestionRef(q.ID)); ok {
		t.Error("a stranger may answer a question held out to two named people")
	}
	if ok, err := entity.Holds(view, "user:judy@example.com", entity.ReadWrite, mirror.QuestionRef(q.ID)); err != nil || !ok {
		t.Errorf("a named answerer may not answer: %v %v", ok, err)
	}
}

// A DEADLINE IS NOT WHAT POSES A QUESTION. A write beneath an address brings it into being, so a
// stamp on a question whose pose failed left a node with a deadline and nothing else: found by
// the answer route, with no record of who may answer it and the default options at one apiece.
func TestADeadlineDoesNotPoseAQuestion(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDocket)
	const id = "window-ask~d1"
	if _, err := mirror.StampDeadline(store, id, time.Now().Add(time.Hour)); err == nil {
		t.Error("a deadline was stamped on a question nobody posed")
	}
	if _, ok, err := store.Get(mirror.QuestionRef(id)); err != nil || ok {
		t.Fatalf("the stamp brought the question into being (ok=%v err=%v)", ok, err)
	}
	// …and on one that stands it is stamped once, a second arming finding the first.
	q := task.GateQuestion{ID: id, Gate: "approve", Deadline: &task.Deadline{Within: "72h", Default: "deny"}}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed")
	}
	first, err := mirror.StampDeadline(store, id, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again, err := mirror.StampDeadline(store, id, time.Now().Add(2*time.Hour)); err != nil || again != first {
		t.Errorf("a second arming stamped %q over %q (err %v)", again, first, err)
	}
}

// oneRecordWrite lets the first write to the perms mirror land and refuses the rest.
type oneRecordWrite struct {
	entity.Store
	landed bool
}

func (o *oneRecordWrite) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref.Under(entity.VerseRoot, entity.PermsSystem) {
		if o.landed {
			return false, 0, errors.New("storage_error: the store refused the write")
		}
		o.landed = true
	}
	return o.Store.Write(w)
}

// A QUESTION A RESOLVER OWNS IS THE RESOLVER'S FROM THE WRITE THAT MAKES ITS RECORD. Held out by
// the default first and narrowed to the resolver in a second write, it was every person's to settle for as
// long as that write took — and for good when it failed, which nothing said.
func TestAResolversQuestionIsNeverHeldOutToEverybody(t *testing.T) {
	inner := &oneRecordWrite{Store: entitytest.OpenStore(t)}
	store := entity.As(inner, entity.SystemDocket)
	q := task.GateQuestion{ID: "review~d1", Gate: "approve", Resolver: "verse.resolvers.reviewbot"}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed")
	}
	view := entity.As(inner.Store, entity.SystemDefault)
	if ok, _ := entity.Holds(view, "user:mallory@example.com", entity.ReadWrite, mirror.QuestionRef(q.ID)); ok {
		t.Error("a resolver's question whose second write failed is anybody's to answer")
	}
	rec, ok, err := inner.Store.Get(entity.PermsRef(mirror.QuestionRef(q.ID)))
	if err != nil || !ok {
		t.Fatalf("no record: %v %v", ok, err)
	}
	gs, err := entity.ParseGrants(mirror.QuestionRef(q.ID).Segs(), rec.Payload)
	if err != nil || len(gs) == 0 {
		t.Fatalf("grants = %v (err %v)", gs, err)
	}
	if got := gs[0].To[entity.ReadWrite]; len(got) != 1 || got[0] != "via:"+q.Resolver {
		t.Errorf("held out to %v, want the resolver", got)
	}
}

// WHO MAY ANSWER BELONGS TO THE ASKING. A gate fired again about the same subject poses the same
// id, and a record written once per id would go on admitting whoever the first asking named and
// refusing whoever this one does.
func TestAQuestionAskedAgainIsHeldOutToWhomThisAskingNames(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDocket)
	view := entity.As(store, entity.SystemDefault)
	davido, alice := "user:davido@example.com", "user:alice@example.com"
	q := task.GateQuestion{ID: "ship~1", Gate: "ship", Answerers: []string{davido},
		Deadline: &task.Deadline{Within: "72h", Default: "deny"}}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed")
	}
	mirror.AnswerQuestion(store, q.ID, task.Resolution{Choice: "proceed", Proceed: true, By: davido})

	q.Answerers = []string{alice}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed again")
	}
	ref := mirror.QuestionRef(q.ID)
	if ok, _ := entity.Holds(view, davido, entity.ReadWrite, ref); ok {
		t.Error("davido, whom the first asking named, still answers the second")
	}
	if ok, err := entity.Holds(view, alice, entity.ReadWrite, ref); err != nil || !ok {
		t.Errorf("alice, whom this asking names, may not answer: %v %v", ok, err)
	}
	// …and the asking keeps its clock's terms: what goes with the earlier round is the instant.
	e, _, _ := store.Get(ref)
	if within, dflt, at := mirror.DeadlineOf(e.Payload); within != "72h" || dflt != "deny" || at != "" {
		t.Errorf("the re-asked question's deadline is within=%q default=%q at=%q", within, dflt, at)
	}
}

// HANDED UP, A QUESTION GOES BACK TO WHOM ITS GATE NAMED. A held-out question's record carries its
// resolver alone, so releasing it to the docket's default would make a gate held out to davido
// every person's the moment its resolver escalates, times out or is removed.
func TestReleasingGivesAQuestionBackToItsAnswerers(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDocket)
	view := entity.As(store, entity.SystemDefault)
	davido, mallory := "user:davido@example.com", "user:mallory@example.com"
	q := task.GateQuestion{ID: "review~1", Gate: "approve", Answerers: []string{davido},
		Resolver: "verse.resolvers.reviewbot"}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed")
	}
	ref := mirror.QuestionRef(q.ID)
	if ok, _ := entity.Holds(view, davido, entity.ReadWrite, ref); ok {
		t.Fatal("the fixture is wrong: a question a resolver owns is the resolver's, and davido holds it")
	}
	if err := mirror.HoldOut(store, q.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := entity.Holds(view, mallory, entity.ReadWrite, ref); ok {
		t.Error("handed up to the docket, a gate held out to davido is anybody's")
	}
	if ok, err := entity.Holds(view, davido, entity.ReadWrite, ref); err != nil || !ok {
		t.Errorf("davido may not answer the gate that names him: %v %v", ok, err)
	}
	// A gate that named nobody goes back to the docket's default, as it did.
	if err := entity.SetChildGrants(view, entity.VerseRef(mirror.QuestionSystem, mirror.QuestionKind), entity.Levels{
		entity.Read: {entity.Everyone}, entity.ReadWrite: {"user:*", "agent:*"},
	}); err != nil {
		t.Fatal(err)
	}
	open := task.GateQuestion{ID: "open~1", Gate: "approve", Resolver: q.Resolver}
	mirror.PostQuestion(store, task.Pending{Question: open}, "")
	if err := mirror.HoldOut(store, open.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := entity.Holds(view, mallory, entity.ReadWrite, mirror.QuestionRef(open.ID)); err != nil || !ok {
		t.Errorf("a gate naming nobody did not go back to the default: %v %v", ok, err)
	}
}
