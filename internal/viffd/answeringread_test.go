package viffd

import (
	"testing"

	"go.viff.dev/viff/internal/resolver"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// AN ASK IS READ BY THE RESOLVERS SERVING IT (9ce3bva0, S2): one its party granted, whether the
// party is a person it names or the place it is about, and whenever the grant was made. One
// nobody granted reads nothing.
func TestAnAskIsReadByTheResolversAnsweringForItsParties(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	installMatch(t, ts.URL, "bobs", `{}`)
	installMatch(t, ts.URL, "alices", `{}`)
	installMatch(t, ts.URL, "nobodys", `{}`)
	reads := func(name string, at entity.Ref) bool {
		t.Helper()
		ok, err := entity.Holds(srv.Store, entity.Via(resolver.Ref(name)), entity.Read, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// A person's ask to bob, read by the resolver bob grants after it was asked.
	id := askID(t, ask(t, "alice@example.com", ts.URL, `{name: land, question: "land it?", to: [bob@example.com]}`))
	posed(t, srv, mirror.AskRef(id))
	if reads("bobs", mirror.AskRef(id)) {
		t.Fatal("a resolver bob has not granted reads his ask")
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "bobs", `{}`); code != 204 {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	if !reads("bobs", mirror.AskRef(id)) {
		t.Fatal("the resolver bob granted does not read the ask put to him")
	}

	// A rule's gate about alice's pr, naming nobody: the place is its party.
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != 201 {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	if code, msg := grantStatus(t, "user:alice@example.com", ts.URL, "alices", `{for: "demo.pr.\"1\""}`); code != 204 {
		t.Fatalf("alice's grant at her pr: %d %s", code, msg)
	}
	if !reads("alices", mirror.QuestionRef(qid)) {
		t.Fatal("the resolver alice granted at her pr does not read the gate about it")
	}
	for _, at := range []entity.Ref{mirror.AskRef(id), mirror.QuestionRef(qid)} {
		if reads("nobodys", at) {
			t.Errorf("a resolver nobody granted reads %s", at)
		}
	}
}
