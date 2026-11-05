package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/internal/resolver"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
)

func installMatch(t *testing.T, url, name, match string) {
	t.Helper()
	resp := installResolver(t, url, `{name: `+name+`, match: `+match+`, method: {driver: service}}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install %s: %s: %s", name, resp.Status, bodyOf(t, resp))
	}
}

func grantStatus(t *testing.T, as, url, name, body string) (int, string) {
	t.Helper()
	resp := answerFor(t, as, url, "grant", name, body)
	return resp.StatusCode, bodyOf(t, resp)
}

// TWO RESOLVERS NEVER ANSWER FOR ONE PARTY ON ONE ASK (9ce3bva0, Q2). A grant that would let a
// second resolver answer for somebody on an ask the first also matches is refused; a match that
// cannot meet the first's, another party's grant, and a place somebody else owns are not.
func TestAGrantThatWouldOverlapIsRefused(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	installMatch(t, ts.URL, "prs", `{about: demo.pr.*}`)
	installMatch(t, ts.URL, "team-a", `{about: demo.pr.*, labels: {team: a}}`)
	installMatch(t, ts.URL, "issues", `{about: demo.issue.*}`)
	installMatch(t, ts.URL, "team-b", `{labels: {team: b}}`)
	installMatch(t, ts.URL, "team-b2", `{labels: {team: b}}`)
	const alice, bob = "user:alice@example.com", "user:bob@example.com"

	if code, msg := grantStatus(t, alice, ts.URL, "prs", `{}`); code != http.StatusNoContent {
		t.Fatalf("alice's first grant: %d %s", code, msg)
	}
	for _, name := range []string{"team-a", "team-b"} {
		if code, msg := grantStatus(t, alice, ts.URL, name, `{}`); code != http.StatusConflict || !strings.Contains(msg, "would both answer for") {
			t.Errorf("alice granted %s beside prs: %d %s", name, code, msg)
		}
	}
	if code, msg := grantStatus(t, alice, ts.URL, "issues", `{}`); code != http.StatusNoContent {
		t.Fatalf("a match that cannot meet prs': %d %s", code, msg)
	}
	if code, msg := grantStatus(t, bob, ts.URL, "team-a", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant, beside alice's: %d %s", code, msg)
	}

	// Places: alice's demo, with her pr beneath it granted prs, where no record stands between;
	// and bob's pr beside it, which is his to grant at.
	owns(t, srv, alice, nil, entity.RefAt("demo"))
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "pr", "1"), entity.Obj{"stage": "open"}.Payload())
	owns(t, srv, bob, nil, entity.RefAt("demo", "pr", "2"))
	if code, msg := grantStatus(t, alice, ts.URL, "prs", `{for: "demo.pr.\"1\""}`); code != http.StatusNoContent {
		t.Fatalf("alice's grant at her pr: %d %s", code, msg)
	}
	if code, msg := grantStatus(t, alice, ts.URL, "team-a", `{for: demo}`); code != http.StatusConflict {
		t.Errorf("alice granted team-a above her grant to prs: %d %s", code, msg)
	}
	if code, msg := grantStatus(t, bob, ts.URL, "team-a", `{for: "demo.pr.\"2\""}`); code != http.StatusNoContent {
		t.Errorf("bob's grant at his own pr, beneath alice's demo: %d %s", code, msg)
	}

	// A record of somebody else's fences their grants off from one above: bob's team-b at his own
	// place beneath alice's answers for bob, and alice's team-b2 above it meets nothing.
	owns(t, srv, alice, nil, entity.RefAt("fence"))
	owns(t, srv, bob, nil, entity.RefAt("fence", "bob"))
	if code, msg := grantStatus(t, bob, ts.URL, "team-b", `{for: fence.bob}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant at his place: %d %s", code, msg)
	}
	if code, msg := grantStatus(t, alice, ts.URL, "team-b2", `{for: fence}`); code != http.StatusNoContent {
		t.Errorf("alice's grant above bob's place met his: %d %s", code, msg)
	}

	// Installed again with a match that would meet issues where alice granted both, refused.
	resp := installResolver(t, ts.URL, `{name: issues, match: {about: demo.pr.*}, method: {driver: service}}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, "would both answer for") {
		t.Errorf("issues reinstalled over prs: %s %s", resp.Status, msg)
	}

	// And a resolver standing from before, with no match, answers for nobody by grant.
	owns(t, srv, alice, nil, resolver.Ref("ladder"))
	entitytest.Set(t, srv.Store, resolver.Ref("ladder"), entity.Obj{
		"name": "ladder", "byRef": true, "method": entity.Obj{"driver": "service"},
	}.Payload())
	if code, msg := grantStatus(t, alice, ts.URL, "ladder", `{}`); code != http.StatusConflict || !strings.Contains(msg, "has no `match`") {
		t.Errorf("a ladder resolver granted: %d %s", code, msg)
	}
}
