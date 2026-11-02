package versed

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// change patches an ask as who.
func change(t *testing.T, as, url, id, body string) *http.Response {
	t.Helper()
	return changeAs(t, as, "", url, id, body)
}

// changeAs is change made as the resolver named, presented by as, where one is named.
func changeAs(t *testing.T, as, resolver, url, id, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, url+"/asks/"+id, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, as)
	if resolver != "" {
		req.Header.Set(api.ResolverHeader, resolver)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const (
	chAsker  = "user:daniel@example.com"
	chDavido = "user:oscar@example.com"
	chBob    = "user:bob@example.com"
)

// A NEW DATE MOVES THE CLOCK AND ASKS NOTHING AGAIN: the edit stands, the deadline is the new
// date, and silence by then settles on what it did before — no, where the asker never said.
func TestAnAsksDateMovesWithoutAskingAgain(t *testing.T) {
	srv, ts := asking(t)
	in24 := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	for body, want := range map[string]string{
		`{name: land, question: "Merge?", to: [oscar@example.com]}`:                                          task.Deny,
		`{name: lazy, question: "Merge?", to: [oscar@example.com], due: "` + in24 + `", onSilence: proceed}`: task.Proceed,
	} {
		id := askID(t, ask(t, chAsker, ts.URL, body))
		ref := mirror.AskRef(id)
		posed(t, srv, ref)
		due := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
		if resp := change(t, chAsker, ts.URL, id, `{due: "`+due+`"}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("moving the date: %s: %s", resp.Status, bodyOf(t, resp))
		}
		e, _, _ := srv.Store.Get(ref)
		if got := mirror.EditOf(e.Payload); got != 1 {
			t.Fatalf("moving the date asked again: edit %d", got)
		}
		_, dflt, at := mirror.DeadlineOf(e.Payload)
		if stamped, _ := time.Parse(time.RFC3339Nano, at); !stamped.Equal(mustTime(t, due)) || dflt != want {
			t.Fatalf("%s: the clock is at %q defaulting to %q, want %s and %s", body, at, dflt, due, want)
		}
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// A NEW THEN ASKS AGAIN: an answer to the edit before is refused, and a yes to the edit asked runs
// what it says now.
func TestAChangedThenAsksAgain(t *testing.T) {
	srv, ts := asking(t)
	id := askID(t, ask(t, chAsker, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com], on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	if resp := change(t, chAsker, ts.URL, id, `{on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it and tag it", environment: {systems: {network: {mode: open}}}}}}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("changing then: %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: chDavido, Edit: 1})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("an answer to edit 1 after the change: %s, want 409", resp.Status)
	}
	resp = postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: chDavido, Edit: 2})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("an answer to edit 2: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := entity.Str(thenTaskOf(t, srv, ref).Payload, "prompt"); got != "merge it and tag it" {
		t.Fatalf("the yes ran %q, want the then as changed", got)
	}
}

// NEW ANSWERERS ASK AGAIN, OF THEM: whom it asked before no longer answers, whom it asks now does,
// and it waits on them.
func TestChangedAnswerersAreAskedInstead(t *testing.T) {
	srv, ts := asking(t)
	id := askID(t, ask(t, chAsker, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	// Said as `answerers`, the key it once was, the change is refused rather than dropped.
	if resp := change(t, chAsker, ts.URL, id, `{answerers: [bob@example.com]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("changing answerers under their old key: %s, want 400", resp.Status)
	}
	if resp := change(t, chAsker, ts.URL, id, `{to: [bob@example.com]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("changing answerers: %s: %s", resp.Status, bodyOf(t, resp))
	}
	e, _, _ := srv.Store.Get(ref)
	if got := entity.Strs(e.Payload, mirror.WaitingOnField); len(got) != 1 || got[0] != chBob {
		t.Fatalf("it waits on %v, want bob", got)
	}
	if resp := answerAs(t, chDavido, ts.URL, id); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("davido, no longer asked, answering: %s, want 403", resp.Status)
	}
	if resp := answerAs(t, chBob, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob answering: %s: %s", resp.Status, bodyOf(t, resp))
	}
}

// ONLY ITS ASKER CHANGES AN ASK, though whom it asks may write its record; and nobody changes one
// that has settled.
func TestAnAskIsChangedByItsAskerWhileItWaits(t *testing.T) {
	srv, ts := asking(t)
	id := askID(t, ask(t, chAsker, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	if resp := change(t, chDavido, ts.URL, id, `{question: "Merge today?"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("davido changing daniel's ask: %s, want 403", resp.Status)
	}
	if resp := answerAs(t, chDavido, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
	waitField(t, srv, ref, "answer")
	if resp := change(t, chAsker, ts.URL, id, `{question: "Merge today?"}`); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(bodyOf(t, resp), "has settled") {
		t.Fatalf("changing a settled ask: %s, want 409 saying it has settled", resp.Status)
	}
}

// AN ONWARD ASK IS CHANGED BY THE RESOLVER THAT RAISED IT, as itself, and not by whoever installed
// it acting as themselves: the record is the installer's, since a resolver owns nothing, but the
// installer did not ask it.
func TestAnOnwardAskIsChangedByTheResolverThatRaisedIt(t *testing.T) {
	srv, url, served := servedPending(t)
	const alice = "user:alice@example.com"
	onward := raiseOnward(t, srv, url, served, "")
	if resp := changeAs(t, alice, "", url, onward, `{question: "should bob land it today?"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice, as herself, changing her resolver's onward ask: %s, want 403", resp.Status)
	}
	if resp := changeAs(t, alice, "helper", url, onward, `{question: "should bob land it today?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("the resolver changing its onward ask: %s: %s", resp.Status, bodyOf(t, resp))
	}
	e, _, _ := srv.Store.Get(mirror.AskRef(onward))
	if got := entity.Str(e.Payload, "question"); got != "should bob land it today?" {
		t.Fatalf("the onward ask asks %q, want the resolver's new words", got)
	}
	if got := mirror.PrincipalFrom(e.Payload); got != "via:verse.resolvers.helper" {
		t.Fatalf("the changed onward ask is from %q, want the resolver still", got)
	}
}

// AND SO IS ANY ASK A RESOLVER RAISED: one with no `serves` is from the resolver as much as an
// onward one, so its installer, as themselves, is refused changing it too.
func TestAResolversOwnAskIsChangedByTheResolver(t *testing.T) {
	srv, url, _ := servedPending(t)
	const alice = "user:alice@example.com"
	id := askID(t, postAsResolver(t, alice, "helper", url+"/asks",
		`{name: plain, question: "should bob land it?", to: [mgr@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if resp := changeAs(t, alice, "", url, id, `{question: "should bob land it today?"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice, as herself, changing her resolver's ask: %s, want 403", resp.Status)
	}
	if resp := changeAs(t, alice, "helper", url, id, `{question: "should bob land it today?"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("the resolver changing its ask: %s: %s", resp.Status, bodyOf(t, resp))
	}
}
