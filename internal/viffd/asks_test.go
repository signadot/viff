package viffd

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/stock"
	"go.viff.dev/viff/pkg/task"
)

// ask posts an ask as who and answers the response.
func ask(t *testing.T, as, url, body string) *http.Response {
	t.Helper()
	return postAs(t, as, url+"/asks", "application/tony", body)
}

// askID reads the id an ask was written under.
func askID(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ask: %s: %s", resp.Status, bodyOf(t, resp))
	}
	var out api.Asked
	if err := client.Decode(resp, &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

// AN ASK IS ITS ASKER'S: written at mirror.ask.<id> under a minted id, owned by the asker, read by
// the asker and whom it asks and nobody else, and posed to whom it names.
func TestAnAskIsItsAskersAndReadByWhomItAsks(t *testing.T) {
	srv, ts := asking(t)
	const victor, oscar, bob = "user:victor@example.com", "user:oscar@example.com", "user:bob@example.com"
	id := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge viff/ask?", to: [oscar@example.com], on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}]}`))
	if !strings.HasPrefix(id, "land-") {
		t.Fatalf("id %q is not derived from the name", id)
	}
	ref := mirror.AskRef(id)
	if o, _ := entity.OwnerOf(srv.Store, ref); o != victor {
		t.Fatalf("the ask is %q's, want the asker's", o)
	}
	e, _, _ := srv.Store.Get(ref)
	if got := mirror.PrincipalFrom(e.Payload); got != victor {
		t.Fatalf("the ask says its asker is %q", got)
	}
	if got := mirror.GranteesTo(e.Payload); len(got) != 1 || got[0] != oscar {
		t.Fatalf("to %v, want [%s] in the grammar's spelling", got, oscar)
	}
	for who, want := range map[string]int{victor: http.StatusOK, oscar: http.StatusOK, bob: http.StatusForbidden} {
		if resp := getAs(t, who, ts.URL+"/entity?at="+ref.KPath()); resp.StatusCode != want {
			t.Errorf("%s reading the ask: %s, want %d", who, resp.Status, want)
		}
	}
	posed(t, srv, ref)
	qid := ref.Name()
	if got := heldOutTo(t, srv, qid); len(got) != 1 || got[0] != oscar {
		t.Fatalf("held out to %v, want oscar alone", got)
	}
	// And its question, which carries its words, is read as the ask is: on its own, and on the
	// docket. Waited for, because the check learns of the question's record a moment after the
	// docket writes it (7q4azhtj); what is asserted is who reads it once it has.
	q := ts.URL + "/entity?at=" + mirror.QuestionRef(qid).KPath()
	for _, who := range []string{victor, oscar} {
		deadline := time.Now().Add(2 * time.Second)
		for getAs(t, who, q).StatusCode != http.StatusOK {
			if time.Now().After(deadline) {
				t.Fatalf("%s never read the question", who)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if docket := bodyOf(t, getAs(t, who, ts.URL+"/asks?all=true")); !strings.Contains(docket, "Merge viff/ask?") {
			t.Errorf("%s's docket does not show the question", who)
		}
	}
	if resp := getAs(t, bob, q); resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob reading the question: %s, want 403", resp.Status)
	}
	if docket := bodyOf(t, getAs(t, bob, ts.URL+"/asks?all=true")); strings.Contains(docket, "Merge viff/ask?") {
		t.Errorf("bob's docket shows the question")
	}
}

// NOBODY WRITES INTO SOMEBODY ELSE'S ASK: not through the generic write, which viff's half
// refuses, and not by owning anything above it, since nobody but the system does.
func TestNobodyElseWritesAnAsk(t *testing.T) {
	srv, ts := asking(t)
	id := askID(t, ask(t, "user:victor@example.com", ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com], on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}]}`))
	ref := mirror.AskRef(id)
	for _, who := range []string{"user:bob@example.com", tester} {
		if _, _, err := entity.As(srv.Store, who).Write(entity.Write{
			At: entity.Path{Ref: ref, Access: "then"}, Value: entity.Obj{"prompt": "something else"}.Payload()}); err == nil {
			t.Errorf("%s rewrote victor's then", who)
		}
	}
}

// AN ASK IS CHECKED BEFORE IT IS WRITTEN, where the asker is present to hear what is wrong.
func TestAnAskIsCheckedWhereItIsAsked(t *testing.T) {
	srv, ts := asking(t)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for body, want := range map[string]string{
		`{name: x}`:                                   "question is required",
		`{name: x, question: q, to: [oscar]}`:         "a bare word names nothing",
		`{name: x, question: q, due: tomorrow}`:       "RFC 3339",
		`{name: x, question: q, due: "` + past + `"}`: "not in the future",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {prompt: a, script: ""}}]}`:                                                "one key",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {shell: "rm -rf /"}}]}`:                                                    "not something an ask runs",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {prompt: "", environment: {systems: {network: {mode: open}}}}}}]}`: "then.spawn: a `prompt`",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {prompt: "merge it"}}]}`:                                                   "not something an ask runs (spawn, answer, ask)",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {prompt: go, doer: {agent: viff}}}}]}`:                             "then.spawn: ",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it", runner: gpu}}}]}`:                             `then.spawn: unknown field "runner"`,
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {script: "true", chain: review}}}]}`:                               "then.spawn.chain: a firing's",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {script: "true", subject: demo.pr.x}}}]}`:                          "then.spawn.subject: a then's task is about its ask",
		`{name: x, question: q, labels: {"a b": c}}`:                                                                                              "a label's name is a word",
		`{name: x, question: q, onSilence: proceed}`:                                                                                              "has no due",
		`{name: x, question: q, due: "` + soon + `", onSilence: maybe}`:                                                                           "proceed or deny",
		// A key the ask does not know is refused rather than dropped: `answerers` dropped would
		// be an ask that names nobody, put to everyone.
		`{name: x, question: q, answerers: [oscar@example.com]}`:                                                                         "answerers",
		`{name: x, question: q, on: [{match: {verdict: proceed}, then: {ask: {name: d, question: q, answerers: [oscar@example.com]}}}]}`: "answerers",
	} {
		resp := ask(t, "user:victor@example.com", ts.URL, body)
		if msg := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, want) {
			t.Errorf("%s: %s %q, want 400 saying %q", body, resp.Status, msg, want)
		}
	}
	if n := len(entity.Below(srv.Store, mirror.IsAsk, 2, entity.ViffRoot, mirror.AskSystem)); n != 0 {
		t.Fatalf("%d refused asks were written", n)
	}
}

// A THEN'S SPAWN IS HELD TO THE RUNNER WHEN IT IS ASKED: one the runner cannot run is refused on
// the asker's terminal, and no ask stands to fail after somebody answers it. Written by hand, it
// gets no network it does not name, and this runner builds only the host's.
func TestAThensSpawnIsHeldToTheRunnerWhenAsked(t *testing.T) {
	srv, ts := asking(t)
	describeLocal(t, srv, stock.Open().Describe([]string{runner.NetworkOpen}))
	resp := ask(t, "user:victor@example.com", ts.URL, `{name: x, question: q, on: [{match: {verdict: proceed}, then: {spawn: {script: "true"}}}]}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, "mode none: this runner builds open only") {
		t.Errorf("a then whose network the runner does not build: %s %q, want 400", resp.Status, msg)
	}
	resp = ask(t, "user:victor@example.com", ts.URL, `{name: y, question: q, on: [{match: {verdict: proceed}, then: {spawn: {script: "true", environment: {systems: {network: {mode: open}}}}}}]}`)
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusCreated {
		t.Errorf("a then the runner runs: %s %q, want 201", resp.Status, msg)
	}
}

// SILENCE IS NO UNLESS THE ASKER SAYS YES: an ask whose date passes unanswered settles on what its
// asker said silence means, and on no where they said nothing, so nobody answering runs no `then`.
func TestAnAsksSilenceIsNoUnlessItSaysYes(t *testing.T) {
	srv, ts, clock := keeping(t, 0)
	for name, said := range map[string]string{"quiet": "", "lazy": ", onSilence: proceed", "said": ", onSilence: deny"} {
		want := task.Deny
		if said == ", onSilence: proceed" {
			want = task.Proceed
		}
		due := clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		body := `{name: ` + name + `, question: "Merge?", to: [oscar@example.com], due: "` + due + `"` + said + `}`
		ref := mirror.AskRef(askID(t, ask(t, "user:victor@example.com", ts.URL, body)))
		posed(t, srv, ref)
		clock.Advance(time.Minute)
		deadline := time.After(3 * time.Second)
		for {
			e, _, _ := srv.Store.Get(ref)
			if entity.Str(e.Payload, "status") != mirror.QuestionPending {
				if by, v := entity.Str(e.Payload, "by"), entity.Str(e.Payload, "verdict"); by != task.ByDeadline || v != want {
					t.Fatalf("%s settled by %q on %q, want the clock on %s", body, by, v, want)
				}
				break
			}
			select {
			case <-deadline:
				t.Fatalf("%s never settled at its date", body)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}

// WITHDRAWING IS THE ASKER'S, and a question about an ask that is gone is withdrawn.
func TestAnAskIsWithdrawnByItsAsker(t *testing.T) {
	srv, ts := asking(t)
	const victor, oscar = "user:victor@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	qid := ref.Name()
	if resp := deleteAs(t, oscar, ts.URL+"/asks/"+id); resp.StatusCode == http.StatusNoContent {
		t.Fatal("oscar withdrew victor's ask")
	}
	if resp := deleteAs(t, victor, ts.URL+"/asks/"+id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("victor's withdrawal: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if _, ok, _ := srv.Store.Get(ref); ok {
		t.Fatal("the ask stands")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		q, _, _ := srv.Store.Get(mirror.QuestionRef(qid))
		if entity.Str(q.Payload, "status") != mirror.QuestionPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("its question still waits")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Gone, it is nobody's to read, and withdrawing it again is refused like any id one cannot read.
	if resp := deleteAs(t, victor, ts.URL+"/asks/"+id); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("withdrawing it again: %s, want 403", resp.Status)
	}
}

// AN ANSWER IS TO THE ASK AS IT STANDS: one naming an earlier edit is refused, and one naming
// none answers the edit standing now.
func TestAnAnswerToAnEarlierEditIsRefused(t *testing.T) {
	srv, ts := asking(t)
	const victor, oscar = "user:victor@example.com", "user:oscar@example.com"
	id := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	// Asked again, as the verb that changes an ask does it: the record and the waiting question.
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"edit": 2, "pose": "Merge now?"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	e, _, _ := srv.Store.Get(ref)
	srv.Reg.Revise(askQuestion(id, e.Payload, ""))
	resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: oscar, Edit: 1})
	if msg := bodyOf(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(msg, "you answered edit 1") {
		t.Fatalf("an answer to edit 1: %s %q, want 409 saying it was asked again", resp.Status, msg)
	}
	if resp := answerAs(t, oscar, ts.URL, id); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("an answer naming no edit: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if got := waitField(t, srv, ref, "answer"); got != "yes" {
		t.Fatalf("the ask records %q, want yes", got)
	}
}
