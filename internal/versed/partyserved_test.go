package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/mirror"
)

func answerForcing(t *testing.T, as, url, id string, force bool) (int, string) {
	t.Helper()
	b, err := codec.Marshal(api.Resolve{Choice: "proceed", By: as, Force: force}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, as, url+answerPath(id), "application/tony", string(b))
	return resp.StatusCode, bodyOf(t, resp)
}

func servedFor(t *testing.T, as, url, id string) (int, string) {
	t.Helper()
	resp := getAs(t, as, url+"/asks/"+id+"/served")
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, bodyOf(t, resp)
	}
	var out api.AskServed
	if err := client.Decode(resp, &out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out.Resolver
}

// A PARTY'S RESOLVER ANSWERS WHAT IT SERVES FOR THEM (9ce3bva0, stage 5): answering it yourself is
// refused, naming the resolver, until you say force; the docket leaves it out unless asked for
// what is served, and says which resolver serves it. A person, and a place's owner, alike.
func TestAPartyAnsweringWhatTheirResolverServesIsAsked(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	for _, doc := range []string{
		`{name: helper, match: {}, method: {driver: service}}`,
		`{name: places, match: {about: demo.pr.*}, method: {driver: service}}`,
	} {
		if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony", doc); resp.StatusCode != http.StatusCreated {
			t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
		}
	}
	if code, msg := grantStatus(t, bob, ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	id := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: q, to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(id))

	docket := func(as, query string) string {
		resp := getAs(t, as, ts.URL+"/asks?waiting=me"+query)
		return bodyOf(t, resp)
	}
	if got := docket(bob, ""); strings.Contains(got, id) {
		t.Fatalf("bob's docket lists what his resolver serves: %s", got)
	}
	if got := docket(bob, "&served=true"); !strings.Contains(got, id) || !strings.Contains(got, "servedBy") {
		t.Fatalf("bob's docket with what is served does not list it, marked: %s", got)
	}
	if code, by := servedFor(t, bob, ts.URL, id); code != http.StatusOK || by != "verse.resolvers.helper" {
		t.Fatalf("served for bob = %d %q, want helper", code, by)
	}
	if code, by := servedFor(t, "user:carol@example.com", ts.URL, id); code != http.StatusOK || by != "" {
		t.Fatalf("served for carol, who asked = %d %q, want nothing", code, by)
	}

	if code, msg := answerForcing(t, bob, ts.URL, id, false); code != http.StatusConflict ||
		!strings.Contains(msg, "verse.resolvers.helper answers") || !strings.Contains(msg, "--force") {
		t.Fatalf("bob answered what his resolver serves, unasked: %d %s", code, msg)
	}
	if code, msg := answerForcing(t, bob, ts.URL, id, true); code != http.StatusNoContent {
		t.Fatalf("bob's forced answer: %d %s", code, msg)
	}
	if rec := settledQuestion(t, srv, id); rec["by"] != bob || rec["via"] != nil {
		t.Fatalf("settled by %v via %v, want bob himself", rec["by"], rec["via"])
	}

	// A place: alice's pr, a gate about it naming nobody, and the resolver she granted there.
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	gate := waitRuleQuestion(t, srv, "land", pr.String())
	posed(t, srv, mirror.QuestionRef(gate))
	if code, msg := grantStatus(t, alice, ts.URL, "places", `{for: "demo.pr.\"1\""}`); code != http.StatusNoContent {
		t.Fatalf("alice's grant at her pr: %d %s", code, msg)
	}
	if code, msg := answerForcing(t, alice, ts.URL, gate, false); code != http.StatusConflict {
		t.Fatalf("alice answered what the resolver she granted at her pr serves, unasked: %d %s", code, msg)
	}
	// dan may write the pr and owns nothing of it: nothing serves the ask for him.
	if code, msg := answerForcing(t, "user:dan@example.com", ts.URL, gate, false); code != http.StatusNoContent {
		t.Fatalf("dan's answer: %d %s", code, msg)
	}
}
