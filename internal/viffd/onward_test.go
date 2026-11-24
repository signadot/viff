package viffd

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
)

// postAsResolver makes a request as the resolver name, presented by as, and says what came back.
func postAsResolver(t *testing.T, as, name, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, as)
	req.Header.Set(api.ResolverHeader, name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// servedPending is a rule's gate carol's charter puts to bob, on a clock that settles on deny,
// which bob's resolver serves.
func servedPending(t *testing.T) (*Server, string, string) {
	t.Helper()
	srv, ts, _ := keeping(t, askKeepDefault)
	if resp := postAs(t, "user:alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	if resp := installAs(t, "carol@example.com", ts.URL, `{
	name: land, condition: {subject: {path: "demo.rel.*", value: {stage: open}}},
	gates: [{name: ok, mode: ask, answerers: [bob@example.com], deadline: {within: "48h", default: deny}}],
	action: {patch: {stage: done}},
}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	rel := entity.RefAt("demo", "rel", "1")
	entitytest.Set(t, srv.Store, rel, entity.Obj{"stage": "open"}.Payload())
	id := waitRuleQuestion(t, srv, "land", rel.String())
	posed(t, srv, mirror.AskRef(id))
	waitStamped(t, srv, id)
	return srv, ts.URL, id
}

// waitStamped waits for a question's clock to be stamped, which follows its posing.
func waitStamped(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok {
			if _, _, at := mirror.DeadlineOf(e.Payload); at != "" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s's clock was never stamped", id)
}

// AN ONWARD ASK IS RAISED BY THE RESOLVER SERVING AN ASK, for a party it serves it for (9ce3bva0,
// stage 4, Q5). It offers the served ask's options, each settled by one ballot; its clock fits
// inside the served ask's, which it takes when it names none, with what silence settles on there;
// and it takes no `then`.
func TestAnOnwardAskIsRaisedInsideTheAskItServes(t *testing.T) {
	srv, url, served := servedPending(t)
	const alice = "user:alice@example.com"
	raise := func(body string) *http.Response {
		return postAsResolver(t, alice, "helper", url+"/asks", body)
	}

	id := askID(t, raise(`{name: up, question: "should bob land it?", to: [mgr@example.com], serves: `+served+`}`))
	posed(t, srv, mirror.AskRef(id))
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	s, _, _ := srv.Store.Get(mirror.AskRef(served))
	link := entity.Get(e.Payload, "serves")
	if entity.Str(link, "id") != served || entity.Str(link, "for") != "user:bob@example.com" {
		t.Fatalf("the onward ask's link is %v, want %s for bob", link, served)
	}
	_, _, servedAt := mirror.DeadlineOf(s.Payload)
	at, _ := time.Parse(time.RFC3339Nano, servedAt)
	if got := entity.Str(e.Payload, "due"); got != at.UTC().Format(time.RFC3339) {
		t.Fatalf("the onward ask is due %s, want the served ask's clock, %s", got, servedAt)
	}
	if _, silence, _ := mirror.DeadlineOf(e.Payload); silence != "deny" {
		t.Fatalf("silence on the onward ask settles on %q, want what it settles on the served ask", silence)
	}
	if opts := mirror.OptionsOf(e.Payload); len(opts) != 2 || opts[0].Need != 1 || opts[1].Need != 1 {
		t.Fatalf("the onward ask offers %v, want the served ask's, one ballot each", opts)
	}

	late := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for body, want := range map[string]int{
		`{name: up2, question: q, serves: ` + served + `, due: "` + late + `"}`:                                                                                               http.StatusBadRequest,
		`{name: up3, question: q, serves: ` + served + `, on: [{match: {verdict: proceed}, then: {spawn: {prompt: "go", environment: {systems: {network: {mode: open}}}}}}]}`: http.StatusBadRequest,
		`{name: up4, question: q, serves: ` + served + `, for: dan@example.com}`:                                                                                              http.StatusForbidden,
		`{name: up5, question: q, serves: nothing-here}`:                                                                                                                      http.StatusNotFound,
		`{name: up6, question: q, for: bob@example.com}`:                                                                                                                      http.StatusBadRequest,
		// Its silence is the served ask's, so it says none of its own.
		`{name: up8, question: q, serves: ` + served + `, due: "` + soon + `", onSilence: proceed}`: http.StatusBadRequest,
	} {
		if resp := raise(body); resp.StatusCode != want {
			t.Errorf("%s: %s %s, want %d", body, resp.Status, bodyOf(t, resp), want)
		}
	}
	if resp := ask(t, alice, url, `{name: up7, question: q, serves: `+served+`}`); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(bodyOf(t, resp), "raised by the resolver serving the ask") {
		t.Errorf("alice, as herself, raised an onward ask: %s", resp.Status)
	}

	// Settled, it has nothing left to serve. bob answers it himself, over his resolver.
	if code, msg := answerForcing(t, "user:bob@example.com", url, served, true); code != http.StatusNoContent {
		t.Fatalf("bob's answer: %d %s", code, msg)
	}
	settledQuestion(t, srv, served)
	if resp := raise(`{name: up8, question: q, serves: ` + served + `}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("an onward ask to a settled ask: %s %s", resp.Status, bodyOf(t, resp))
	}
}
