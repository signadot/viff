package versed

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
	"github.com/signadot/verse/pkg/task"
)

// waitWithdrawn waits for an ask to be withdrawn, and answers why.
func waitWithdrawn(t *testing.T, srv *Server, id string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok && entity.Str(e.Payload, "status") == mirror.QuestionOrphaned {
			return fmtPayload(e.Payload)
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(id))
	t.Fatalf("%s was never withdrawn: %v", id, e.Payload)
	return ""
}

// AN ONWARD ASK FOLLOWS THE ASK IT SERVES (9ce3bva0, stage 4): answered there, it is withdrawn,
// and so it is when the served ask is asked again or withdrawn; a sooner clock there moves its own.
func TestAnOnwardAskIsWithdrawnWhenItsAskSettles(t *testing.T) {
	srv, url, served := servedPending(t)
	onward := raiseOnward(t, srv, url, served, "")
	if code, msg := answerForcing(t, "user:bob@example.com", url, served, true); code != http.StatusNoContent {
		t.Fatalf("bob's own answer, over his resolver: %d %s", code, msg)
	}
	e := func() entity.Payload { e, _, _ := srv.Store.Get(mirror.AskRef(onward)); return e.Payload }
	waitWithdrawn(t, srv, onward)
	if !strings.Contains(fmtPayload(e()), "has settled") {
		t.Fatalf("the withdrawn onward ask does not say why: %s", fmtPayload(e()))
	}
}

func TestAnOnwardAskIsWithdrawnWhenItsAskIsAskedAgainOrWithdrawn(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	if resp := postAs(t, "user:alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	served := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: "land it?", to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(served))

	reworded := raiseOnward(t, srv, ts.URL, served, "")
	if resp := change(t, "user:carol@example.com", ts.URL, served, `{question: "land it today?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("carol's change: %s: %s", resp.Status, bodyOf(t, resp))
	}
	waitWithdrawn(t, srv, reworded)
	if e, _, _ := srv.Store.Get(mirror.AskRef(reworded)); !strings.Contains(fmtPayload(e.Payload), "asked again") {
		t.Fatalf("the onward ask does not say its ask was asked again: %s", fmtPayload(e.Payload))
	}

	gone := raiseOnward(t, srv, ts.URL, served, "")
	if resp := deleteAs(t, "user:carol@example.com", ts.URL+"/asks/"+served); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("carol's withdrawal: %s", resp.Status)
	}
	waitWithdrawn(t, srv, gone)

	// And one whose served ask is removed outright, with no withdrawal before it.
	other := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land2, question: "land it?", to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(other))
	removed := raiseOnward(t, srv, ts.URL, other, "")
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{At: mirror.AskRef(other).At()}); err != nil {
		t.Fatal(err)
	}
	if why := waitWithdrawn(t, srv, removed); !strings.Contains(why, "is gone") {
		t.Fatalf("the onward ask does not say its ask is gone: %s", why)
	}
}

// A sooner clock on the served ask moves an onward ask's own to it; a later one leaves it.
func TestAnOnwardAsksClockFollowsItsAsksSoonerClock(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	if resp := postAs(t, "user:alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	in48 := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	served := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: q, to: [bob@example.com], due: "`+in48+`"}`))
	posed(t, srv, mirror.AskRef(served))
	onward := raiseOnward(t, srv, ts.URL, served, "")
	dueOf := func() string { e, _, _ := srv.Store.Get(mirror.AskRef(onward)); return entity.Str(e.Payload, "due") }

	in24 := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if resp := change(t, "user:carol@example.com", ts.URL, served, `{due: "`+in24+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("carol's sooner date: %s: %s", resp.Status, bodyOf(t, resp))
	}
	deadline := time.Now().Add(10 * time.Second)
	for dueOf() != in24 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := dueOf(); got != in24 {
		t.Fatalf("the onward ask is due %s, want the served ask's sooner %s", got, in24)
	}
	// Moved, it still settles on what the served ask's silence does: carol said nothing, so no.
	if e, _, _ := srv.Store.Get(mirror.AskRef(onward)); true {
		if _, dflt, _ := mirror.DeadlineOf(e.Payload); dflt != task.Deny {
			t.Fatalf("the moved onward ask settles on %q in silence, want the served ask's deny", dflt)
		}
	}
	in36 := time.Now().Add(36 * time.Hour).UTC().Format(time.RFC3339)
	if resp := change(t, "user:carol@example.com", ts.URL, served, `{due: "`+in36+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("carol's later date: %s: %s", resp.Status, bodyOf(t, resp))
	}
	time.Sleep(300 * time.Millisecond)
	if got := dueOf(); got != in24 {
		t.Fatalf("a later clock moved the onward ask to %s", got)
	}
}

func fmtPayload(p entity.Payload) string {
	b, _ := codec.NodeBytes(p, codec.Tony)
	return string(b)
}

// A resolver removed takes what it raised with it: its waiting onward asks are withdrawn, since
// nobody is left to carry them back. What it served is its askers' and stays.
func TestRemovingAResolverWithdrawsWhatItRaised(t *testing.T) {
	srv, url, served := servedPending(t)
	onward := raiseOnward(t, srv, url, served, "")
	if resp := deleteAs(t, "user:alice@example.com", url+"/resolvers/helper"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("removing helper: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if why := waitWithdrawn(t, srv, onward); !strings.Contains(why, "was removed") {
		t.Fatalf("the onward ask does not say its resolver was removed: %s", why)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(served)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("the ask it served moved with it: %v", e.Payload)
	}
}
