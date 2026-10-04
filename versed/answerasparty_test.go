package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// answerAsResolver answers id as the resolver name, presented by as, for the party named (or
// none), and says what came back.
func answerAsResolver(t *testing.T, as, name, url, id, choice, forParty string) (int, string) {
	t.Helper()
	b, err := codec.Marshal(api.Resolve{Choice: choice, For: forParty}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url+answerPath(id), strings.NewReader(string(b)))
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
	return resp.StatusCode, bodyOf(t, resp)
}

// A RESOLVER'S ANSWER IS ITS PARTY'S (9ce3bva0, stage 4): the ballot is theirs, given through
// the resolver they granted, and the record names both. It answers only for a party that granted
// it, one party to an answer, and a quorum counts each party once.
func TestAResolversAnswerIsThePartys(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice, bob, erin = "user:alice@example.com", "user:bob@example.com", "user:erin@example.com"
	for _, doc := range []string{
		`{name: helper, match: {}, method: {driver: service}}`,
		`{name: places, match: {about: demo.pr.*}, method: {driver: service}}`,
	} {
		if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony", doc); resp.StatusCode != http.StatusCreated {
			t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
		}
	}
	for _, who := range []string{bob, erin} {
		if code, msg := grantStatus(t, who, ts.URL, "helper", `{}`); code != http.StatusNoContent {
			t.Fatalf("%s's grant: %d %s", who, code, msg)
		}
	}

	// For bob, who granted it: his answer, through helper.
	one := askID(t, ask(t, "carol@example.com", ts.URL, `{name: one, question: q, answerers: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(one))
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, one, "proceed", "dan@example.com"); code != http.StatusForbidden {
		t.Fatalf("helper answered for dan, who is not asked: %d %s", code, msg)
	}
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, one, "proceed", ""); code != http.StatusNoContent {
		t.Fatalf("helper's answer for bob: %d %s", code, msg)
	}
	rec := settledQuestion(t, srv, one)
	e, _, _ := srv.Store.Get(mirror.AskRef(one))
	if rec["by"] != bob || rec["via"] != "via:verse.resolvers.helper" ||
		entity.Str(entity.Get(entity.Get(e.Payload, "ballots"), bob), "via") != "via:verse.resolvers.helper" {
		t.Fatalf("the answer is %v by %v via %v, ballots %v; want bob's, through helper", rec["verdict"], rec["by"], rec["via"], rec["ballots"])
	}

	// For dan, who granted nothing: nobody's.
	toDan := askID(t, ask(t, "carol@example.com", ts.URL, `{name: two, question: q, answerers: [dan@example.com]}`))
	posed(t, srv, mirror.AskRef(toDan))
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, toDan, "proceed", ""); code != http.StatusForbidden {
		t.Fatalf("helper answered an ask to dan: %d %s", code, msg)
	}
	// For frank, who granted a resolver whose match does not admit the ask: it reads the ask, and
	// answers for nobody on it.
	const frank = "user:frank@example.com"
	if code, msg := grantStatus(t, frank, ts.URL, "places", `{}`); code != http.StatusNoContent {
		t.Fatalf("frank's grant: %d %s", code, msg)
	}
	toFrank := askID(t, ask(t, "carol@example.com", ts.URL, `{name: five, question: q, answerers: [frank@example.com]}`))
	posed(t, srv, mirror.AskRef(toFrank))
	if code, msg := answerAsResolver(t, alice, "places", ts.URL, toFrank, "proceed", ""); code != http.StatusForbidden || !strings.Contains(msg, "answers for nobody") {
		t.Fatalf("places answered an ask about nothing: %d %s", code, msg)
	}

	// Two parties, two ballots, each named.
	both := askID(t, ask(t, "carol@example.com", ts.URL, `{name: three, question: q, answerers: [bob@example.com, erin@example.com]}`))
	posed(t, srv, mirror.AskRef(both))
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: mirror.AskRef(both).At(), Value: entity.Obj{"options": []any{
			entity.Obj{"id": "proceed", "need": 2}, entity.Obj{"id": "deny", "need": 1},
		}}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, both, "proceed", ""); code != http.StatusForbidden || !strings.Contains(msg, "name one in `for`") {
		t.Fatalf("helper answered for two parties at once: %d %s", code, msg)
	}
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, both, "proceed", "bob@example.com"); code != http.StatusAccepted {
		t.Fatalf("helper's ballot for bob: %d %s", code, msg)
	}
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, both, "proceed", "bob@example.com"); code != http.StatusAccepted {
		t.Fatalf("helper's ballot for bob, again: %d %s", code, msg)
	}
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, both, "proceed", erin); code != http.StatusNoContent {
		t.Fatalf("helper's ballot for erin: %d %s", code, msg)
	}
	if rec := settledQuestion(t, srv, both); rec["by"] != erin {
		t.Fatalf("settled by %v, want erin's ballot, the second party's", rec["by"])
	}

	// A place: an ask about alice's pr naming nobody, answered for the place, as its owner.
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	gate := waitRuleQuestion(t, srv, "land", pr.String())
	posed(t, srv, mirror.QuestionRef(gate))
	if code, msg := grantStatus(t, alice, ts.URL, "places", `{for: "demo.pr.\"1\""}`); code != http.StatusNoContent {
		t.Fatalf("alice's grant at her pr: %d %s", code, msg)
	}
	if code, msg := answerAsResolver(t, alice, "places", ts.URL, gate, "proceed", ""); code != http.StatusNoContent {
		t.Fatalf("places' answer for alice's pr: %d %s", code, msg)
	}
	if rec := settledQuestion(t, srv, gate); rec["by"] != alice || rec["via"] != "via:verse.resolvers.places" {
		t.Fatalf("the gate was answered by %v via %v, want the pr's owner through places", rec["by"], rec["via"])
	}

	// `for` is a resolver's word, and nobody else's.
	b, _ := codec.Marshal(api.Resolve{Choice: "proceed", For: "frank@example.com"}, codec.Tony)
	if resp := postAs(t, frank, ts.URL+answerPath(toFrank), "application/tony", string(b)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("frank answered for somebody as himself: %s", resp.Status)
	}

	// Revoked, it answers for bob no more.
	if resp := answerFor(t, bob, ts.URL, "revoke", "helper", `{}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob's revoke: %s", resp.Status)
	}
	again := askID(t, ask(t, "carol@example.com", ts.URL, `{name: four, question: q, answerers: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(again))
	if code, msg := answerAsResolver(t, alice, "helper", ts.URL, again, "proceed", ""); code != http.StatusForbidden {
		t.Fatalf("helper answered for bob after he revoked it: %d %s", code, msg)
	}
}
