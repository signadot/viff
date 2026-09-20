package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/task"
)

// planeOf reads a task's read plane from the beginning until its gate is decided, and
// returns the frames.
func planeOf(t *testing.T, ts *httptest.Server, taskID string, answer func(questionID string)) []task.Frame {
	t.Helper()
	// Scanned on its own goroutine so the deadline can fire while the stream is SILENT,
	// which is exactly the failure this waits for: a decision that never reaches the plane
	// is a stream with nothing more to say, and a reader that only checks the clock between
	// frames waits on it forever.
	_, lines, stop := sseData(t, http.DefaultClient, ts.URL+"/tasks/"+taskID+"/events?since=0")
	defer stop()

	var got []task.Frame
	answered := false
	deadline := time.After(5 * time.Second)
	for {
		select {
		case data, open := <-lines:
			if !open {
				t.Fatalf("the stream ended with the gate undecided; read %d frames", len(got))
			}
			var f task.Frame
			if codec.Unmarshal([]byte(data), &f, codec.Tony) != nil {
				continue
			}
			got = append(got, f)
			if f.Kind == task.EvPending && !answered {
				answered = true
				answer(f.ID)
			}
			if f.Kind == task.EvDecision {
				return got
			}
		case <-deadline:
			t.Fatalf("the gate was never decided on the run's own stream; read %d frames (%v). "+
				"An answer that settles the record without reaching the plane is the invisible "+
				"answer this test exists to catch", len(got), shape(got))
			return nil
		}
	}
}

// shape is what two runs of the same work should have in common: the kinds, in order.
func shape(fs []task.Frame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func decisionOf(t *testing.T, fs []task.Frame) task.Frame {
	t.Helper()
	for _, f := range fs {
		if f.Kind == task.EvDecision {
			return f
		}
	}
	t.Fatal("no decision on the plane")
	return task.Frame{}
}

// A supervised run reads exactly like a human-supervised one, except in WHO decided.
//
// That is the design's claim and the reason supervision is never a side channel: control
// application is emitted on the one ordered stream, so an observer reads the whole exchange in
// order — the ask, the decision with its `by`, the action that followed. There is no
// supervised run that reads as unsupervised, and no way to answer invisibly.
//
// Comparing two real runs is the only way to test that. Asserting the resolver's own path in
// isolation would pass just as well if it emitted something extra, or something less.
func TestASupervisedRunReadsLikeAHumanSupervisedOne(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const binding = "verse.resolvers.security-review"

	askingRun(t, srv, ts, "byhand", "")
	byHand := planeOf(t, ts, "byhand", func(qid string) {
		r := postTony(t, ts.URL+"/gate/resolve", api.Resolve{ID: qid, Choice: "proceed", By: "user:scott@example.com"})
		r.Body.Close()
	})

	askingRun(t, srv, ts, "watched", binding)
	inst := resolver.InstanceRef("security-review", "watched").Name()
	byResolver := planeOf(t, ts, "watched", func(qid string) {
		r := postTony(t, ts.URL+"/resolvers/instance/"+inst+"/verdicts",
			api.Verdict{ID: qid, Verdict: "proceed"})
		r.Body.Close()
	})

	if a, b := shape(byHand), shape(byResolver); !equal(a, b) {
		t.Fatalf("the two runs read differently:\n  by hand:     %v\n  by resolver: %v", a, b)
	}

	hand, res := decisionOf(t, byHand), decisionOf(t, byResolver)
	if hand.By != "user:scott@example.com" {
		t.Fatalf("the human's decision is attributed to %q", hand.By)
	}
	if res.By != "via:"+binding {
		t.Fatalf("the resolver's decision is attributed to %q, want via %q", res.By, binding)
	}
	// Everything else about the two decisions is the same act.
	if hand.Proceed != res.Proceed || hand.Tool != res.Tool {
		t.Fatalf("the decisions differ beyond who made them:\n  %+v\n  %+v", hand, res)
	}

	// And both settle their question the same way — the docket cannot tell them apart
	// either, except by reading who answered.
	for _, c := range []struct{ task, by string }{{"byhand", "user:scott@example.com"}, {"watched", "via:" + binding}} {
		q := settledQuestion(t, srv, c.task+"~d1")
		if q["status"] != mirror.QuestionAnswered || q["verdict"] != "proceed" || q["by"] != c.by {
			t.Fatalf("%s settled as %+v", c.task, q)
		}
	}
	if got := docket(t, srv); len(got) != 0 {
		t.Fatalf("the docket still holds %v", got)
	}
}

// A position that advances with no verdict emits NOTHING on the task's stream, and shows on
// the resolver's record.
//
// That is the whole of "what was watched" being a separate fact from "what was decided": a
// hundred frames may produce no verdict, which is the ordinary case, and the run has no
// business being told that somebody read it.
func TestWatchingIsNotDeciding(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	askingRun(t, srv, ts, "watched", "verse.resolvers.security-review")
	qid := waitQuestion(t, srv, "watched")
	ref := resolver.InstanceRef("security-review", "watched")

	before := framesSoFar(t, ts.URL+"/tasks/watched/events?since=0")
	for _, p := range []int64{3, 7, 11} {
		r := postTony(t, ts.URL+"/resolvers/instance/"+ref.Name()+"/position",
			api.Position{Position: p, Notes: "still reading"})
		r.Body.Close()
	}
	after := framesSoFar(t, ts.URL+"/tasks/watched/events?since=0")

	if len(after) != len(before) {
		t.Fatalf("watching put %d frame(s) on the run's stream: %v",
			len(after)-len(before), shape(after[len(before):]))
	}
	e, _, _ := srv.Store.Get(ref)
	if got := entity.ToObj(e.Payload); got["position"] != int64(11) {
		t.Fatalf("the instance is at %v, want 11 — the last position it reported", got["position"])
	}
	// The question is untouched by all that reading: it is still open, still nobody's answer.
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("reading settled the question: %+v", q)
	}
	// AND THE INSTANCE IS ITS BINDING'S TO WRITE, and nobody else's: the system mints it beneath
	// a row that holds `owner` out to everyone, so it carries the system's record under its
	// binding — whose owner co-owns it, which is who wrote the positions above.
	if got := owner(t, srv, ref); !entity.IsSystem(got) {
		t.Errorf("the instance's record is %q's, want the system's, under its binding", got)
	}
	if ok, _ := entity.Holds(srv.Store, "user:mallory@example.com", entity.ReadWrite, ref); ok {
		t.Error("a stranger writes a binding's instance: its position, and whose it is")
	}
}

// framesSoFar reads a stream that is deliberately NOT finished — a run parked on a gate
// nobody has answered — and returns what it has said so far.
func framesSoFar(t *testing.T, url string) []task.Frame {
	t.Helper()
	_, lines, stop := sseData(t, http.DefaultClient, url)
	defer stop()
	var out []task.Frame
	for {
		select {
		case d, ok := <-lines:
			if !ok {
				return out
			}
			var f task.Frame
			if codec.Unmarshal([]byte(d), &f, codec.Tony) == nil {
				out = append(out, f)
			}
		case <-time.After(300 * time.Millisecond):
			return out // parked: it has said everything it is going to for now
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
