package versed

import (
	"net/http"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// AN ASK'S ANSWERERS ARE A LIST, however many it names. One name was stored as a string, and a
// change that rewords the ask wrote it back that way after posing had made it a list, so a reader
// taking a list found nobody.
func TestAnAsksAnswerersAreAListHoweverManyItNames(t *testing.T) {
	srv, ts := asking(t)
	const victor = "user:victor@example.com"
	id := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	stored := func(when string) {
		t.Helper()
		e, ok, err := srv.Store.Get(mirror.AskRef(id))
		if err != nil || !ok {
			t.Fatalf("%s: the ask is not stored: %v", when, err)
		}
		if got := mirror.GranteesTo(e.Payload); len(got) != 1 || got[0] != "user:oscar@example.com" {
			t.Fatalf("%s: to = %v, want the one name as a list", when, entity.Get(e.Payload, mirror.ToField))
		}
	}
	stored("posed")
	if resp := change(t, victor, ts.URL, id, `{question: "Merge, and tag it?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("the change: %s", resp.Status)
	}
	stored("reworded")
}
