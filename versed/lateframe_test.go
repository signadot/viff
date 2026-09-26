package versed

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A LATE FRAME DOES NOT UNDO A HANDOVER. A local run posts its question twice, from the registry
// and again from the task's pending frame, and the frame's post names the owner it looked up
// when it was reported. When that post lands after the owner was removed — its question handed
// on — it named the removed resolver again and held the question out to it: a question
// stranded on a name nothing answers, though the removal did everything right. Under load the
// frame lands late now and then, which is how TestRemovingTheLastResolverSendsAQuestionToTheDocket
// flaked; here the late frame is delivered by hand, after the handover, every time.
func TestALateFrameDoesNotNameAResolverTheQuestionWasHandedOnFrom(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	const subject = `change.pr."1"`
	spawnFor(t, ts, "run-1", subject, "")
	qid := waitQuestion(t, srv, "run-1")
	waitOwned(t, srv, qid)

	removeResolverAt(t, ts.URL, "alpha")
	if got := question(t, srv, qid); got["resolver"] != nil || got["reownedFrom"] != "verse.resolvers.alpha" {
		t.Fatalf("the fixture is wrong: the handover did not land: %+v", got)
	}

	// The frame the mirror would have posted, arriving now, naming the owner it saw before.
	late := task.Frame{Kind: task.EvPending, ID: qid, Gate: "d1", Mode: string(task.Ask)}
	mirror.PostFrameQuestion(entity.As(srv.Store, entity.SystemDocket), late, "run-1", subject,
		"verse.resolvers.alpha", nil)

	got := question(t, srv, qid)
	if got["resolver"] != nil {
		t.Fatalf("a late frame named %v again, after the question was handed on from it", got["resolver"])
	}
	if got["status"] != mirror.QuestionPending {
		t.Fatalf("the late frame changed more than the owner: %+v", got)
	}
	// Nor the grant: the removed resolver does not hold the question out to itself again.
	rec, ok, err := srv.Store.Get(entity.PermsRef(mirror.QuestionRef(qid)))
	if err != nil || !ok {
		t.Fatalf("the question's record: ok=%v err=%v", ok, err)
	}
	if s := fmt.Sprintf("%v", entity.ToObj(rec.Payload)); strings.Contains(s, "resolvers.alpha") {
		t.Fatalf("the late frame held the question out to the removed resolver again: %s", s)
	}
}
