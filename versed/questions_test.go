package versed

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/mirror"
)

// THE QUESTIONS PUT TO YOU (`GET /asks?waiting=me`, `verse question`): an ask whose waitingOn
// names you, and not one you raised. Its asker reads their own ask on the docket and does not find
// it among their questions; whom it names does; a vote waits on every person but its asker.
func TestTheQuestionsPutToYouAreWhatWaitsOnYou(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	land := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", answerers: [oscar@example.com]}`))
	lunch := askID(t, ask(t, davido, ts.URL, `{name: lunch, question: "Lunch at noon?"}`))
	list := func(who, q string) string {
		t.Helper()
		resp := getAs(t, who, ts.URL+"/asks"+q)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s /asks%s: %s: %s", who, q, resp.Status, b)
		}
		return string(b)
	}
	posed(t, srv, mirror.AskRef(land))
	posed(t, srv, mirror.AskRef(lunch))
	for _, c := range []struct {
		who, id string
		want    bool
	}{
		{daniel, land, false}, // his own ask
		{davido, land, true},  // named
		{daniel, lunch, true}, // a vote, waiting on every person
		{davido, lunch, false},
	} {
		if got := strings.Contains(list(c.who, "?waiting=me"), c.id); got != c.want {
			t.Errorf("%s's questions list %s: %v, want %v", c.who, c.id, got, c.want)
		}
	}
	// The docket is every pending ask they may read, their own included.
	if !strings.Contains(list(daniel, "?all=true"), land) {
		t.Errorf("daniel's docket does not list his own ask")
	}
}

// A QUESTION ABOUT A PLACE, naming nobody, waits on whoever may write the place: dan, who may,
// finds it among his questions; carol, who may only read the place, finds it on the docket and
// not among hers.
func TestAQuestionAboutAPlaceWaitsOnWhoMayWriteIt(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	list := func(who, q string) string {
		resp := getAs(t, who, ts.URL+"/asks"+q)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	// Posed, and then held out to whoever may write the pr: what waitingOn copies lands a moment
	// after the question does.
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(list("dan@example.com", "?waiting=me"), qid); {
		if time.Now().After(deadline) {
			t.Fatalf("dan, who may write the pr, never finds the question among his")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(list("carol@example.com", "?waiting=me"), qid) {
		t.Errorf("carol, who may only read the pr, finds the question among hers")
	}
	if !strings.Contains(list("carol@example.com", "?all=true"), qid) {
		t.Errorf("carol's docket does not list the question about a pr she reads")
	}
}

// THE ASKS TREE READS AS EACH SIDE ASKS: one ask by its id, to whoever may read it; the docket
// (`all=true`) with what a resolver serves for you in it; and a listing it does not have refused.
func TestTheAsksTreeReadsAsEachSideAsks(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const bob, carol = "user:bob@example.com", "user:carol@example.com"
	if resp := postAs(t, "user:alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, bob, ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	id := askID(t, ask(t, carol, ts.URL, `{name: land, question: "Merge it?", answerers: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(id))

	if resp := getAs(t, carol, ts.URL+"/asks/"+id); resp.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, resp), "Merge it?") {
		t.Errorf("carol reading her ask by its id: %s", resp.Status)
	}
	if resp := getAs(t, "user:dan@example.com", ts.URL+"/asks/"+id); resp.StatusCode != http.StatusForbidden {
		t.Errorf("dan, who is neither asker nor asked, read the ask: %s", resp.Status)
	}
	if got := bodyOf(t, getAs(t, bob, ts.URL+"/asks?all=true")); !strings.Contains(got, id) || !strings.Contains(got, "servedBy") {
		t.Errorf("bob's docket leaves out what his resolver serves, or does not mark it: %s", got)
	}
	for _, q := range []string{"?waiting=carol", "?waiting=me&all=true"} {
		if resp := getAs(t, bob, ts.URL+"/asks"+q); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /asks%s: %s, want 400", q, resp.Status)
		}
	}
}
