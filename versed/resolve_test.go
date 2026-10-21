package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// The resolve body is one type for both of verse's routes (api.Resolve), and these are the
// tests that keep it that way. They exist because the routes once carried a vocabulary each
// and nothing noticed: no test posted either body, so the drift was invisible to everything
// except a person reading both handlers at once.
//
// Posting the SHARED type is the point. A field renamed in api.Resolve now fails to
// compile here and at every call site, instead of decoding to a zero value and
// answering "unknown or already-answered decision" at runtime.

// answerPath is the route an answer to the ask id is posted to, and sayPath the one a word on it is:
// the path names the ask, so a body need not.
func answerPath(id string) string { return "/asks/" + url.PathEscape(id) + "/answer" }

func sayPath(id string) string { return "/asks/" + url.PathEscape(id) + "/say" }

// postTony sends v as tony to path and returns the response, as the tester: the principal the
// CLI would send from its config's `user:`, so what a test does is somebody's, the way what a
// person does is. A test about the unattributed request sends none (postAs).
func postTony(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(v, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// AS WHOEVER THE BODY SAYS IT IS BY, where it says: an act is recorded as whoever the request
	// is made as, so the CLI sends one name in both places and a test does too.
	as := tester
	switch body := v.(type) {
	case api.Resolve:
		as = orTester(body.By)
	case api.GateSay:
		as = orTester(body.By)
	case api.Say:
		as = orTester(body.By)
	case api.SetAutonomy:
		as = orTester(body.By)
	}
	return postAs(t, as, url, "application/tony", string(b))
}

func orTester(by string) string {
	if by == "" {
		return tester
	}
	return by
}

// tester is who a test acts as.
const tester = "tester@example.com"

// An engine gate posted to the registry is answered through POST /asks/{id}/answer carrying
// api.Resolve — the path names the gate, choice carries the answer, and say rides along
// as steering.
func TestAnAskAnswerAcceptsAPIResolve(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Raise a gate the way the engine does, and block on it as the engine would.
	const id = "verse.trigger.review.review~1"
	answered := make(chan task.Resolution, 1)
	go func() {
		res, _ := srv.Reg.Resolve(context.Background(),
			task.GateQuestion{ID: id, Origin: "review", Gate: "review"},
			task.ToolUse{Name: "verse.trigger.review"})
		answered <- res
	}()
	waitPending(t, srv, id)

	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: "user:scott@example.com", Say: []string{"ship it"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("gate resolve: status = %s, want 204", resp.Status)
	}

	select {
	case res := <-answered:
		if !res.Proceed {
			t.Error("gate resolved to deny; the choice did not arrive")
		}
		if len(res.Steer) != 1 || res.Steer[0] != "ship it" {
			t.Errorf("steer = %v, want [ship it] — the say field did not arrive", res.Steer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate never resolved: the posted body did not decode into api.Resolve")
	}
}

// waitPending waits until a question is on the docket AND PUBLISHED — both, because the two
// happen in that order and a test that stops at the first reads the store in the window between
// them.
//
// task.Registry.Resolve puts the question in `pending` under its lock and calls OnPost after
// releasing it, so `Reg.Pending()` reports a question whose record is not written yet. Waiting
// on the registry alone was a race that a slower write turns from rare into reliable — which is
// how it was found, one write path later.
func waitPending(t *testing.T, srv *Server, id string) {
	t.Helper()
	for range 100 {
		for _, p := range srv.Reg.Pending() {
			if p.Question.ID == id {
				if _, ok, _ := srv.Store.Get(mirror.QuestionRef(id)); ok {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gate %s never became pending", id)
}
