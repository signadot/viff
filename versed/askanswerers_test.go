package versed

import (
	"net/http"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// AN ASK'S ANSWERERS ARE A LIST, however many it names. One name was stored as a string, and a
// change that rewords the ask wrote it back that way after posing had made it a list, so a reader
// taking a list found nobody.
func TestAnAsksAnswerersAreAListHoweverManyItNames(t *testing.T) {
	srv, ts := asking(t)
	const daniel = "user:daniel@example.com"
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", answerers: [oscar@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	stored := func(when string) {
		t.Helper()
		e, ok, err := srv.Store.Get(mirror.AskRef(id))
		if err != nil || !ok {
			t.Fatalf("%s: the ask is not stored: %v", when, err)
		}
		if got := entity.Strs(e.Payload, "answerers"); len(got) != 1 || got[0] != "user:oscar@example.com" {
			t.Fatalf("%s: answerers = %v, want the one name as a list", when, entity.Get(e.Payload, "answerers"))
		}
	}
	stored("posed")
	if resp := change(t, daniel, ts.URL, id, `{question: "Merge, and tag it?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("the change: %s", resp.Status)
	}
	stored("reworded")
}
