package versed

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/task"
)

// A service dials in, reads the run, and answers — which is the whole of what a resolver does.
//
// It reads CONTEXT before the ask: by the time it sees the gate it has already seen what led
// to it, which is the reason a resolver is attached to a run rather than handed a question.
func TestAResolverReadsARunAndAnswersIt(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const binding = "verse:resolvers:security:security-review"

	askingRun(t, srv, ts, "watched", binding)
	qid := waitQuestion(t, srv, "watched")
	inst := resolver.InstanceRef("security-review", "watched").ID()
	base := ts.URL + "/resolvers/instance/" + inst

	// Dial the stream from the beginning and read until the ask, keeping what came before it.
	resp, err := http.Get(base + "/events?since=0")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer resp.Body.Close()
	var before []string
	var asked string
	sc := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(5 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var f task.Frame
		if codec.Unmarshal([]byte(data), &f, codec.Tony) != nil {
			continue
		}
		if f.Kind == task.EvPending {
			asked = f.ID
			break
		}
		before = append(before, f.Kind)
	}
	if asked != qid {
		t.Fatalf("read to %q, want the open question %q", asked, qid)
	}
	if len(before) == 0 {
		t.Fatal("the ask was the first thing on the stream; a resolver that sees no context " +
			"is one that may as well have been handed the question")
	}

	// Answer it, and the run goes on.
	v := postTony(t, base+"/verdicts", api.Verdict{ID: qid, Verdict: "proceed", Why: "staging path"})
	v.Body.Close()
	if v.StatusCode != http.StatusNoContent {
		t.Fatalf("verdict = %s, want 204", v.Status)
	}
	var settled map[string]any
	for range 400 {
		settled = question(t, srv, qid)
		if settled["status"] == mirror.QuestionAnswered {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if settled["status"] != mirror.QuestionAnswered || settled["verdict"] != "proceed" {
		t.Fatalf("the question did not settle: %+v", settled)
	}
	// Attribution is verse's: a service says what it decided, never who it is.
	if settled["by"] != binding {
		t.Fatalf("by = %v, want the resolver's own ref %q", settled["by"], binding)
	}
}

// A stream it may READ is not a docket it may ANSWER.
//
// The check is structural rather than a policy about who is asking: the instance names its
// binding and its origin, so this is a comparison against two fields the question carries.
func TestAResolverCannotAnswerAQuestionItDoesNotOwn(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	askingRun(t, srv, ts, "watched", "verse:resolvers:security:security-review")
	askingRun(t, srv, ts, "elsewhere", "verse:resolvers:cost:budget")
	mine := waitQuestion(t, srv, "watched")
	theirs := waitQuestion(t, srv, "elsewhere")

	base := ts.URL + "/resolvers/instance/" + resolver.InstanceRef("security-review", "watched").ID()
	resp := postTony(t, base+"/verdicts", api.Verdict{ID: theirs, Verdict: "proceed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("answering another resolver's question = %s, want 403", resp.Status)
	}
	if q := question(t, srv, theirs); q["status"] != mirror.QuestionPending {
		t.Fatalf("the refused verdict settled it anyway: %+v", q)
	}
	// …and its own is untouched by the attempt.
	if q := question(t, srv, mine); q["status"] != mirror.QuestionPending {
		t.Fatalf("its own question moved: %+v", q)
	}
}

// A position advances whether or not anything was decided, which is what tells "has not
// decided" from "has not caught up". Notes ride with it, for whoever picks the position up.
func TestAResolverReportsItsPosition(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	askingRun(t, srv, ts, "watched", "verse:resolvers:security:security-review")
	waitQuestion(t, srv, "watched")
	ref := resolver.InstanceRef("security-review", "watched")

	resp := postTony(t, ts.URL+"/resolvers/instance/"+ref.ID()+"/position",
		api.Position{Position: 12, Notes: "denied the prod path once; staging is fine"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("position = %s, want 204", resp.Status)
	}
	e, _, _ := srv.Store.Get(ref)
	got := entity.ToObj(e.Payload)
	if got["position"] != int64(12) {
		t.Fatalf("position = %v (%T), want 12", got["position"], got["position"])
	}
	if got["notes"] == nil {
		t.Fatalf("the handover was dropped: %+v", got)
	}
	// And nothing about it reached the task's stream: what was WATCHED is not what was
	// decided, and only one of the two is the run's business.
	if q := question(t, srv, waitQuestion(t, srv, "watched")); q["status"] != mirror.QuestionPending {
		t.Fatalf("reporting a position settled a question: %+v", q)
	}
}

// An instance nobody minted is not a stream anybody may read.
func TestAnUnknownInstanceIsNotAStream(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/resolvers/instance/nobody@nothing/events?since=0")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unminted instance = %s, want 404", resp.Status)
	}
}
