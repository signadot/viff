package versed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// A question with a threshold above one is settled by a TALLY: a ballot is recorded and the
// question stays open until the chosen option has as many as it needs. These tests pin what
// the record and the waiter see on the way there.

// raise poses a question with options, blocking on it the way a firing does, and returns the
// channel its resolution comes back on.
func raise(t *testing.T, srv *Server, id string, opts []task.Option) chan task.Resolution {
	t.Helper()
	answered := make(chan task.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review", Options: opts},
			task.ToolUse{Name: "verse.trigger.review"})
		answered <- res
	}()
	waitPending(t, srv, id)
	return answered
}

// ballotsOf reads the record's ballots as who → choice.
func ballotsOf(q map[string]any) map[string]string {
	out := map[string]string{}
	b, _ := q["ballots"].(map[string]any)
	for who, v := range b {
		if m, ok := v.(map[string]any); ok {
			out[who], _ = m["choice"].(string)
		}
	}
	return out
}

func stillWaiting(t *testing.T, answered chan task.Resolution) {
	t.Helper()
	select {
	case res := <-answered:
		t.Fatalf("the waiter was released by %+v; the threshold was not reached", res)
	case <-time.After(100 * time.Millisecond):
	}
}

// Two ballots settle a question whose proceed needs two. The first is recorded and releases
// nothing; the second releases the waiter — and the second ballot and the settle are ONE
// commit, so no reader can see the threshold reached without the record saying so.
func TestAThresholdOfTwoSettlesOnTheSecondBallot(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~1"
	answered := raise(t, srv, id, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 1}})

	// The records are one segment below the place verse keeps its questions.
	questions := []string{entity.VerseRoot, mirror.AskSystem}
	commits, unsub, err := srv.Store.WatchSubtree(len(questions)+1, questions...)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unsub()

	first := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:alice@example.com"})
	body, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first ballot = %s, want 202 recorded: %s", first.Status, body)
	}
	var rec api.Recorded
	if err := codec.Unmarshal(body, &rec, codec.Tony); err != nil || rec != (api.Recorded{Count: 1, Need: 2, Choice: "proceed"}) {
		t.Errorf("the answerer is not told where the tally stands: %q (%v)", body, err)
	}
	stillWaiting(t, answered)
	q := questionDoc(t, srv, id)
	if q["status"] != mirror.QuestionPending || ballotsOf(q)["user:alice@example.com"] != "proceed" {
		t.Fatalf("after one ballot the record should be pending with alice's ballot on it: %+v", q)
	}

	second := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:bob@example.com"})
	second.Body.Close()
	if second.StatusCode != http.StatusNoContent {
		t.Fatalf("second ballot = %s, want 204 settled", second.Status)
	}
	select {
	case res := <-answered:
		if !res.Proceed || res.Chosen() != "proceed" || res.By != "user:bob@example.com" {
			t.Fatalf("the waiter got %+v, want proceed by the ballot that reached the threshold", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the question never resolved")
	}
	q = settledQuestion(t, srv, id)
	if q["verdict"] != "proceed" || q["by"] != "user:bob@example.com" {
		t.Fatalf("the record does not say what settled it: %+v", q)
	}
	if b := ballotsOf(q); b["user:alice@example.com"] != "proceed" || b["user:bob@example.com"] != "proceed" {
		t.Fatalf("both ballots should stand on the settled record: %+v", b)
	}

	// The commit that settled is the commit that carried bob's ballot: no earlier commit
	// had it. Read the stream up to the settle.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-commits:
			for _, ev := range c.Changes {
				if ev.Ref != mirror.QuestionRef(id) {
					continue
				}
				bob := entity.Str(entity.Get(entity.Get(ev.To, "ballots"), "user:bob@example.com"), "choice")
				settled := entity.Str(ev.To, "status") == mirror.QuestionAnswered
				switch {
				case bob != "" && !settled:
					t.Fatalf("rev %d carries bob's ballot and is not the settle: the two are separate writes", ev.Rev)
				case settled && bob == "":
					t.Fatalf("rev %d settles without the ballot that reached the threshold", ev.Rev)
				case settled:
					return
				}
			}
		case <-deadline:
			t.Fatal("never saw the settling commit")
		}
	}
}

// One answerer, one ballot. Casting again replaces it — a changed mind is the later ballot,
// not a second one — so nobody reaches a threshold alone.
func TestASecondBallotByTheSameAnswererReplacesTheFirst(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~2"
	answered := raise(t, srv, id, []task.Option{{ID: "proceed", Need: 2}, {ID: "deny", Need: 2}})

	cast := func(by, choice string) string {
		t.Helper()
		resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: choice, By: by})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s voting %s = %s, want 202: %s", by, choice, resp.Status, body)
		}
		var rec api.Recorded
		if err := codec.Unmarshal(body, &rec, codec.Tony); err != nil {
			t.Fatalf("%s voting %s: the tally did not read: %v", by, choice, err)
		}
		return rec.String()
	}
	cast("user:alice@example.com", "proceed")
	if got := cast("user:alice@example.com", "proceed"); !strings.Contains(got, "1 of 2") {
		t.Errorf("alice twice is one ballot, got %q", got)
	}
	if got := cast("user:alice@example.com", "deny"); !strings.Contains(got, "1 of 2 for deny") {
		t.Errorf("alice changing her mind moves her one ballot, got %q", got)
	}
	if got := cast("user:bob@example.com", "proceed"); !strings.Contains(got, "1 of 2 for proceed") {
		t.Errorf("alice no longer counts for proceed, got %q", got)
	}
	stillWaiting(t, answered)
	q := questionDoc(t, srv, id)
	if b := ballotsOf(q); len(b) != 2 || b["user:alice@example.com"] != "deny" || b["user:bob@example.com"] != "proceed" {
		t.Fatalf("ballots = %+v, want alice's latest and bob's", b)
	}
	if q["status"] != mirror.QuestionPending {
		t.Fatalf("nothing reached two: %+v", q)
	}
}

// A ballot for an option the question does not carry is refused, with the options listed,
// and neither the record nor the waiter is touched.
func TestABallotForAnUnknownOptionIsRefused(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const id = "verse.trigger.review.review~3"
	answered := answerable(t, srv, id)

	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "maybe", By: "user:walter@example.com"})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("answer = %s, want 400", resp.Status)
	}
	if !strings.Contains(string(body), "proceed, deny") {
		t.Errorf("the refusal should list the options: %q", body)
	}
	stillWaiting(t, answered)
	q := questionDoc(t, srv, id)
	if q["status"] != mirror.QuestionPending || len(ballotsOf(q)) != 0 {
		t.Fatalf("a refused ballot left a trace: %+v", q)
	}

	ok := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:walter@example.com"})
	ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("a real answer afterwards = %s, want 204", ok.Status)
	}
	<-answered
}

// A QUESTION ASKED AGAIN IS NOT ANSWERED AS THE ONE BEFORE IT. A poser that asks again under the
// id it had — posed here straight to the registry — finds the record and the question standing
// from the earlier asking until the new one lands: settled, with its options at one apiece. An
// answer sent in that window waits for this asking, and is never tallied against the last one's
// options, which would release on one ballot a waiter that needs three.
func TestAQuestionAskedAgainIsNotAnsweredWithTheOldOptions(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	three := []task.Option{{ID: "proceed", Need: 3}, {ID: "deny", Need: 1}}
	for i := 0; i < 6; i++ { // the window is narrow, so it is tried more than once
		id := fmt.Sprintf("verse.trigger.review.review~again%d", i)
		first := raise(t, srv, id, nil) // the default two, at one apiece
		resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: tester})
		resp.Body.Close()
		<-first

		// Asked again, needing three, and answered at the first instant the registry has it.
		again := make(chan task.Resolution, 1)
		go func() {
			res, _ := srv.Reg.Resolve(context.Background(),
				task.GateQuestion{ID: id, Origin: "review", Gate: "review", Options: three},
				task.ToolUse{Name: "verse.trigger.review"})
			again <- res
		}()
		for srv.Reg.WaiterCount(id) == 0 {
			time.Sleep(time.Millisecond)
		}
		resp = postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: tester})
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("round %d: one ballot of three answered %s, want 202 recorded", i, resp.Status)
		}
		select {
		case <-again:
			t.Fatalf("round %d: one ballot released a waiter that needs three", i)
		case <-time.After(50 * time.Millisecond):
		}
		srv.Reg.Answer(id, task.Resolution{Choice: "deny"}) // let the waiter go
	}
}

// AN ANSWER IS BY WHOEVER THE REQUEST IS MADE AS. The check admits the header's principal, so
// the record keeps that one: a body's `by` held to no name would let one permitted answerer cast
// every ballot of a threshold under three names, the last of them `system:docket` — a name the
// door refuses in the header — and have the question say the system decided it.
func TestABallotIsCastByWhoeverTheRequestIsMadeAs(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const id = "verse.trigger.review.review~by"
	answered := raise(t, srv, id, []task.Option{{ID: "proceed", Need: 3}, {ID: "deny", Need: 1}})
	as := func(by string) *http.Response { // made as the tester, whatever the body says
		t.Helper()
		b, err := codec.Marshal(api.Resolve{Choice: "proceed", By: by}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		return postAs(t, tester, ts.URL+answerPath(id), "application/tony", string(b))
	}
	for _, by := range []string{"user:a@example.com", "system:docket", "via:verse.resolvers.bot"} {
		resp := as(by)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a ballot by %s from a request made as %s: %s, want 400", by, tester, resp.Status)
		}
	}
	stillWaiting(t, answered)
	if got := ballotsOf(questionDoc(t, srv, id)); len(got) != 0 {
		t.Fatalf("ballots were recorded: %v", got)
	}
	// Its own name, spelled either way, or none at all: one ballot, the tester's.
	for _, by := range []string{"", tester, "user:" + tester} {
		resp := as(by)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("a ballot by %q as %s: %s, want 202", by, tester, resp.Status)
		}
	}
	if got := ballotsOf(questionDoc(t, srv, id)); len(got) != 1 || got["user:"+tester] != "proceed" {
		t.Fatalf("ballots = %v, want the tester's one", got)
	}
	srv.Reg.Answer(id, task.Resolution{Choice: "deny"})
}
