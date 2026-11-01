package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A question posed with a deadline settles on its default when the clock passes, as
// `deadline`, and the waiter is released the way an answer releases it.

func raiseWithDeadline(t *testing.T, srv *Server, id, within, dflt string) chan task.Resolution {
	t.Helper()
	answered := make(chan task.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review",
				Deadline: &task.Deadline{Within: within, Default: dflt}},
			task.ToolUse{Name: "verse.trigger.review"})
		answered <- res
	}()
	waitPending(t, srv, id)
	return answered
}

// armedQuestion waits for the question's deadline to be stamped, and answers the record. The
// pose and the stamp are two writes — the instant is stamped once, by the Arm hook, after the
// question is posed — so a read the moment the question is pending can land between them, and
// "posed and not armed" is that window rather than a deadline that never armed.
func armedQuestion(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	var q map[string]any
	for range 200 {
		q = questionDoc(t, srv, id)
		if _, _, at := mirror.DeadlineOf(entity.Obj(q).Payload()); at != "" {
			return q
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the deadline was posed and never armed: %+v", q)
	return nil
}

func byDeadline(t *testing.T, answered chan task.Resolution, want string) task.Resolution {
	t.Helper()
	select {
	case res := <-answered:
		if res.By != task.ByDeadline || res.Chosen() != want || res.Proceed {
			t.Fatalf("the waiter got %+v, want %s by the deadline", res, want)
		}
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("the deadline never released the waiter")
	}
	return task.Resolution{}
}

// backdate moves a question's stamped deadline into the past, standing in for wall time
// that a test cannot afford to wait through.
func backdate(t *testing.T, store entity.Store, id string) {
	t.Helper()
	past := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, _, err := store.Write(entity.Write{
		At:    mirror.QuestionRef(id).At("deadline"),
		Value: entity.Obj{"at": past}.Payload(),
	}); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestADeadlineSettlesOnItsDefault(t *testing.T) {
	srv := testServer(t)
	const id = "verse.trigger.review.review~1"
	answered := raiseWithDeadline(t, srv, id, "50ms", "deny")

	q := armedQuestion(t, srv, id)
	byDeadline(t, answered, "deny")
	q = settledQuestion(t, srv, id)
	if q["verdict"] != "deny" || q["by"] != task.ByDeadline || q["why"] == nil {
		t.Fatalf("the record does not say the clock decided: %+v", q)
	}
	if _, has := q["ballots"]; has {
		t.Fatalf("a deadline is not a ballot: %+v", q)
	}
}

// A deadline given no default settles on nothing: the waiter is refused, and the record says
// no option was chosen rather than naming one nobody picked.
func TestADeadlineWithNoDefaultSettlesOnNothing(t *testing.T) {
	srv := testServer(t)
	const id = "verse.trigger.review.review~2"
	answered := raiseWithDeadline(t, srv, id, "50ms", "")
	byDeadline(t, answered, task.NoOption)
	if q := settledQuestion(t, srv, id); q["verdict"] != task.NoOption {
		t.Fatalf("verdict = %v, want %s", q["verdict"], task.NoOption)
	}
}

// The stamped instant is honoured across a restart. The question is re-posed with the same
// duration, as a charter's replay re-poses it, and the clock does not start over: an instant
// that passed while the verse was down fires on the way back up.
func TestARestartHonoursTheStampedDeadline(t *testing.T) {
	addr := entitytest.Addr(t)
	const id = "verse.trigger.review.review~3"

	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	raiseWithDeadline(t, first, id, "10m", "deny")
	_, _, stamped := mirror.DeadlineOf(entity.Obj(armedQuestion(t, first, id)).Payload())
	backdate(t, first.Store, id) // the verse is down for longer than the deadline
	stop()

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	// The reconcile orphaned it (its waiter died with the first verse); the replay re-poses it.
	answered := raiseWithDeadline(t, second, id, "10m", "deny")
	byDeadline(t, answered, "deny")
	q := settledQuestion(t, second, id)
	if _, _, at := mirror.DeadlineOf(entity.Obj(q).Payload()); at == stamped {
		t.Fatalf("the re-pose kept the first stamp rather than the backdated one it found standing: %s", at)
	} else if at == "" {
		t.Fatalf("the re-pose lost the stamp: %+v", q)
	}
	if q["askedAgain"] != true {
		t.Errorf("a re-posed question says so: %+v", q)
	}
}

// A daemon whose timer died still settles an overdue question the moment anybody reads the
// docket, and the listing shows it settled rather than pending.
func TestAnOverdueDeadlineSettlesWhenTheDocketIsRead(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const id = "verse.trigger.review.review~4"
	answered := raiseWithDeadline(t, srv, id, "10m", "deny")
	backdate(t, srv.Store, id)

	resp, err := http.Get(ts.URL + "/asks?all=true")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	resp.Body.Close()
	byDeadline(t, answered, "deny")
	for _, g := range docket(t, srv) {
		if g["id"] == id {
			t.Fatalf("still listed as pending after its deadline passed: %+v", g)
		}
	}
}
