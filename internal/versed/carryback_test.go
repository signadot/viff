package versed

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

func raiseOnward(t *testing.T, srv *Server, url, served, extra string) string {
	t.Helper()
	id := askID(t, postAsResolver(t, "user:alice@example.com", "helper", url+"/asks",
		`{name: up, question: "should bob land it?", to: [mgr@example.com], serves: `+served+extra+`}`))
	posed(t, srv, mirror.AskRef(id))
	return id
}

func carried(t *testing.T, srv *Server, id string) entity.Payload {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(id)); ok {
			if c := entity.Get(e.Payload, "carried"); c != nil {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never carried its answer back", id)
	return nil
}

// WHAT AN ONWARD ASK COMES TO IS CARRIED BACK (9ce3bva0, stage 4, Q6): versed answers the ask it
// serves, as the resolver that raised it, for the party it was raised for.
func TestAnOnwardAsksAnswerIsCarriedBack(t *testing.T) {
	srv, url, served := servedPending(t)
	onward := raiseOnward(t, srv, url, served, "")
	if resp := answerAs(t, "user:mgr@example.com", url, onward); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the manager's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	rec := settledQuestion(t, srv, served)
	if rec["verdict"] != "proceed" || rec["by"] != "user:bob@example.com" || rec["via"] != "via:verse.resolvers.helper" {
		t.Fatalf("the served ask settled %v by %v via %v, want the manager's yes, for bob, through helper", rec["verdict"], rec["by"], rec["via"])
	}
	if c := entity.ToObj(carried(t, srv, onward)); fmt.Sprint(c["status"]) != "204" {
		t.Fatalf("the onward ask says it carried %v", c)
	}
	// Once: a carry recorded is not made again when the onward ask changes later. The clock
	// moves on, so a second carry would stamp a later instant.
	first := fmt.Sprint(entity.ToObj(carried(t, srv, onward))["at"])
	srv.clock.(*fakeClock).Advance(2 * time.Second)
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: mirror.AskRef(onward).At(), Value: entity.Obj{"reason": "later"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if again := fmt.Sprint(entity.ToObj(carried(t, srv, onward))["at"]); again != first {
		t.Fatalf("the carry was made again when the onward ask changed: at %s, then %s", first, again)
	}
}

// Its clock, earlier than the served ask's, settles on what silence settles on there, and that is
// carried back as any answer is.
func TestAnOnwardAsksSilenceIsCarriedBack(t *testing.T) {
	srv, url, served := servedPending(t)
	clock := srv.clock.(*fakeClock)
	soon := clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	raiseOnward(t, srv, url, served, `, due: "`+soon+`"`)
	clock.Advance(time.Minute)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(mirror.AskRef(served)); ok && entity.Str(e.Payload, "status") == mirror.QuestionAnswered {
			if v, by := entity.Str(e.Payload, "verdict"), entity.Str(e.Payload, "by"); v != "deny" || by != "user:bob@example.com" {
				t.Fatalf("the served ask settled %s by %s, want the onward ask's silence, deny, for bob", v, by)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the onward ask's silence was never carried back")
}

// Carried back as the resolver's answer at that moment: a party that revoked it in between is
// answered for by nobody, the served ask waits on, and the onward ask says why.
func TestACarryIsHeldToTheGrantAsItStands(t *testing.T) {
	srv, url, served := servedPending(t)
	onward := raiseOnward(t, srv, url, served, "")
	if resp := answerFor(t, "user:bob@example.com", url, "revoke", "helper", `{}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob's revoke: %s", resp.Status)
	}
	if resp := answerAs(t, "user:mgr@example.com", url, onward); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the manager's answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	c := entity.ToObj(carried(t, srv, onward))
	if fmt.Sprint(c["status"]) != "403" || !strings.Contains(fmt.Sprint(c["refused"]), "helper") {
		t.Fatalf("the onward ask says it carried %v, want a refusal", c)
	}
	if e, _, _ := srv.Store.Get(mirror.AskRef(served)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("the served ask settled on a carry nobody may give: %v", e.Payload)
	}
}

// An onward ask found answered and not carried — its process stopped between the two — is carried
// when the watch on the asks sees it, as a restart's replay does.
func TestAnAnsweredOnwardAskNotYetCarriedIsCarried(t *testing.T) {
	srv, url, served := servedPending(t)
	onward := raiseOnward(t, srv, url, served, "")
	srv.asks.mu.Lock()
	wait := srv.asks.waits[onward]
	srv.asks.mu.Unlock()
	wait.cancel(errors.New("the process stopped"))
	<-wait.done
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: mirror.AskRef(onward).At(), Value: entity.Obj{
			"status": mirror.QuestionAnswered, "verdict": "proceed", "by": "user:mgr@example.com",
		}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	if rec := settledQuestion(t, srv, served); rec["verdict"] != "proceed" || rec["by"] != "user:bob@example.com" {
		t.Fatalf("the served ask settled %v by %v, want the onward ask's yes, for bob", rec["verdict"], rec["by"])
	}
}
