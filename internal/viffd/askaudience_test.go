package viffd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// alicesPR is a pr alice holds, which carol may read and dan may write: nobody else reads it.
func alicesPR(t *testing.T, srv *Server) entity.Ref {
	t.Helper()
	pr := entity.RefAt("demo", "pr", "1")
	owns(t, srv, "user:alice@example.com", []entity.Grant{{At: pr.Segs(), To: map[entity.Level][]string{
		entity.Read: {"user:carol@example.com"}, entity.ReadWrite: {"user:dan@example.com"},
	}}}, pr)
	entitytest.Set(t, srv.Store, pr, entity.Obj{"stage": "open"}.Payload())
	return pr
}

// AN ASK ABOUT SOMETHING IS READ AND ANSWERED AS THAT THING IS (9ce3bva0, D3). A rule's gate that
// names nobody, about a pr only alice, carol and dan may read: bob neither reads it nor sees it on
// the docket nor answers it; carol reads it and may not answer, since she may not write the pr;
// dan, who may, answers it.
func TestAnAskIsReadAndAnsweredAsItsSubjectIs(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	if resp := installAs(t, "alice@example.com", ts.URL, gatedRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	pr := alicesPR(t, srv)
	qid := waitRuleQuestion(t, srv, "land", pr.String())
	at := "/entity?at=" + mirror.QuestionRef(qid).String()

	read := func(as, path string) (int, string) {
		resp := getAs(t, as, ts.URL+path)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := read("bob@example.com", at); code != http.StatusForbidden {
		t.Fatalf("bob read an ask about a pr he may not read: %d", code)
	}
	if _, docket := read("bob@example.com", "/asks?all=true"); strings.Contains(docket, qid) {
		t.Fatalf("bob's docket lists an ask about a pr he may not read: %s", docket)
	}
	if code, _ := read("carol@example.com", at); code != http.StatusOK {
		t.Fatalf("carol, who reads the pr, could not read the ask about it: %d", code)
	}
	if _, docket := read("carol@example.com", "/asks?all=true"); !strings.Contains(docket, qid) {
		t.Fatalf("carol's docket does not list the ask about a pr she reads: %s", docket)
	}
	for _, who := range []string{"user:bob@example.com", "user:carol@example.com"} {
		resp := answerAs(t, who, ts.URL, qid)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s, who may not write the pr, answered the ask about it: %s", who, resp.Status)
		}
	}
	if resp := answerAs(t, "user:dan@example.com", ts.URL, qid); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dan, who may write the pr, could not answer: %s: %s", resp.Status, bodyOf(t, resp))
	}
}

// NOBODY IS ASKED ABOUT WHAT THEY CANNOT READ: an ask naming somebody who may not read its subject
// is refused where it is asked — a person's with 403, a firing's or a task's as the error its
// asking returns — and an answer from somebody who could read it once and no longer may is
// refused when it comes.
func TestAnAskToWhoCannotReadItsSubjectIsRefused(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	pr := alicesPR(t, srv)

	resp := ask(t, "alice@example.com", ts.URL, `{name: land, question: "land it?", to: [bob@example.com], about: "demo.pr.\"1\""}`)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(bodyOf(t, resp), "user:bob@example.com may not read") {
		t.Fatalf("alice asked bob about a pr he may not read: %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp = ask(t, "bob@example.com", ts.URL, `{name: land, question: "land it?", to: [carol@example.com], about: "demo.pr.\"1\""}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob asked about a pr he may not read: %s", resp.Status)
	}

	// Naming nobody, alice's ask is read by whoever reads the pr, and not by bob.
	vote := askID(t, ask(t, "alice@example.com", ts.URL, `{name: vote, question: "land it?", about: "demo.pr.\"1\""}`))
	posed(t, srv, mirror.AskRef(vote))
	if resp := getAs(t, "bob@example.com", ts.URL+"/entity?at="+mirror.AskRef(vote).String()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob read an ask about a pr he may not read: %s", resp.Status)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second) // refused, not waited on
	defer cancel()
	_, err := srv.Reg.Resolve(ctx, task.GateQuestion{ID: "gate~1", Subject: pr.String(),
		Answerers: []string{"user:bob@example.com"}}, task.ToolUse{Name: "test"})
	if !errors.Is(err, mirror.ErrCannotRead) {
		t.Fatalf("a firing asked bob about a pr he may not read: %v", err)
	}

	id := askID(t, ask(t, "alice@example.com", ts.URL, `{name: land, question: "land it?", to: [carol@example.com], about: "demo.pr.\"1\""}`))
	posed(t, srv, mirror.AskRef(id))
	if err := entity.SetLevel(srv.Store, pr, entity.Read, nil); err != nil {
		t.Fatal(err)
	}
	resp = answerAs(t, "user:carol@example.com", ts.URL, id)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(bodyOf(t, resp), "may not read") {
		t.Fatalf("carol answered about a pr she may no longer read: %s: %s", resp.Status, bodyOf(t, resp))
	}
}
