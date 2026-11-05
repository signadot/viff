package mirror_test

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
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

// WHO MAY ANSWER BELONGS TO THE ASKING. A question posed again under an id that settled is a new
// asking, and a record written once per id would go on admitting whoever the first asking named
// and refusing whoever this one does.
func TestAQuestionAskedAgainIsHeldOutToWhomThisAskingNames(t *testing.T) {
	store := entity.As(entitytest.OpenStore(t), entity.SystemDocket)
	view := entity.As(store, entity.SystemDefault)
	oscar, alice := "user:oscar@example.com", "user:alice@example.com"
	q := task.GateQuestion{ID: "ship~1", Gate: "ship", Answerers: []string{oscar},
		Deadline: &task.Deadline{Within: "72h", Default: "deny"}}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed")
	}
	mirror.AnswerQuestion(store, q.ID, task.Resolution{Choice: "proceed", Proceed: true, By: oscar})

	q.Answerers = []string{alice}
	if !mirror.PostQuestion(store, task.Pending{Question: q}, "") {
		t.Fatal("not posed again")
	}
	ref := mirror.QuestionRef(q.ID)
	if ok, _ := entity.Holds(view, oscar, entity.ReadWrite, ref); ok {
		t.Error("oscar, whom the first asking named, still answers the second")
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
