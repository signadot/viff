package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/launch"
	"github.com/signadot/verse/pkg/mirror"
)

// A WITHDRAWN QUESTION LEAVES THE DOCKET WHEN IT IS WITHDRAWN, not when the task ends.
//
// An intervention withdraws the standing question — the task package's own machine says so and
// `TestAPauseWithdrawsAStandingQuestionAndAResumeDoesNot` proves it — and for a HOSTED task
// nothing carried that as far as the record. runnerd's per-task registry had `OnPost` wired to the
// publisher and no `OnDrop` beside it, so the question was announced and never retracted: it sat
// `pending` on `verse gate` next to a live one with nothing to tell them apart, and answering it
// was a 404 (git-issue zdaqh9vkh12ksxneg9n0).
//
// It became visible when a paused task started SURVIVING. Before that the run died and the
// terminal sweep (OrphanQuestions) tidied the leftovers, so nobody saw the stale question — and
// that sweep is why "it is orphaned eventually" was true and not the point.
func TestAnInterruptedHostedTaskWithdrawsItsQuestionAtOnce(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()

	spawn := postTony(t, ops.URL+"/tasks", api.Spawn{ID: "held-1", Spawn: launch.Spec{Script: "true", Subject: `demo.pr."1"`, Environment: stepEnv, Autonomy: stepAsks}})
	spawn.Body.Close()
	if spawn.StatusCode >= 300 {
		t.Fatalf("spawn = %s", spawn.Status)
	}
	qid := waitQuestion(t, srv, "held-1")

	// An intervention — a pause holds the run, and the machine withdraws whatever gate was
	// outstanding (effDropGate). The task stands paused, so no terminal sweep can settle the
	// question first: what leaves it off the docket is the withdrawal the runner reports.
	if code, msg := control(t, ops, "/tasks/held-1/pause", "", nil); code != http.StatusNoContent {
		t.Fatalf("pause = %d %s", code, msg)
	}

	// THE `why` IS WHAT TELLS THE TWO APART, and it is the discriminator the issue used. Both
	// the drop and the terminal sweep (OrphanQuestions) settle a question the same way; only
	// the drop can say what actually happened to it. A record reading "the task that raised it
	// ended without it being answered" is the sweep having tidied up afterwards, which is what
	// was happening before — true, late, and not news anybody can act on.
	deadline := time.After(3 * time.Second)
	for {
		q := entity.ToObj(mustGet(t, srv.Store, mirror.QuestionRef(qid)))
		if q["status"] == mirror.QuestionOrphaned {
			why, _ := q["why"].(string)
			if why == "" {
				t.Fatal("a withdrawn question must say why it left the docket")
			}
			if strings.Contains(why, "ended without it being answered") {
				t.Fatalf("why = %q — that is the terminal sweep, not the withdrawal", why)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the question is %v after the intervention that withdrew it — still offered, "+
				"and answering it is a 404", q["status"])
		case <-time.After(5 * time.Millisecond):
		}
	}
}
