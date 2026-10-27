package versed

import (
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/mirror"
)

// A THEN THAT ANSWERS BACK IS REFUSED. bob's ask waits on alice; alice's ask answers bob's on a
// yes. bob then changing his ask to answer alice's would close a loop: each ask's settle would
// run the other's answer from inside itself (consequence.go). The change is refused where the loop
// would close, naming the chain.
func TestAThenThatAnswersBackIsRefused(t *testing.T) {
	srv, ts := asking(t)
	const bob, alice = "user:bob@example.com", "user:alice@example.com"
	first := askID(t, ask(t, bob, ts.URL, `{name: first, question: "Ship?", to: [alice@example.com]}`))
	posed(t, srv, mirror.AskRef(first))
	second := askID(t, ask(t, alice, ts.URL, `{name: second, question: "Tag it too?", to: [carol@example.com, bob@example.com],
		then: {answer: {ask: "`+first+`", choice: proceed}}}`))
	posed(t, srv, mirror.AskRef(second))
	resp := change(t, bob, ts.URL, first, `{then: {answer: {ask: "`+second+`", choice: proceed}}}`)
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "a loop") || !strings.Contains(body, second) {
		t.Fatalf("a then answering back = %s: %s, want refused as a loop naming %s", resp.Status, body, second)
	}
	// A then answering an ask that answers a third is fine: no loop.
	third := askID(t, ask(t, alice, ts.URL, `{name: third, question: "Announce?", to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(third))
	if resp := change(t, bob, ts.URL, first, `{then: {answer: {ask: "`+third+`", choice: proceed}}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("a then answering an ask that answers nothing back: %s: %s", resp.Status, bodyOf(t, resp))
	}
}
