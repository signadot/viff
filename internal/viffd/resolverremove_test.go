package viffd

import (
	"net/http"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// A RESOLVER'S GRANTS GO WITH IT: bob grants alice's r1, alice removes it, and mallory installs an
// r1 of her own. Bob's grant was to the one alice ran, so mallory's answers nothing for him.
func TestARemovedResolversGrantsDoNotPassToTheNextOfItsName(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice, bob, mallory = "user:alice@example.com", "user:bob@example.com", "user:mallory@example.com"
	const r1 = `{name: r1, match: {}, method: {driver: service}}`
	if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony", r1); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice's install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, bob, ts.URL, "r1", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	if resp := deleteAs(t, alice, ts.URL+"/resolvers/r1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice's removal: %s: %s", resp.Status, bodyOf(t, resp))
	}
	via := "via:mirror.resolvers.r1"
	if held, err := entity.Holds(srv.Store, via, entity.Answer, entity.ForRef(bob)); err != nil || held {
		t.Fatalf("bob's grant to the removed r1 stands (held %v, %v)", held, err)
	}

	if resp := postAs(t, mallory, ts.URL+"/resolvers", "application/tony", r1); resp.StatusCode != http.StatusCreated {
		t.Fatalf("mallory's install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	id := askID(t, ask(t, "user:carol@example.com", ts.URL, `{name: land, question: q, to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if code, msg := answerAsResolver(t, mallory, "r1", ts.URL, id, "proceed", ""); code < 400 {
		t.Fatalf("mallory's r1 answered bob's ask: %d %s", code, msg)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(id)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("bob's ask was settled by a resolver he never granted: %v", entity.ToObj(e.Payload))
	}
}

// A GRANT TO A RESOLVER SINCE REMOVED IS STILL YOURS TO TAKE BACK: revoking it needs no resolver
// standing.
func TestAGrantToAResolverSinceRemovedCanBeRevoked(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const bob = "user:bob@example.com"
	// A line left from before removal withdrew grants: bob's grant to r9, which no longer stands.
	if code, msg := grantStatus(t, bob, ts.URL, "nothing-yet", `{}`); code != http.StatusNotFound {
		t.Fatalf("a grant to a resolver that does not stand: %d %s, want 404", code, msg)
	}
	at := entity.ForRef(bob)
	if err := entity.Establish(srv.Store, at, bob, entity.Ref{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetLevel(srv.Store, at, entity.Answer, []string{"via:mirror.resolvers.r9"}); err != nil {
		t.Fatal(err)
	}
	if resp := answerFor(t, bob, ts.URL, "revoke", "r9", `{}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob's revoke of r9: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if held, err := entity.Holds(srv.Store, "via:mirror.resolvers.r9", entity.Answer, at); err != nil || held {
		t.Fatalf("r9 still holds answer for bob (held %v, %v)", held, err)
	}
}
