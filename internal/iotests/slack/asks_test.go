package slack_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	viffapi "go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

const peggy = "user:peggy@example.com"

// tonyDoc is src read as a tony document.
func tonyDoc(t *testing.T, src string) entity.Payload {
	t.Helper()
	n, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// askPeggy is walter asking peggy something with an `on`, as `viff ask` does, and the input a charter
// derives to reach her; it answers the ask's id once it is posed.
func (r *socketRig) askPeggy(t *testing.T) string {
	t.Helper()
	resp, err := client.New(r.addr, "user:walter@example.com").Post("/asks", nil, viffapi.Ask{
		Name: "land", Question: "Merge viff/ask?", To: []string{peggy},
		On: tonyDoc(t, `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge viff/ask to main", environment: {systems: {network: {mode: open}}}}}}]`),
	})
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("asking peggy: %v %v", err, resp)
	}
	var asked viffapi.Asked
	if err := client.Decode(resp, &asked); err != nil {
		t.Fatal(err)
	}
	for range 400 {
		if e, ok, _ := r.store.Get(mirror.AskRef(asked.ID)); ok && entity.Str(e.Payload, "status") == mirror.QuestionPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.ask(t, peggy, "land", entity.Obj{"text": "An ask is waiting on you.", "asks": asked.ID})
	return asked.ID
}

// peggySays is peggy replying in the thread of the DM the out half posted her.
func (r *socketRig) peggySays(t *testing.T, envelope, text string) {
	t.Helper()
	r.event(t, envelope, map[string]any{
		"type": "message", "channel": "DU8", "user": "U8", "text": text,
		"ts": "1790000099.00" + envelope, "thread_ts": r.ts,
	})
}

func (r *socketRig) ask1(t *testing.T, id string) entity.Obj {
	t.Helper()
	e, _, _ := r.store.Get(mirror.AskRef(id))
	return entity.ToObj(e.Payload)
}

// A PERSON'S ASK REACHES THEM IN SLACK, read as them (9ce3bva0): the io reads an ask as whom it is
// for, since an ask is read by whom it asks, and shows who asked and what a yes runs.
func TestAPersonsAskReachesThemReadAsThem(t *testing.T) {
	r := newSocketRig(t)
	id := r.askPeggy(t)
	r.pass(t)
	if len(r.fake.posted) != 1 {
		t.Fatalf("posted %v, want peggy's DM: %v", r.fake.posted, r.status(t, "land"))
	}
	said := shown(t, r.fake.posted[0])
	for _, want := range []string{"Merge viff/ask?", "asked by `walter@example.com`", "on *proceed*, runs as the asker: “merge viff/ask to main”", "Answer in this thread"} {
		if !strings.Contains(said, want) {
			t.Errorf("peggy's DM does not say %q: %q", want, said)
		}
	}
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	r.peggySays(t, "1", "proceed")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("peggy's yes answered nothing: %v", r.ask1(t, id))
	}
	if q := r.ask1(t, id); q["verdict"] != "proceed" || q["by"] != peggy {
		t.Fatalf("answered %v by %v, want peggy's yes", q["verdict"], q["by"])
	}
}

// A REPLY ANSWERS THE WORDS IT SAW (provisional `then`): one to a message drawn before the asker
// changed what a yes runs is refused, and the redrawn message is answered as it now reads.
func TestAReplyToAnAskSinceChangedIsRefused(t *testing.T) {
	r := newSocketRig(t)
	id := r.askPeggy(t)
	r.pass(t)
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	b, _ := codec.Marshal(viffapi.AskChange{On: tonyDoc(t, `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge it and tag it", environment: {systems: {network: {mode: open}}}}}}]`)}, codec.Tony)
	req, _ := http.NewRequest(http.MethodPatch, "http://"+r.addr+"/asks/"+id, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(viffapi.PrincipalHeader, "user:walter@example.com")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("walter's change: %v %v", err, resp)
	}
	r.peggySays(t, "2", "proceed")
	if said := r.heard(t, "U8"); !strings.Contains(said, "Not answered") {
		t.Fatalf("peggy was told %q, want her stale answer refused", said)
	}
	if q := r.ask1(t, id); q["status"] != mirror.QuestionPending {
		t.Fatalf("a reply to the old words settled the ask: %v", q)
	}
	r.pass(t)
	if len(r.fake.updates) == 0 || !strings.Contains(shown(t, r.fake.updates[len(r.fake.updates)-1]), "merge it and tag it") {
		t.Fatalf("the message was not redrawn with the new then: %v", r.fake.updates)
	}
	r.peggySays(t, "3", "proceed")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("peggy's yes to the redrawn ask answered nothing: %v", r.ask1(t, id))
	}
	if q := r.ask1(t, id); q["verdict"] != "proceed" {
		t.Fatalf("answered %v, want peggy's yes", q["verdict"])
	}
}

// A MESSAGE SLACK DID NOT REDRAW STILL ANSWERS ITS OLD WORDS: where the redraw fails, a reply is
// to the words the message still shows, so it is refused rather than counted for the new ones.
func TestAReplyToAMessageNotYetRedrawnIsRefused(t *testing.T) {
	r := newSocketRig(t)
	id := r.askPeggy(t)
	r.pass(t)
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	if code := r.send(t, "user:walter@example.com", "", http.MethodPatch, "/asks/"+id,
		`{on: [{match: {verdict: proceed}, then: {spawn: {prompt: "merge it and tag it", environment: {systems: {network: {mode: open}}}}}}]}`); code != http.StatusOK {
		t.Fatalf("walter's change: %d", code)
	}
	r.fake.fail["chat.update"] = "cant_update_message" // refused, and the message stands with its old words
	r.pass(t)
	r.peggySays(t, "2", "proceed")
	if said := r.heard(t, "U8"); !strings.Contains(said, "Not answered") {
		t.Fatalf("peggy was told %q, want her answer to the words she saw refused", said)
	}
	if q := r.ask1(t, id); q["status"] != mirror.QuestionPending {
		t.Fatalf("a reply to a message Slack never redrew settled the ask: %v", q)
	}
}

// deskForPeggy is a resolver peggy installs and grants to answer for her.
func (r *socketRig) deskForPeggy(t *testing.T) {
	t.Helper()
	if code := r.send(t, peggy, "", http.MethodPost, "/resolvers",
		`{name: desk, match: {}, method: {driver: service}}`); code != http.StatusCreated {
		t.Fatalf("install: %d", code)
	}
	if code := r.send(t, peggy, "", http.MethodPost, "/resolvers/desk/grant", `{}`); code != http.StatusNoContent {
		t.Fatalf("grant: %d", code)
	}
}

// send makes one request of the viff instance as who, through the resolver named where one is.
func (r *socketRig) send(t *testing.T, who, resolver, method, path, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+r.addr+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(viffapi.PrincipalHeader, who)
	if resolver != "" {
		req.Header.Set(viffapi.ResolverHeader, resolver)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// AN ASK PEGGY'S RESOLVER SERVES says so, and peggy answers it herself only by saying `force`.
func TestAnAskYourResolverServesTakesForce(t *testing.T) {
	r := newSocketRig(t)
	r.deskForPeggy(t)
	id := r.askPeggy(t)
	r.pass(t)
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	if said := shown(t, r.fake.posted[0]); !strings.Contains(said, "served for you by `mirror.resolvers.desk`") {
		t.Fatalf("peggy's DM does not say her resolver serves it: %q", said)
	}
	r.peggySays(t, "4", "proceed")
	if said := r.heard(t, "U8"); !strings.Contains(said, "Not answered") {
		t.Fatalf("peggy was told %q, want her answer refused without force", said)
	}
	r.peggySays(t, "5", "force proceed")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("peggy's forced yes answered nothing: %v", r.ask1(t, id))
	}
	if q := r.ask1(t, id); q["verdict"] != "proceed" || q["by"] != peggy {
		t.Fatalf("answered %v by %v, want peggy's own yes", q["verdict"], q["by"])
	}
}

// AN ANSWER THROUGH A RESOLVER is drawn with the resolver it came through.
func TestAnAnswerThroughAResolverSaysSo(t *testing.T) {
	r := newSocketRig(t)
	r.deskForPeggy(t)
	id := r.askPeggy(t)
	r.pass(t)
	if code := r.send(t, peggy, "desk", http.MethodPost, "/asks/"+id+"/answer", `{choice: proceed}`); code != http.StatusNoContent {
		t.Fatalf("the desk's answer: %d", code)
	}
	r.pass(t)
	if len(r.fake.updates) == 0 {
		t.Fatal("the message was not redrawn")
	}
	if said := shown(t, r.fake.updates[len(r.fake.updates)-1]); !strings.Contains(said, "answered *proceed* by `peggy@example.com` via `mirror.resolvers.desk`") {
		t.Fatalf("redrawn as %q, want peggy's answer via the desk", said)
	}
}

// A TURN TAKES WORDS: what is said under a conversation's turn is the reply, words and all.
func TestWordsUnderATurnAreTheReply(t *testing.T) {
	r := newSocketRig(t)
	const turn = "run-7~t1"
	go func() {
		_, _ = r.srv.Reg.Resolve(context.Background(), task.GateQuestion{ID: turn, Origin: "run-7", Gate: "turn",
			Reason:  "your turn",
			Options: []task.Option{{ID: task.Reply, Says: "say something"}, {ID: task.End, Says: "end the conversation"}}},
			task.ToolUse{Name: "turn"})
	}()
	for range 400 {
		if _, ok, _ := r.store.Get(mirror.QuestionRef(turn)); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.ask(t, "C999", "turn", entity.Obj{"text": "your turn", "asks": turn})
	r.pass(t)
	r.ts, _ = r.fake.posted[0]["ts"].(string)
	r.reply(t, "e6", "U7", "look at the flaky test first")
	select {
	case <-r.kicked:
	case <-time.After(5 * time.Second):
		t.Fatalf("the words answered nothing: %v", r.ask1(t, turn))
	}
	q := r.ask1(t, turn)
	if q["verdict"] != task.Reply || !strings.Contains(strings.Join(entity.Strs(entity.Obj(q).Payload(), "say"), " "), "flaky test") {
		t.Fatalf("the turn settled %v saying %v, want the reply with its words", q["verdict"], q["say"])
	}
}

// WHAT A YES AND A NO RUN, in the DM: a then that answers another ask and an else that asks the
// lead, said so the person answering knows what each outcome sets going.
func TestADMSaysWhatAYesAndANoRun(t *testing.T) {
	r := newSocketRig(t)
	post := func(as string, body viffapi.Ask) string {
		t.Helper()
		resp, err := client.New(r.addr, as).Post("/asks", nil, body)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("asking: %v %v", err, resp)
		}
		var asked viffapi.Asked
		if err := client.Decode(resp, &asked); err != nil {
			t.Fatal(err)
		}
		for range 400 {
			if e, ok, _ := r.store.Get(mirror.AskRef(asked.ID)); ok && entity.Str(e.Payload, "status") == mirror.QuestionPending {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return asked.ID
	}
	ship := post("user:alice@example.com", viffapi.Ask{Name: "ship", Question: "Ship it?", To: []string{"user:walter@example.com"}})
	id := post("user:walter@example.com", viffapi.Ask{
		Name: "ui-ok", Question: "Is the UI ok?", To: []string{peggy},
		On: tonyDoc(t, `[{match: {verdict: proceed}, then: {answer: {ask: `+ship+`, choice: proceed}}},
			{then: {ask: {name: ship-lead, question: "Peggy said no: ship it?", to: [lead@example.com]}}}]`),
	})
	r.ask(t, peggy, "ui-ok", entity.Obj{"text": "An ask is waiting on you.", "asks": id})
	r.pass(t)
	if len(r.fake.posted) != 1 {
		t.Fatalf("posted %v, want peggy's DM: %v", r.fake.posted, r.status(t, "ui-ok"))
	}
	said := shown(t, r.fake.posted[0])
	for _, want := range []string{
		"on *proceed*, runs as the asker: answers `" + ship + "` *proceed*",
		"on any answer, runs as the asker: asks “Peggy said no: ship it?” of `lead@example.com`",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("peggy's DM does not say %q: %q", want, said)
		}
	}
}
