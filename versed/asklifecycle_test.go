package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// AN ANSWER DELIVERED BEFORE A REWORDING IS NOT OVERTAKEN BY IT: davido's yes to edit 1 reaches
// the waiter, and daniel's change to the words, which would have asked edit 2, is refused whole:
// the record stays at edit 1, and its date does not move. Before, the record moved to edit 2 and
// the yes settled it, running a `then` davido never saw.
func TestAnAnswerDeliveredBeforeARewordingIsNotOvertaken(t *testing.T) {
	srv, ts := asking(t)
	const daniel, davido = "user:daniel@example.com", "user:oscar@example.com"
	due := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	id := askID(t, ask(t, daniel, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com], due: "`+due+`"}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)

	res := task.Choose(task.Proceed, davido)
	res.Edit = 1
	if err := srv.Reg.Deliver(id, res); err != nil {
		t.Fatalf("delivering davido's yes: %v", err)
	}
	later := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	resp := change(t, daniel, ts.URL, id, `{question: "Merge, and tag it?", then: {spawn: {prompt: "merge and tag", environment: {systems: {network: {mode: open}}}}}, due: "`+later+`"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a change after the answer was delivered: %s: %s, want 409", resp.Status, bodyOf(t, resp))
	}
	e, _, _ := srv.Store.Get(ref)
	if got := mirror.EditOf(e.Payload); got != 1 || entity.Get(e.Payload, "then") != nil {
		t.Fatalf("the refused change moved the ask to edit %d: %v", got, fmtPayload(e.Payload))
	}
	if got := entity.Str(e.Payload, "due"); got != due {
		t.Fatalf("the refused change moved the date to %s, want %s", got, due)
	}
}

// ONLY A PERSON'S ASK IS WITHDRAWN BY DELETING IT: a rule's or a task's question is waited on by
// what raised it, and its owner's DELETE is refused rather than leaving that waiting on nothing.
func TestOnlyAPersonsAskIsDeleted(t *testing.T) {
	srv, ts := asking(t)
	const scott = "user:scott@example.com"
	ref := mirror.AskRef("verse.trigger.review.review~7")
	if err := entity.Establish(srv.Store, ref, scott, entity.Ref{}, nil); err != nil {
		t.Fatal(err)
	}
	entitytest.Set(t, srv.Store, ref, entity.Obj{"status": mirror.QuestionPending, "gate": "review"}.Payload())
	if resp := deleteAs(t, scott, ts.URL+"/asks/verse.trigger.review.review~7"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting a rule's question: %s: %s, want 409", resp.Status, bodyOf(t, resp))
	}
	if _, ok, _ := srv.Store.Get(ref); !ok {
		t.Fatal("the rule's question was removed")
	}
}

// WORDS TO A TASK THAT HAS ENDED ARE REFUSED, by the task's say route and by `flow say`, not
// recorded as waiting for a hearing that will never come.
func TestWordsToAnEndedTaskAreRefused(t *testing.T) {
	srv, ts := sayServer(t)
	const alice = "user:alice@example.com"
	spawnSaying(t, ts.URL, alice, "run-done", "mute", false)
	first := sayTo(t, ts.URL, alice, "run-done", "anyone there?")
	waitAsk(t, srv, first, mirror.QuestionOrphaned) // the task has ended

	b, _ := codec.Marshal(api.Say{Instruction: "still there?", By: alice}, codec.Tony)
	resp := postAs(t, alice, ts.URL+"/tasks/run-done/say", "application/tony", string(b))
	if resp.StatusCode != http.StatusConflict {
		var out api.Asked
		_ = client.Decode(resp, &out)
		t.Fatalf("words to an ended task: %s (taken as %q), want 409", resp.Status, out.ID)
	}
	if code, msg := flowAct(t, ts, "say", alice, "", api.FlowSelect{Task: "run-done", Text: "still there?"}); code != http.StatusConflict {
		t.Fatalf("flow say to an ended task: %d %s, want 409", code, msg)
	}
	for _, e := range entity.Below(srv.Store, mirror.IsAsk, 1, entity.VerseRoot, mirror.AskSystem) {
		if entity.Str(e.Payload, "question") == "still there?" && entity.Str(e.Payload, "status") == mirror.QuestionPending {
			t.Fatalf("the refused words stand pending: %v", fmtPayload(e.Payload))
		}
	}
}

// AN ONWARD ASK KEEPS ITS RULES WHEN IT IS CHANGED: its installer's PATCH adds no `then` and moves
// its date no later than the served ask's, as raising it would not.
func TestAnOnwardAskKeepsItsRulesWhenChanged(t *testing.T) {
	srv, ts := shortKeep(t, askKeepDefault)
	const alice = "user:alice@example.com"
	if resp := postAs(t, alice, ts.URL+"/resolvers", "application/tony",
		`{name: helper, match: {}, method: {driver: service}}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s: %s", resp.Status, bodyOf(t, resp))
	}
	if code, msg := grantStatus(t, "user:bob@example.com", ts.URL, "helper", `{}`); code != http.StatusNoContent {
		t.Fatalf("bob's grant: %d %s", code, msg)
	}
	in48 := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	served := askID(t, ask(t, "carol@example.com", ts.URL, `{name: land, question: q, to: [bob@example.com], due: "`+in48+`"}`))
	posed(t, srv, mirror.AskRef(served))
	onward := raiseOnward(t, srv, ts.URL, served, "")
	in72 := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	for body, want := range map[string]string{
		`{then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}`: "takes no `then`",
		`{due: "` + in72 + `"}`: "past ask",
	} {
		resp := change(t, alice, ts.URL, onward, body)
		if msg := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(msg, want) {
			t.Errorf("%s on the onward ask: %s %q, want 400 saying %q", body, resp.Status, msg, want)
		}
	}
}

// AN ASK THAT CAN NO LONGER BE ASKED SAYS SO: carol lost read on the pr the ask is about while
// verse was down, so the restart cannot pose it to her again, and the ask is withdrawn saying why
// rather than standing pending with nobody waiting on it.
func TestAnAskThatCannotBeAskedAgainSaysSo(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, down := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	pr := alicesPR(t, first)
	id := askID(t, ask(t, "user:alice@example.com", ts.URL,
		`{name: land, question: "Land it?", to: [carol@example.com], about: "demo.pr.\"1\""}`))
	posed(t, first, mirror.AskRef(id))
	ts.Close()
	down()
	first.stopAllIOs()

	if err := entity.SetLevel(first.Store, pr, entity.Read, nil); err != nil {
		t.Fatal(err)
	}
	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	t.Cleanup(second.stopAllIOs)
	deadline := time.Now().Add(10 * time.Second)
	for {
		e, _, _ := second.Store.Get(mirror.AskRef(id))
		if s := entity.Str(e.Payload, "status"); s != "" && s != mirror.QuestionPending {
			if !strings.Contains(entity.Str(e.Payload, "why"), "could not be asked") {
				t.Fatalf("the ask settled %s without saying why: %v", s, fmtPayload(e.Payload))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ask stands %v after its asking was refused", fmtPayload(e.Payload))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WHAT FOLLOWS A YES IS NOT LOST FOR WANT OF THE ANSWER FIELD: an ask the restart finds answered,
// with its verdict and no `answer`, still runs its `then`.
func TestAnAnsweredAskWithoutItsAnswerFieldStillFollows(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	ctx, down := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	id := askID(t, ask(t, "user:scott@example.com", ts.URL, `{name: ship, question: q, to: [ann@example.com],
		then: {ask: {name: next, question: "And then?", to: [lead@example.com]}}}`))
	posed(t, first, mirror.AskRef(id))
	ts.Close()
	down()
	first.stopAllIOs()

	sys := entity.As(first.Store, entity.SystemDocket)
	if _, _, err := sys.Write(entity.Write{At: mirror.AskRef(id).At(), Value: entity.Obj{
		"status": mirror.QuestionAnswered, "verdict": task.Proceed,
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	t.Cleanup(second.stopAllIOs)
	asked := entity.ToObj(entity.Get(askField(t, second, id, "then-asked"), "then-asked"))
	if next, _ := asked["id"].(string); !strings.HasPrefix(next, "next-") {
		t.Fatalf("then-asked %v, want the next ask raised", asked)
	}
}
