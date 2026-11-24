package viffd

import (
	"net/http"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A RESOLVER IS GRANTED A PLACE BY ITS OWNER ON RECORD: what it answers there is given as that
// owner, so carol, who owns `demo` above dave's `demo.team`, does not grant at dave's place, and
// dave does, and nobody grants at a place not made yet. Carol may still take a grant at dave's
// back, as she may anything below her.
func TestAResolverIsGrantedAPlaceByItsOwnerOnRecord(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const carol, dave = "user:carol@example.com", "user:dave@example.com"
	owns(t, srv, carol, nil, entity.RefAt("demo"))
	owns(t, srv, dave, nil, entity.RefAt("demo", "team"))
	if resp := postAs(t, carol, ts.URL+"/resolvers", "application/tony",
		`{name: desk, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	via := "via:mirror.resolvers.desk"
	team := entity.RefAt("demo", "team")

	if code, msg := grantStatus(t, carol, ts.URL, "desk", `{for: demo.team}`); code != http.StatusForbidden || !strings.Contains(msg, dave) {
		t.Fatalf("carol's grant at dave's place: %d %s, want 403 naming dave", code, msg)
	}
	if held, err := entity.Holds(srv.Store, via, entity.Answer, team); err != nil || held {
		t.Fatalf("carol's refused grant stands at dave's place (held %v, %v)", held, err)
	}
	// A place not made yet is nobody's, and a grant waiting there would answer as whoever makes it.
	if code, msg := grantStatus(t, carol, ts.URL, "desk", `{for: demo.later}`); code != http.StatusForbidden || !strings.Contains(msg, "nobody's") {
		t.Fatalf("carol's grant at a place not made: %d %s, want 403", code, msg)
	}
	if code, msg := grantStatus(t, dave, ts.URL, "desk", `{for: demo.team}`); code != http.StatusNoContent {
		t.Fatalf("dave's grant at his own place: %d %s", code, msg)
	}
	if held, err := entity.Holds(srv.Store, via, entity.Answer, team); err != nil || !held {
		t.Fatalf("dave's grant does not stand (held %v, %v)", held, err)
	}
	if resp := answerFor(t, carol, ts.URL, "revoke", "desk", `{for: demo.team}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("carol's revoke at dave's place: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if held, err := entity.Holds(srv.Store, via, entity.Answer, team); err != nil || held {
		t.Fatalf("carol's revoke left the grant standing (held %v, %v)", held, err)
	}
}

// NOBODY HANDS DOWN `answer`: a child-grants line names nobody the resolver would answer for, so
// it is refused by the record, by PUT /perms before the place is taken, and as the root's default
// at start.
func TestChildGrantsRefuseAnswer(t *testing.T) {
	levels := entity.Levels{entity.Answer: {"via:mirror.resolvers.desk"}}
	if err := entity.CheckChildGrants(levels); err == nil {
		t.Fatal("child-grants holding answer passed the check")
	}

	srv, ts := shortKeep(t, askKeepDefault)
	const carol = "user:carol@example.com"
	resp := putBody(t, carol, ts.URL+"/perms", `{at: demo, child-grants: {answer: ["via:mirror.resolvers.desk"]}}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "child-grants") {
		t.Fatalf("PUT /perms handing down answer: %s %q, want 400", resp.Status, msg)
	}
	if owner, err := entity.OwnerOf(srv.Store, entity.RefAt("demo")); err != nil || owner == carol {
		t.Fatalf("the refused edit took demo for carol (owner %q, %v)", owner, err)
	}
	if err := entity.Establish(srv.Store, entity.RefAt("demo"), carol, entity.Ref{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetChildGrants(srv.Store, entity.RefAt("demo"), levels); err == nil {
		t.Fatal("SetChildGrants recorded answer")
	}

	refusing := serverWith(t, runnerdtest.Scripted, entitytest.Store(t), WithPerms(Perms{ChildGrants: levels}))
	if err := refusing.SeedError(); err == nil || !strings.Contains(err.Error(), "child-grants") {
		t.Fatalf("a root default handing down answer: seed error %v, want it refused", err)
	}
}
