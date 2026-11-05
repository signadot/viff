package versed

import (
	"net/http"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// ESCALATING IS AN ONWARD ASK (9ce3bva0, stage 4): bob's resolver asks his manager, the manager's
// resolver asks a director, and the director's answer comes back over both hops — carried to the
// manager's ask as the manager's, and from there to bob's as his.
func TestAnEscalationIsOnwardAsksOverTwoHops(t *testing.T) {
	srv, url, served := servedPending(t)
	const alice, mgr = "user:alice@example.com", "user:mgr@example.com"
	if resp := postAs(t, alice, url+"/resolvers", "application/tony",
		`{name: mgr-desk, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, mgr, url, "mgr-desk", `{}`); code != http.StatusNoContent {
		t.Fatalf("the manager's grant: %d %s", code, msg)
	}

	hop1 := raiseOnward(t, srv, url, served, "")
	hop2 := askID(t, postAsResolver(t, alice, "mgr-desk", url+"/asks",
		`{name: up2, question: "should bob land it?", to: [director@example.com], serves: `+hop1+`}`))
	posed(t, srv, mirror.AskRef(hop2))
	e, _, _ := srv.Store.Get(mirror.AskRef(hop2))
	h1, _, _ := srv.Store.Get(mirror.AskRef(hop1))
	if entity.Str(e.Payload, "due") != entity.Str(h1.Payload, "due") {
		t.Fatalf("the second hop is due %s, want the first's %s", entity.Str(e.Payload, "due"), entity.Str(h1.Payload, "due"))
	}

	if resp := answerAs(t, "user:director@example.com", url, hop2); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the director's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if rec := settledQuestion(t, srv, hop1); rec["by"] != mgr || rec["via"] != "via:verse.resolvers.mgr-desk" {
		t.Fatalf("the first hop settled by %v via %v, want the manager's, through mgr-desk", rec["by"], rec["via"])
	}
	if rec := settledQuestion(t, srv, served); rec["verdict"] != "proceed" || rec["by"] != "user:bob@example.com" {
		t.Fatalf("bob's ask settled %v by %v, want the director's yes, as bob's", rec["verdict"], rec["by"])
	}
}
