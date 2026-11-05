package versed

import (
	"net/http"
	"slices"
	"testing"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/client"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

func servedBy(t *testing.T, as, name, url string) map[string][]string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/resolvers/"+name+"/asks", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(api.PrincipalHeader, as)
	req.Header.Set(api.ResolverHeader, name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("served asks: %s: %s", resp.Status, bodyOf(t, resp))
	}
	var out []api.ServedAsk
	if err := client.Decode(resp, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, a := range out {
		got[a.ID] = a.For
	}
	return got
}

// A RESOLVER SERVES AN ASK FOR A PARTY where its match admits the ask and the party granted it:
// for each person the ask names who did, or for the place an ask naming nobody is about.
func TestAResolverServesTheAsksItMatchesForWhoeverGrantedIt(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"
	for _, doc := range []string{
		`{name: prs, match: {about: demo.pr.*, labels: {team: a}}, method: {driver: service}}`,
		`{name: places, match: {about: demo.pr.*}, method: {driver: service}}`,
	} {
		if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony", doc); resp.StatusCode != http.StatusCreated {
			t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
		}
	}
	if code, msg := grantStatus(t, bob, ts.URL, "prs", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "pr", "2"), entity.Obj{"stage": "open"}.Payload())
	const about = `about: "demo.pr.\"2\""`
	served := askID(t, ask(t, "carol@example.com", ts.URL, `{name: a1, question: q, to: [bob@example.com], `+about+`, labels: {team: a}}`))
	otherTeam := askID(t, ask(t, "carol@example.com", ts.URL, `{name: a2, question: q, to: [bob@example.com], `+about+`, labels: {team: b}}`))
	toDan := askID(t, ask(t, "carol@example.com", ts.URL, `{name: a3, question: q, to: [dan@example.com], `+about+`, labels: {team: a}}`))
	toBoth := askID(t, ask(t, "carol@example.com", ts.URL, `{name: a4, question: q, to: [bob@example.com, dan@example.com], `+about+`, labels: {team: a}}`))

	// And a rule's gate about alice's pr, naming nobody, served for the place.
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	gate := waitRuleQuestion(t, srv, "land", pr.String())
	if code, msg := grantStatus(t, alice, ts.URL, "places", `{for: "demo.pr.\"1\""}`); code != http.StatusNoContent {
		t.Fatalf("alice's grant at her pr: %d %s", code, msg)
	}

	got := servedBy(t, alice, "prs", ts.URL)
	if !slices.Equal(got[served], []string{bob}) || !slices.Equal(got[toBoth], []string{bob}) {
		t.Fatalf("prs serves %v, want %s and %s for bob", got, served, toBoth)
	}
	for _, id := range []string{otherTeam, toDan, gate} {
		if _, ok := got[id]; ok {
			t.Errorf("prs serves %s", id)
		}
	}
	if got := servedBy(t, alice, "places", ts.URL); !slices.Equal(got[gate], []string{pr.String()}) || len(got) != 1 {
		t.Fatalf("places serves %v, want the gate about alice's pr, for the place", got)
	}

	// Read as the resolver, and by nobody else.
	if resp := getAs(t, alice, ts.URL+"/resolvers/prs/asks"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice, as herself, read what prs serves: %s", resp.Status)
	}
}
