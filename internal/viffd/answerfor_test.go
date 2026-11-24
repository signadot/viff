package viffd

import (
	"fmt"
	"net/http"
	"testing"

	"go.viff.dev/viff/internal/resolver"
	"go.viff.dev/viff/pkg/entity"
)

func answerFor(t *testing.T, as, url, verb, name, body string) *http.Response {
	t.Helper()
	return postAs(t, as, url+"/resolvers/"+name+"/"+verb, "application/tony", body)
}

// WHOM A RESOLVER ANSWERS FOR IS GRANTED (9ce3bva0, stage 4): by a person at their own
// `viff.for.<person>`, made for them by their first grant, and by a place's owner at the place.
// Nobody grants at somebody else's, and a grant is taken back the same way.
func TestAResolverIsGrantedWhomItAnswersFor(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	if resp := installResolver(t, ts.URL, `{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	via := entity.Via(resolver.Ref("helper"))
	const alice = "user:alice@example.com"
	holds := func(at entity.Ref) bool {
		t.Helper()
		ok, err := entity.Holds(srv.Store, via, entity.Answer, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if resp := answerFor(t, alice, ts.URL, "grant", "helper", `{}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's grant: %s: %s", resp.Status, bodyOf(t, resp))
	}
	mine := entity.ForRef(alice)
	if owner, _ := entity.OwnerOf(srv.Store, mine); owner != alice {
		t.Fatalf("%s is %q's, want alice's own", mine, owner)
	}
	if !holds(mine) {
		t.Fatal("the resolver alice granted does not answer for her")
	}

	pr := alicesPR(t, srv)
	if resp := answerFor(t, "user:bob@example.com", ts.URL, "grant", "helper", `{for: "demo.pr.\"1\""}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob granted at alice's pr: %s", resp.Status)
	}
	if resp := answerFor(t, "user:bob@example.com", ts.URL, "grant", "helper", fmt.Sprintf(`{for: %q}`, mine.String())); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob granted at alice's own node: %s", resp.Status)
	}
	if holds(pr) {
		t.Fatal("a refused grant was written")
	}
	if resp := answerFor(t, alice, ts.URL, "grant", "helper", `{for: "demo.pr.\"1\""}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's grant at her pr: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if !holds(pr) {
		t.Fatal("the resolver alice granted at her pr does not answer for it")
	}

	if resp := answerFor(t, alice, ts.URL, "revoke", "helper", `{}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's revoke: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if holds(mine) {
		t.Fatal("the resolver alice revoked still answers for her")
	}
	if !holds(pr) {
		t.Fatal("revoking for alice took back what she granted at her pr")
	}
	if resp := answerFor(t, alice, ts.URL, "grant", "nobody", `{}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a grant to a resolver that does not stand: %s", resp.Status)
	}
}
