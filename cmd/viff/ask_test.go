package main

import (
	"errors"
	"fmt"
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott-cotton/cli"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
)

const (
	victor = "user:victor@example.com"
	oscar  = "user:oscar@example.com"
)

// serveAsking is a started viff, which asks as every viff instance does: viffd poses what stands under
// mirror.ask.
func serveAsking(t *testing.T) entity.Store {
	t.Helper()
	store, _ := serveAskingAt(t)
	return store
}

// serveAskingAt is serveAsking, and the server it answers at, for a test that ends its streams.
func serveAskingAt(t *testing.T) (entity.Store, *httptest.Server) {
	t.Helper()
	store := entitytest.Store(t)
	srv := viffdtest.Server(t, runnerdtest.Scripted, store)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(ts.CloseClientConnections)
	testAddr = strings.TrimPrefix(ts.URL, "http://")
	return store, ts
}

// saidLines counts the lines a watch said exactly, whatever revision each was said at.
func saidLines(out, want string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if _, said, ok := strings.Cut(line, " "); ok && strings.HasPrefix(line, "@") && said == want {
			n++
		}
	}
	return n
}

// as makes the CLI act as who, for the rest of the test or until the next call: the config is
// read again each time a command is built (see TestMain).
func as(t *testing.T, who string) {
	t.Helper()
	t.Setenv("VIFF_USER", who)
}

// askAs asks as who, and answers the id the ask was written under.
func askAs(t *testing.T, who string, args ...string) string {
	t.Helper()
	as(t, who)
	out, err := runCLI(t, append([]string{"ask"}, args...)...)
	if err != nil {
		t.Fatalf("ask %v: %v\n%s", args, err, out)
	}
	id, _, ok := strings.Cut(strings.TrimPrefix(out, "raised question "), ":")
	if !ok {
		t.Fatalf("ask said %q", out)
	}
	return id
}

// questionOn waits for an ask to be pending on the docket, and answers its id: the ask is the
// question.
func questionOn(t *testing.T, store entity.Store, ask entity.Ref) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if payloadOf(t, store, ask)["status"] == mirror.QuestionPending {
			return ask.Name()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never posed", ask)
	return ""
}

// atPathCLI is the value at a dotted path of fields in m, nil where any is absent: `ran.task`.
func atPathCLI(m map[string]any, path string) any {
	var v any = m
	for _, k := range strings.Split(path, ".") {
		mm, _ := v.(map[string]any)
		v = mm[k]
	}
	return v
}

func waitFieldCLI(t *testing.T, store entity.Store, ref entity.Ref, field string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if atPathCLI(payloadOf(t, store, ref), field) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never carried %s", ref, field)
}

// ASKING, END TO END: victor asks oscar by name, sees the ask pending and waiting on oscar,
// oscar answers through `viff gate`, and victor sees the answer.
func TestAnAskIsAskedAnsweredAndSeen(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review viff/ask?", "--to", "oscar@example.com",
		"--due", "48h", "--on-silence", "proceed", "--on", `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge viff/ask", environment: {systems: {network: {mode: open}}}}}}]`, "--keep", "72h")
	ref := mirror.AskRef(id)
	rec := payloadOf(t, store, ref)
	if rec["onSilence"] != "proceed" {
		t.Fatalf("onSilence = %v, want --on-silence proceed to settle silence on proceed", rec["onSilence"])
	}
	// What silence settles on is said when the ask is asked, and not changed after.
	if _, err := runCLI(t, "ask", "land", "--on-silence", "deny"); err == nil || !strings.Contains(err.Error(), "go with --question") {
		t.Fatalf("--on-silence on a standing ask: %v, want it refused as going with --question", err)
	}
	if got := mirror.GranteesTo(entity.Obj(rec).Payload()); len(got) != 1 || got[0] != oscar {
		t.Fatalf("to = %v, want [%s] in the grammar", rec[mirror.ToField], oscar)
	}
	if rec["keep"] != "72h0m0s" {
		t.Fatalf("keep = %v, want 72h", rec["keep"])
	}
	due, err := time.Parse(time.RFC3339, entity.Str(entity.Val(rec), "due"))
	if err != nil || due.Before(time.Now().Add(47*time.Hour)) {
		t.Fatalf("due = %v (%v), want a date 48h out", rec["due"], err)
	}
	qid := questionOn(t, store, ref)

	if out, err := runCLI(t, "ask"); err != nil || !strings.Contains(out, id) || !strings.Contains(out, "pending") {
		t.Fatalf("the listing: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "ask", "land"); err != nil || !strings.Contains(out, qid) || !strings.Contains(out, oscar) {
		t.Fatalf("the ask does not say its question and whom it waits on: %v\n%s", err, out)
	}

	as(t, oscar)
	if out, err := runCLI(t, "gate", qid, "--answer", "proceed"); err != nil {
		t.Fatalf("oscar's answer: %v\n%s", err, out)
	}
	as(t, victor)
	waitFieldCLI(t, store, ref, "answer")
	if out, err := runCLI(t, "ask", id); err != nil || !strings.Contains(out, "answer: yes") {
		t.Fatalf("the answer is not on the ask: %v\n%s", err, out)
	}
}

// THE WATCH, FROM A CURSOR: run after the fact from revision 0, it says each event of victor's ask
// in order; oscar's says the question that waited on him, and that it settled.
func TestTheWatchSaysWhatHappenedSinceItsCursor(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review viff/ask?", "--to", "oscar@example.com")
	qid := questionOn(t, store, mirror.AskRef(id))
	as(t, oscar)
	if out, err := runCLI(t, "gate", qid, "--answer", "proceed"); err != nil {
		t.Fatalf("answer: %v\n%s", err, out)
	}
	waitFieldCLI(t, store, mirror.AskRef(id), "answer")

	his, _ := runCLIIn(t, strings.NewReader(""), "questions", "--watch", "--since", "0")
	waitFor(t, his, "waiting on you "+qid+": Review viff/ask?")
	waitFor(t, his, "settled "+qid+": answered")
	if strings.Contains(his.String(), "raised question "+id) {
		t.Fatalf("oscar's watch tells him of victor's ask as his own:\n%s", his.String())
	}

	// oscar's own watch of asks says his asks, and not the question put to him: that is his
	// question watch's. It is read once it says an ask of his raised after.
	as(t, oscar)
	own := askAs(t, oscar, "tea", "-q", "Tea?")
	// An ask he names nothing is raised all the same, under the name viff gives it.
	if anon := askAs(t, oscar, "-q", "Coffee?"); !strings.HasPrefix(anon, "run-") {
		t.Fatalf("an ask named nothing was raised as %q, want viff's run-<nonce>", anon)
	}
	as(t, oscar)
	asks, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch", "--since", "0")
	waitFor(t, asks, "raised question "+own+": Tea?")
	if strings.Contains(asks.String(), "waiting on you") {
		t.Fatalf("oscar's ask watch says a question waits on him:\n%s", asks.String())
	}

	as(t, victor)
	mine, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch", "--since", "0")
	waitFor(t, mine, "raised question "+id+": Review viff/ask?")
	waitFor(t, mine, "answered "+id+" yes: Review viff/ask?")
	if strings.Contains(mine.String(), "waiting on you") {
		t.Fatalf("victor's watch says a question waits on him:\n%s", mine.String())
	}
}

// A VOTE WAITS ON EVERYBODY: an ask naming nobody waits on `user:*`, as a conversation's turn and
// a task's own question do, and a person's watch says it waits on them. Its asker's does not: they
// do not answer their own ask.
func TestTheWatchSaysAnAskForAnyoneWaitsOnYou(t *testing.T) {
	serveAsking(t)
	id := askAs(t, oscar, "lunch", "-q", "Lunch at noon?")
	as(t, victor)
	his, _ := runCLIIn(t, strings.NewReader(""), "questions", "--watch", "--since", "0")
	waitFor(t, his, "waiting on you "+id+": Lunch at noon?")

	// oscar's watch is read once it says a later question put to him by name, so a line about
	// his own vote would be there before it.
	tea := askAs(t, victor, "tea", "-q", "Tea?", "--to", "oscar@example.com")
	as(t, oscar)
	mine, _ := runCLIIn(t, strings.NewReader(""), "questions", "--watch", "--since", "0")
	waitFor(t, mine, "waiting on you "+tea+": Tea?")
	if strings.Contains(mine.String(), id) {
		t.Fatalf("oscar's watch says his own vote waits on him:\n%s", mine.String())
	}
}

// WHOM A WILDCARD REACHES: every principal of its kind, or everyone, and never the asker of the
// ask; a principal is matched as written.
func TestWhomAnAskWaitsOn(t *testing.T) {
	const me, other = "user:victor@example.com", "user:oscar@example.com"
	w := &askWatch{me: me}
	for _, c := range []struct {
		waiting       []any
		asker         string
		want, decided bool
	}{
		{[]any{me}, "", true, true},
		{[]any{other, me}, "", true, true},
		{[]any{"user:*", "agent:*"}, "", true, true},
		{[]any{"*"}, "", true, true},
		{[]any{"agent:*"}, "", false, true},
		{[]any{other}, "", false, true},
		{[]any{"user:*"}, me, false, true},
		{[]any{me}, me, true, true},
		// Who may write a place is the viff instance's to say.
		{[]any{"audience:demo.pr"}, "", false, false},
		{[]any{"audience:demo.pr", "user:*"}, "", true, true},
	} {
		q := entity.Obj{"waitingOn": c.waiting}
		if c.asker != "" {
			q[mirror.FromField] = mirror.FromPrincipal(c.asker)
		}
		if got, decided := w.waitsOnMe(q.Payload()); got != c.want || decided != c.decided {
			t.Errorf("waitingOn %v, asker %q: waits on victor %v (decided %v), want %v (%v)",
				c.waiting, c.asker, got, decided, c.want, c.decided)
		}
	}
}

// THE QUESTIONS PUT TO YOU are what `viff question` lists: victor's ask to oscar is on his docket
// (`--all`) and not among his questions, and oscar finds it among his under each of the
// command's names.
func TestTheQuestionListingIsWhatWaitsOnYou(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com")
	questionOn(t, store, mirror.AskRef(id))
	if out, err := runCLI(t, "question"); err != nil || strings.Contains(out, id) {
		t.Fatalf("victor's questions list his own ask: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "question", "--all"); err != nil || !strings.Contains(out, id) {
		t.Fatalf("victor's docket does not list his own ask: %v\n%s", err, out)
	}
	as(t, oscar)
	for _, name := range []string{"question", "questions", "qs", "gate"} {
		if out, err := runCLI(t, name); err != nil || !strings.Contains(out, id) {
			t.Fatalf("viff %s as oscar does not list the ask put to him: %v\n%s", name, err, out)
		}
	}
}

// WHAT THE WATCH CANNOT READ, IT ASKS: a question waiting on whoever may write a place is said to
// wait on you when the viff instance says it does.
func TestTheQuestionWatchAsksTheViffWhatItCannotRead(t *testing.T) {
	var asked string
	w := newAskWatch(apiClient(), victor, false, true)
	w.askViff = func(id string) bool { asked = id; return true }
	var said []string
	w.frame(askFrame{at: "mirror.ask.q1", rev: "5", to: entity.Obj{
		"status": "pending", "waitingOn": []any{"audience:demo.pr"}, "pose": "Land it?",
	}.Payload()}, func(rev, line string) { said = append(said, line) })
	if asked != "q1" || len(said) != 1 || !strings.HasPrefix(said[0], "waiting on you q1: Land it?") {
		t.Fatalf("asked the viff instance about %q, and said %q", asked, said)
	}
}

// YOUR ASKS are the ones you asked: oscar's vote, which anyone may read, is not in victor's list.
func TestTheListingIsYourAsks(t *testing.T) {
	serveAsking(t)
	askAs(t, oscar, "lunch", "-q", "Lunch at noon?")
	land := askAs(t, victor, "land", "-q", "Review?")
	out, err := runCLI(t, "ask")
	if err != nil || !strings.Contains(out, land) || strings.Contains(out, "lunch") {
		t.Fatalf("victor's listing: %v\n%s", err, out)
	}
	as(t, "user:nobody@example.com")
	if out, err = runCLI(t, "ask"); err != nil || !strings.Contains(out, "no asks standing") {
		t.Fatalf("a listing with none: %v\n%s", err, out)
	}
}

// A NAME NAMES ONE ASK OR NONE: two asks under one name are shown by id, and the name is refused
// with both ids rather than guessed between.
func TestANameThatNamesTwoAsksIsRefused(t *testing.T) {
	serveAsking(t)
	first := askAs(t, victor, "land", "-q", "first?")
	second := askAs(t, victor, "land", "-q", "second?")
	_, err := runCLI(t, "ask", "land")
	if err == nil || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("an ambiguous name: %v", err)
	}
	if out, err := runCLI(t, "ask", second); err != nil || !strings.Contains(out, "second?") {
		t.Fatalf("by id: %v\n%s", err, out)
	}
}

// CHECKED WHERE IT IS ASKED: a name outside the grammar, a date in the past, and a then that is not
// one key an ask runs are refused before anything stands.
func TestAnAskIsCheckedBeforeItIsWritten(t *testing.T) {
	store := serveAsking(t)
	as(t, victor)
	for _, args := range [][]string{
		{"ask", "land", "-q", "x?", "--to", "oscar"},
		{"ask", "land", "-q", "x?", "--due", "-1h"},
		{"ask", "land", "-q", "x?", "--due", "2020-01-01T00:00:00Z"},
		{"ask", "land", "-q", "x?", "--due", "tomorrow"},
		{"ask", "land", "-q", "x?", "--on", "merge it"},
		{"ask", "land", "-q", "x?", "--on", `[{match: {verdict: proceed}, then: {spawn: {prompt: a, script: b}}}]`},
		{"ask", "land", "-q", "x?", "--on", `[{match: {verdict: proceed}, then: {shell: "rm -rf /"}}]`},
		{"ask", "land", "-q", "x?", "--on", `[{match: {verdict: proceed}, then: {prompt: "merge it"}}]`},
		{"ask", "land", "-q", "x?", "--on", `[{match: {verdict: proceed}, then: {spawn: {script: "echo hi", environment: {systems: {git: {repo: $[subject.value.repo], fetch: {}}}}}}}]`},
		{"ask", "-q", "x?", "--withdraw"},
		{"ask", "--to", "oscar@example.com"},
		{"ask", "land", "--to", "oscar@example.com"},
		{"ask", "land", "-q", "x?", "--on-silence", "proceed"},
	} {
		if _, err := runCLI(t, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if n := len(entity.Below(store, mirror.IsAsk, 2, entity.ViffRoot, mirror.AskSystem)); n != 0 {
		t.Fatalf("%d refused asks were written", n)
	}
}

// CHANGED WHILE IT WAITS, by its asker: --due moves its date and asks nothing again, --on asks
// it again as its next edit, and --about, which says what a new ask is, is refused.
func TestAWaitingAskIsChangedByItsAsker(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com", "--due", "1h", "--on", `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge it", environment: {systems: {network: {mode: open}}}}}}]`)
	ref := mirror.AskRef(id)
	questionOn(t, store, ref)
	if out, err := runCLI(t, "ask", "land", "--due", "72h"); err != nil || !strings.Contains(out, "moved "+id) {
		t.Fatalf("moving the date: %v\n%s", err, out)
	}
	due, _ := time.Parse(time.RFC3339, entity.Str(entity.Val(payloadOf(t, store, ref)), "due"))
	if due.Before(time.Now().Add(71 * time.Hour)) {
		t.Fatalf("due = %v, want 72h out", due)
	}
	if got := mirror.EditOf(entity.Val(payloadOf(t, store, ref))); got != 1 {
		t.Fatalf("moving the date asked again: edit %d", got)
	}
	out, err := runCLI(t, "ask", "land", "--on", `[{match: {verdict: proceed}, then: {spawn: {prompt: "merge it and tag it", environment: {systems: {network: {mode: open}}}}}}]`)
	if err != nil || !strings.Contains(out, "as edit 2") {
		t.Fatalf("changing then: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "ask", "land", "--about", "demo.pr.1"); err == nil || !strings.Contains(err.Error(), "say what a new ask is") {
		t.Fatalf("--about on a standing ask: %v", err)
	}
	watch, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch", "--since", "0")
	waitFor(t, watch, "asked again "+id+", as edit 2")
}

// WITHDRAWING removes the ask, and its question goes with it.
func TestAWithdrawnAskIsGoneAndAsksNobody(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com")
	ref := mirror.AskRef(id)
	qid := questionOn(t, store, ref)
	if out, err := runCLI(t, "ask", "land", "--withdraw"); err != nil {
		t.Fatalf("withdraw: %v\n%s", err, out)
	}
	if payloadOf(t, store, ref) != nil {
		t.Fatal("the ask stands")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		q := payloadOf(t, store, mirror.QuestionRef(qid))
		if q == nil || q["status"] != mirror.QuestionPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("its question still asks: %v", q)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := runCLI(t, "ask", "land", "--withdraw"); err == nil || !strings.Contains(err.Error(), "no ask named") {
		t.Fatalf("withdrawing an ask that is not there: %v", err)
	}
}

// A THEN OF CALLS RUNS AS A CALL LIST: a yes runs what it lists, as the asker's task, and its
// result is what the calls did.
func TestACallsThenRunsItsCalls(t *testing.T) {
	store := serveCalls(t)
	id := askAs(t, victor, "land", "-q", "Tag it?", "--to", "oscar@example.com",
		"--on", `[{match: {verdict: proceed}, then: {spawn: {script: "echo tagged", environment: {systems: {network: {mode: open}}}}}}]`)
	ref := mirror.AskRef(id)
	questionOn(t, store, ref)
	as(t, oscar)
	if out, err := runCLI(t, "gate", id, "--answer", "proceed"); err != nil {
		t.Fatalf("oscar's answer: %v\n%s", err, out)
	}
	waitFieldCLI(t, store, ref, "ran.task")
	task := mirror.Ref(atPathCLI(payloadOf(t, store, ref), "ran.task").(string))
	deadline := time.Now().Add(10 * time.Second)
	for {
		p := payloadOf(t, store, task)
		if p["status"] == "done" {
			if !strings.Contains(fmt.Sprint(p), "tagged") {
				t.Fatalf("the calls ran and said nothing of them: %v", p)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the then never finished: %v", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A WATCH OPENED BEFORE sees whole records: an ask standing when it opened is known as victor's
// when it is answered, and an answer is said once however much is written on the ask after it (its
// status, its expiry). The watch is known to be open once it says an ask asked after it.
// A WATCH FROM A CURSOR SAYS WHAT HAPPENS AFTER ITS HISTORY: the ask stood before the watch, its
// asking is in the history from 0, and its answer comes live, after the history ends. The live
// part carries the whole ask, so the answer is said as victor's.
func TestAWatchFromACursorSaysWhatHappensAfterItsHistory(t *testing.T) {
	store := serveAsking(t)
	id := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com")
	qid := questionOn(t, store, mirror.AskRef(id))
	as(t, victor)
	mine, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch", "--since", "0")
	waitFor(t, mine, "raised question "+id+": Review?")

	as(t, oscar)
	if out, err := runCLI(t, "question", qid, "--answer", "proceed"); err != nil {
		t.Fatalf("oscar's answer: %v\n%s", err, out)
	}
	waitFor(t, mine, "answered "+id+" yes: Review?")
}

func TestTheWatchSaysAnAskStandingBeforeItAnsweredOnce(t *testing.T) {
	store := serveAsking(t)
	before := askAs(t, victor, "land", "-q", "Review?", "--to", "oscar@example.com")
	qBefore := questionOn(t, store, mirror.AskRef(before))
	as(t, victor)
	mine, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch")
	after := askAs(t, victor, "tag", "-q", "Tag?", "--to", "oscar@example.com")
	waitFor(t, mine, "raised question "+after+": Tag?")
	qAfter := questionOn(t, store, mirror.AskRef(after))

	as(t, oscar)
	for _, q := range []string{qBefore, qAfter} {
		if out, err := runCLI(t, "gate", q, "--answer", "proceed"); err != nil {
			t.Fatalf("answer %s: %v\n%s", q, err, out)
		}
	}
	waitFor(t, mine, "answered "+before+" yes: Review?")
	waitFor(t, mine, "answered "+after+" yes: Tag?")
	// Every write after the answer is in the stream before an ask asked after them.
	waitFieldCLI(t, store, mirror.AskRef(before), "expires")
	waitFieldCLI(t, store, mirror.AskRef(after), "expires")
	last := askAs(t, victor, "last", "-q", "Last?")
	waitFor(t, mine, "raised question "+last+": Last?")

	out := mine.String()
	for _, line := range []string{"answered " + before + " yes: Review?", "answered " + after + " yes: Tag?", "raised question " + after + ": Tag?"} {
		if n := saidLines(out, line); n != 1 {
			t.Errorf("%q said %d times, want once:\n%s", line, n, out)
		}
	}
	// It stood when the watch opened, so its asking is not news.
	if n := saidLines(out, "raised question "+before+": Review?"); n != 0 {
		t.Errorf("the ask standing before the watch was said asked:\n%s", out)
	}
}

// AN ASK THAT GOES AT ITS EXPIRY IS EXPIRED, and one taken back while it waits is withdrawn.
func TestTheWatchSaysAnExpiredAskExpired(t *testing.T) {
	store := serveAsking(t)
	as(t, victor)
	mine, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch")
	pending := askAs(t, victor, "later", "-q", "Later?", "--to", "oscar@example.com")
	waitFor(t, mine, "raised question "+pending+": Later?")
	brief := askAs(t, victor, "brief", "-q", "Brief?", "--to", "oscar@example.com", "--keep", "1s")
	q := questionOn(t, store, mirror.AskRef(brief))
	as(t, oscar)
	if out, err := runCLI(t, "gate", q, "--answer", "proceed"); err != nil {
		t.Fatalf("answer: %v\n%s", err, out)
	}
	as(t, victor)
	waitFor(t, mine, "expired "+brief)
	if out, err := runCLI(t, "ask", pending, "--withdraw"); err != nil {
		t.Fatalf("withdraw: %v\n%s", err, out)
	}
	waitFor(t, mine, "withdrawn "+pending)
	out := mine.String()
	if saidLines(out, "withdrawn "+brief) != 0 || saidLines(out, "expired "+pending) != 0 {
		t.Fatalf("an expiry and a withdrawal told apart wrongly:\n%s", out)
	}
}

// A STREAM THAT ENDS IS SAID, and followed again from the revision it last carried: nothing is
// said twice, and what is asked after is said.
func TestTheWatchFollowsAgainWhenItsStreamEnds(t *testing.T) {
	_, ts := serveAskingAt(t)
	as(t, victor)
	mine, _ := runCLIIn(t, strings.NewReader(""), "ask", "--watch")
	first := askAs(t, victor, "first", "-q", "First?")
	waitFor(t, mine, "raised question "+first+": First?")
	ts.CloseClientConnections()
	waitFor(t, mine, "the stream ended")
	second := askAs(t, victor, "second", "-q", "Second?")
	waitFor(t, mine, "raised question "+second+": Second?")
	if n := saidLines(mine.String(), "raised question "+first+": First?"); n != 1 {
		t.Fatalf("the ask before the stream ended said %d times:\n%s", n, mine.String())
	}
}

// A GAP IS SAID, with the revision to replay it from: the last the watch met.
func TestTheWatchSaysAGap(t *testing.T) {
	w := newAskWatch(apiClient(), victor, true, false)
	var said []string
	say := func(rev, line string) { said = append(said, "@"+rev+" "+line) }
	w.frame(askFrame{at: "mirror.ask.a1", op: "set", rev: "5", to: entity.Obj{mirror.FromField: mirror.FromPrincipal(oscar)}.Payload()}, say)
	w.frame(askFrame{rev: "9", gap: 2}, say)
	if len(said) != 1 || said[0] != "@9 gap: 2 commits were not delivered; replay them with --since 5" {
		t.Fatalf("said %q", said)
	}
}

// --served WIDENS THE LISTING and only that, and --watch follows what waits on you: each with what
// it cannot mean is refused rather than ignored, a flag after an id as one before it.
func TestTheQuestionFlagsThatMeanNothingTogetherAreRefused(t *testing.T) {
	as(t, victor)
	for _, args := range [][]string{
		{"question", "--served", "--all"},
		{"question", "--served", "--watch"},
		{"question", "--all", "--watch"},
		{"question", "--served", "q1"},
		{"question", "q1", "--served"},
		{"question", "q1", "--watch"},
		{"question", "q1", "--since", "3"},
		{"question", "--notify"},
		{"question", "q1", "--notify"},
		{"ask", "--notify"},
	} {
		if _, err := runCLI(t, args...); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
}
