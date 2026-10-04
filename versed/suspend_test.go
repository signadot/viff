package versed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// suspendAs posts `flow suspend` or `flow resume` as a principal, in scope when one is named, and
// hands back the status, the row when there is one, and the body's text.
func suspendAs(t *testing.T, ts *httptest.Server, verb, as, scope string, sel api.FlowSelect) (int, api.FlowRow, string) {
	t.Helper()
	code, msg := flowAct(t, ts, verb, as, scope, sel)
	var out api.FlowRow
	if code == http.StatusOK {
		if err := codec.Unmarshal([]byte(msg), &out, codec.Tony); err != nil {
			t.Fatalf("decoding %s: %v\n%s", verb, err, msg)
		}
	}
	return code, out, msg
}

// suspensionsIn lists the suspensions in force in a scope: the listing's suspension rows.
func suspensionsIn(t *testing.T, ts *httptest.Server, scope string) []api.FlowRow {
	t.Helper()
	var out []api.FlowRow
	for _, row := range flowRows(t, ts, scope, api.FlowSelect{}) {
		if row.Kind == api.FlowRowSuspension {
			out = append(out, row)
		}
	}
	return out
}

// startedServer is a server that has run its restore, serving.
func startedServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

// The issue's three shapes are three records, keyed by what they cover: the same shape held
// twice is one record with two holders, and two spellings of one subject are one shape.
func TestASuspensionIsOneRecordPerThingItCovers(t *testing.T) {
	srv, ts := startedServer(t)
	// Alice's charter and the places she and bob both watch: a suspension is its holder's say
	// over a place they hold `control` at, so the places are somebody's, with bob let in.
	owns(t, srv, "user:alice@example.com",
		[]entity.Grant{{To: map[entity.Level][]string{entity.Control: {"user:bob@example.com"}, entity.Read: {entity.Everyone}}}},
		entity.RefAt("verse-dev"), entity.RefAt("demo"), entity.RefAt("git"))

	for _, c := range []struct {
		as   string
		body api.FlowSelect
		want string
	}{
		{"alice@example.com", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*", Why: "reading its range logic"}, "verse-dev.drift:*@*"},
		{"alice@example.com", api.FlowSelect{Charter: "*", Subject: `demo.pr."1"`, Why: "a person is reviewing it"}, `*:*@demo.pr."1"`},
		{"bob@example.com", api.FlowSelect{Charter: "*", Subject: "demo.pr.1", Why: "and so am I"}, `*:*@demo.pr."1"`},
		{"alice@example.com", api.FlowSelect{Charter: "verse-dev.drift:claim", Subject: "git.verse.head", Why: "the claim is wrong"}, "verse-dev.drift:claim@git.verse.head"},
	} {
		code, got, msg := suspendAs(t, ts, "suspend", c.as, "", c.body)
		if code != http.StatusOK || got.ID != c.want {
			t.Fatalf("suspend %+v as %s = %d %+v %s, want %s", c.body, c.as, code, got, msg, c.want)
		}
	}

	listed := suspensionsIn(t, ts, "")
	if len(listed) != 3 {
		t.Fatalf("suspensions = %+v, want three records", listed)
	}
	pr := `*:*@demo.pr."1"`
	rec, ok, err := srv.Store.Get(trigger.SuspensionRef(pr))
	if err != nil || !ok {
		t.Fatalf("no record for %s (ok=%v err=%v)", pr, ok, err)
	}
	sus, err := trigger.SuspensionOf(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := sus.Holders(); len(got) != 2 || got[0] != "user:alice@example.com" || got[1] != "user:bob@example.com" {
		t.Fatalf("%s is held by %v, want alice and bob", pr, got)
	}
	if h := sus.Held["user:bob@example.com"]; h.Why != "and so am I" || h.Since == "" || h.Occurrence == 0 {
		t.Fatalf("bob's hold = %+v, want his why, a since and the revision it landed at", h)
	}
}

// A resume by a person lifts a suspension whoever held it; the system's resume removes only the
// system's own hold, so an automatic suspension lifting itself never lifts a person's.
func TestASystemResumeLiftsOnlyTheSystemsHold(t *testing.T) {
	srv, ts := startedServer(t)
	const throttle = "system:throttle"
	sus, err := trigger.NewSuspension("", "loop", "*", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.hold("", sus, throttle, "the cycle throttle tripped"); err != nil {
		t.Fatal(err)
	}
	// Her rule, as an install leaves it, with bob let in: a person's resume lifts what another
	// person holds, where both hold `control` over what it covers.
	owns(t, srv, "user:alice@example.com",
		[]entity.Grant{{To: map[entity.Level][]string{entity.Control: {"user:bob@example.com"}, entity.Read: {entity.Everyone}}}},
		trigger.CharterRef("loop"))
	if code, _, msg := suspendAs(t, ts, "suspend", "alice@example.com", "", api.FlowSelect{Charter: ":loop", Subject: "*", Why: "and I want to read it"}); code != http.StatusOK {
		t.Fatalf("alice's suspend = %d %s", code, msg)
	}
	rest, lifted, err := srv.release("", sus.ID(), throttle)
	if err != nil || lifted {
		t.Fatalf("the system's release = lifted %v, err %v; want its hold removed and the suspension standing", lifted, err)
	}
	if got := rest.Holders(); len(got) != 1 || got[0] != "user:alice@example.com" {
		t.Fatalf("after the system's release it is held by %v, want alice alone", got)
	}
	if _, ok, _ := srv.Store.Get(trigger.SuspensionRef(sus.ID())); !ok {
		t.Fatal("the system's release deleted a record a person still holds")
	}
	code, got, msg := suspendAs(t, ts, "resume", "bob@example.com", "", api.FlowSelect{Charter: ":loop", Subject: "*"})
	if code != http.StatusOK || got.ID != sus.ID() {
		t.Fatalf("bob's resume = %d %+v %s, want the suspension lifted", code, got, msg)
	}
	if _, ok, _ := srv.Store.Get(trigger.SuspensionRef(sus.ID())); ok {
		t.Fatal("a person's resume left the record standing")
	}
	if len(suspensionsIn(t, ts, "")) != 0 {
		t.Fatal("a person's resume left the suspension in the table")
	}
}

// A suspension belongs to the scope it was made in: a scope's is refused from baseline and
// baseline's from a scope, with the sentence every control write refuses in, and a scope may not
// name baseline's charter at all.
func TestASuspensionBelongsToItsScope(t *testing.T) {
	srv, ts := startedServer(t)
	testerOwns(t, srv, entity.RefAt("demo"))

	if code, _, msg := suspendAs(t, ts, "suspend", "", "me", api.FlowSelect{Charter: "*", Subject: "demo.pr.1", Why: "mine"}); code != http.StatusOK {
		t.Fatalf("suspend --scope me = %d %s", code, msg)
	}
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "demo.pr.2", Why: "baseline's"}); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	if got := suspensionsIn(t, ts, "me"); len(got) != 1 || got[0].Scope != "me" {
		t.Fatalf("scope me's suspensions = %+v, want its one, stamped with the scope", got)
	}
	if got := suspensionsIn(t, ts, ""); len(got) != 1 || got[0].Subject != `demo.pr."2"` {
		t.Fatalf("baseline's suspensions = %+v, want its one and not the scope's", got)
	}

	refusesNaming := func(code int, msg, whose, redo, what string) {
		t.Helper()
		if code != http.StatusConflict || !strings.Contains(msg, whose) || !strings.Contains(msg, redo) {
			t.Fatalf("%s = %d %q; want 409 naming %s and saying %q", what, code, msg, whose, redo)
		}
	}
	code, _, msg := suspendAs(t, ts, "resume", "", "", api.FlowSelect{Charter: "*", Subject: "demo.pr.1"})
	refusesNaming(code, msg, "scope me's", "--scope me", "resume from baseline of the scope's")
	code, _, msg = suspendAs(t, ts, "resume", "", "me", api.FlowSelect{Charter: "*", Subject: "demo.pr.2"})
	refusesNaming(code, msg, "baseline's", "without --scope", "resume --scope me of baseline's")
	code, _, msg = suspendAs(t, ts, "suspend", "", "me", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*", Why: "not mine"})
	refusesNaming(code, msg, "baseline's", "without --scope", "suspend --scope me of a package")

	if _, ok, _ := srv.Store.Scope("me").Get(trigger.SuspensionRef(`*:*@demo.pr."1"`)); !ok {
		t.Fatal("the scope's record is not in the scope")
	}
	if _, ok, _ := srv.Store.Get(trigger.SuspensionRef(`*:*@demo.pr."1"`)); ok {
		t.Fatal("the scope's record leaked into baseline")
	}
}

// A suspension says what it is for, names something somebody could stand behind, and is made
// only by the route: not by a raw put, and not by a rule's derive.
func TestASuspensionIsMadeOnlyByTheRouteAndSaysWhy(t *testing.T) {
	_, ts := startedServer(t)

	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*"}); code != http.StatusBadRequest || !strings.Contains(msg, "why") {
		t.Fatalf("suspend with no why = %d %s, want 400 asking for it", code, msg)
	}
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "*", Why: "everything"}); code != http.StatusBadRequest {
		t.Fatalf("suspend of every charter about every subject = %d %s, want 400", code, msg)
	}
	resp := putAs(t, "", ts.URL, "verse.trigger.suspension.x", entity.Obj{"charter": "*"}.Payload())
	body := readBody(t, resp)
	if resp.StatusCode/100 == 2 {
		t.Fatalf("a raw put of a suspension record = %s %s, want it refused", resp.Status, body)
	}
	resp, err := httpPost(ts.URL+"/charter", "application/tony", strings.NewReader(`{
		name: suspender, stratum: prs,
		condition: {subject: {path: "demo.pr.*"}},
		action: {derive: [{path: "verse.trigger.suspension", value: {charter: "*"}}]},
	}`))
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "suspension") {
		t.Fatalf("a rule deriving a suspension = %s %s, want it refused at install", resp.Status, body)
	}
	if !strings.Contains(body, "verse flow suspend") {
		t.Fatalf("the refusal = %s, want it to name verse flow suspend", body)
	}
}

// The table is loaded before anything it could hold is re-attached or restored: from the moment a
// restore begins until its first phase has read the records, the table has not loaded, a suspend
// is refused rather than written over records the table does not know, and the phases that follow
// wait for it. A restart brings every suspension back.
func TestSuspensionsLoadBeforeTheRestore(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	first.susp.Unload() // where a restore begins
	testerOwns(t, first, entity.RefAt("verse-dev"))
	ts := httptest.NewServer(first.Handler())
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*", Why: "early"}); code != http.StatusServiceUnavailable {
		t.Fatalf("a suspend before the table loaded = %d %s, want 503", code, msg)
	}
	// Work that arrives in the window — a task run by hand while the restore reads — is held,
	// and released when the table has loaded and holds nothing of it.
	early := newIdleHandle(t)
	first.Eng.Adopt("demo.pr.1", "early", early)
	if early.count("pause") != 1 {
		t.Fatalf("a task tracked before the suspensions loaded was paused %d times, want once", early.count("pause"))
	}
	first.waitMu.Lock()
	first.waiting = map[string]bool{phaseSuspensions: true}
	first.waitMu.Unlock()
	for name, phase := range map[string]func() error{
		"reconcileTasks": first.reconcileTasks, "restorePackages": first.restorePackages,
	} {
		if err := phase(); !entity.IsUnread(err) {
			t.Fatalf("%s while the suspensions are restoring = %v, want it to wait for them", name, err)
		}
	}
	first.waitMu.Lock()
	first.waiting = nil
	first.waitMu.Unlock()

	ctx, down := context.WithCancel(t.Context())
	first.Start(ctx)
	if !first.susp.Loaded() {
		t.Fatal("Start did not load the suspensions")
	}
	if early.count("resume") != 1 {
		t.Fatalf("the task held while the suspensions loaded was resumed %d times once they had, want once", early.count("resume"))
	}
	code, held, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "verse-dev.drift", Subject: "*", Why: "across a restart"})
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	ts.Close()
	down()
	first.stopAllIOs()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	sus, ok := second.susp.Get(held.ID)
	if !ok || sus.Held["user:"+tester].Why != "across a restart" {
		t.Fatalf("after a restart the table has %s = %+v (ok=%v), want it back with its holder", held.ID, sus, ok)
	}
}

// questionRecord reads a question's record.
func questionRecord(t *testing.T, srv *Server, id string) entity.Payload {
	t.Helper()
	e, ok, err := srv.Store.Get(mirror.QuestionRef(id))
	if err != nil || !ok {
		t.Fatalf("no record for question %s (ok=%v err=%v)", id, ok, err)
	}
	return e.Payload
}

// waitQuestionStatus waits for a question's record to reach a status.
func waitQuestionStatus(t *testing.T, srv *Server, id, status string) entity.Payload {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.QuestionRef(id)); ok && entity.Str(e.Payload, "status") == status {
			return e.Payload
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(mirror.QuestionRef(id))
			t.Fatalf("question %s never became %s; record %v", id, status, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// pendingHas reports whether the registry has a question pending.
func pendingHas(srv *Server, id string) bool {
	for _, p := range srv.Reg.Pending() {
		if p.Question.ID == id {
			return true
		}
	}
	return false
}

// A firing waiting at its gate is withdrawn from in front of people when a suspension lands:
// the record goes orphaned with the suspension as its why, keeps the ballot already cast, and
// cannot be answered while withdrawn. Lifted, the same question is on the docket again, joining
// what was cast, and the next ballot settles it.
func TestAWithdrawnQuestionKeepsItsBallotsAndIsAskedAgain(t *testing.T) {
	srv, ts := startedServer(t)
	addRule(t, ts, `{
		name: ship,
		condition: {subject: {path: "demo.release.*", value: {stage: open}}},
		gates: [{name: approvals, need: 2}],
		action: {patch: {stage: shipped}},
	}`)
	subject := entity.RefAt("demo", "release", "1")
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"stage": "open"}.Payload())
	// Alice and bob may write the release, so they answer for it.
	if err := entity.SetLevel(srv.Store, subject, entity.ReadWrite, []string{"user:alice@example.com", "user:bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	id := waitRuleQuestion(t, srv, "ship", subject.String())

	ballot := func(by string) int {
		t.Helper()
		resp := postTony(t, ts.URL+answerPath(id), api.Resolve{Choice: "proceed", By: by})
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := ballot("user:alice@example.com"); code != http.StatusAccepted {
		t.Fatalf("alice's ballot = %d, want it recorded as 1 of 2", code)
	}

	code, held, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1", Why: "hold the release"})
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	rec := waitQuestionStatus(t, srv, id, mirror.QuestionOrphaned)
	if why := entity.Str(rec, "why"); !strings.Contains(why, held.ID) {
		t.Fatalf("the withdrawn question says why %q, want it to name %s", why, held.ID)
	}
	if entity.Get(entity.Get(rec, "ballots"), "user:alice@example.com") == nil {
		t.Fatalf("the withdrawal lost alice's ballot: %v", rec)
	}
	if pendingHas(srv, id) {
		t.Fatal("the withdrawn question is still pending in the registry")
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		st := statusOf(t, ts).Suspensions
		if st != nil && *st == (api.SuspensionCount{Count: 1, Questions: 1}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status says suspensions %+v, want one in force holding the withdrawn question", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := ballot("user:bob@example.com"); code != http.StatusNotFound {
		t.Fatalf("a ballot on a withdrawn question = %d, want 404", code)
	}
	stays := func() {
		t.Helper()
		time.Sleep(200 * time.Millisecond)
		if e, _, _ := srv.Store.Get(subject); entity.Str(e.Payload, "stage") != "open" {
			t.Fatalf("the release moved while suspended: %v", e.Payload)
		}
	}
	stays()

	if code, _, msg := suspendAs(t, ts, "resume", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1"}); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	rec = waitQuestionStatus(t, srv, id, mirror.QuestionPending)
	if entity.Get(entity.Get(rec, "ballots"), "user:alice@example.com") == nil || entity.Get(rec, "askedAgain") == nil {
		t.Fatalf("the question asked again = %v, want alice's ballot on it and askedAgain", rec)
	}
	deadline := time.After(3 * time.Second)
	for !pendingHas(srv, id) {
		select {
		case <-deadline:
			t.Fatal("the lifted question never came back to the registry")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if code := ballot("user:bob@example.com"); code != http.StatusNoContent {
		t.Fatalf("bob's ballot after the lift = %d, want it to settle the question", code)
	}
	deadline = time.After(3 * time.Second)
	for {
		if e, _, _ := srv.Store.Get(subject); entity.Str(e.Payload, "stage") == "shipped" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the release did not ship on the second ballot")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A withdrawn question's clock stops: one withdrawn with ten minutes left and lifted an hour
// later has ten minutes left.
func TestAWithdrawnQuestionsClockStops(t *testing.T) {
	srv, ts := startedServer(t)
	addRule(t, ts, `{
		name: ship,
		condition: {subject: {path: "demo.release.*", value: {stage: open}}},
		gates: [{name: ok, mode: ask, deadline: {within: "10m", default: deny}}],
		action: {patch: {stage: shipped}},
	}`)
	subject := entity.RefAt("demo", "release", "1")
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"stage": "open"}.Payload())
	id := waitRuleQuestion(t, srv, "ship", subject.String())

	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1", Why: "hold"}); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	waitQuestionStatus(t, srv, id, mirror.QuestionOrphaned)
	// An hour ago, with ten minutes left.
	withdrawn := time.Now().Add(-time.Hour).UTC()
	if _, _, err := srv.Store.Write(entity.Write{At: mirror.QuestionRef(id).At(), Value: entity.Obj{
		"orphanedAt": withdrawn.Format(time.RFC3339Nano),
		"deadline":   entity.Obj{"at": withdrawn.Add(10 * time.Minute).Format(time.RFC3339Nano)},
	}.Payload()}); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1"}); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	rec := waitQuestionStatus(t, srv, id, mirror.QuestionPending)
	_, _, at := mirror.DeadlineOf(rec)
	fires, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		t.Fatalf("the question asked again has deadline.at %q: %v", at, err)
	}
	if left := time.Until(fires); left < 9*time.Minute || left > 11*time.Minute {
		t.Fatalf("the question asked again has %s left, want ten minutes", left.Round(time.Second))
	}
}

// A rule that stops selecting its subject during a suspension ends its firing: the question is
// moot, and the lift asks nothing.
func TestMootnessDuringASuspensionEndsTheFiring(t *testing.T) {
	srv, ts := startedServer(t)
	addRule(t, ts, `{
		name: ship,
		condition: {subject: {path: "demo.release.*", value: {stage: open}}},
		gates: [{name: ok, mode: ask}],
		action: {patch: {stage: shipped}},
	}`)
	subject := entity.RefAt("demo", "release", "1")
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"stage": "open"}.Payload())
	id := waitRuleQuestion(t, srv, "ship", subject.String())
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1", Why: "hold"}); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	waitQuestionStatus(t, srv, id, mirror.QuestionOrphaned)
	entitytest.Set(t, asTester(srv), subject, entity.Obj{"stage": "withdrawn"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if code, _, msg := suspendAs(t, ts, "resume", "", "", api.FlowSelect{Charter: "*", Subject: "demo.release.1"}); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	time.Sleep(300 * time.Millisecond)
	if pendingHas(srv, id) {
		t.Fatal("the lift asked a question the rule no longer selects the subject for")
	}
	if got := entity.Str(questionRecord(t, srv, id), "status"); got != mirror.QuestionOrphaned {
		t.Fatalf("the moot question is %s after the lift, want it left orphaned", got)
	}
}

// runVerb posts `flow suspend|resume --task` and hands back the status and the body's text.
func runVerb(t *testing.T, ts *httptest.Server, verb, id string) (int, string) {
	t.Helper()
	return flowAct(t, ts, verb, "", "", api.FlowSelect{Task: id})
}

// A task knows whose hold it is under. An operator's hold on a run and a suspension's are two
// holds: a `flow resume --task` of a run a suspension holds is refused, naming it, and leaves it
// held; the lift resumes only what nothing else holds, so an operator's hold outlives it whichever
// came first.
func TestATaskKnowsWhoseHoldItIs(t *testing.T) {
	srv, ts := startedServer(t)
	suspend := api.FlowSelect{Charter: "*", Subject: "demo.pr.1", Why: "hold it"}
	lift := api.FlowSelect{Charter: "*", Subject: "demo.pr.1"}
	heldBy := func(id string) []string { return srv.Eng.HeldBy(id) }

	// suspend --task, suspend, resume --task → refused; lift → still held by the operator.
	testerOwns(t, srv, entity.RefAt("demo"), mirror.Ref("first"), mirror.Ref("second"))
	first := newIdleHandle(t)
	srv.Eng.Adopt("demo.pr.1", "first", first)
	if code, msg := runVerb(t, ts, "suspend", "first"); code != http.StatusOK {
		t.Fatalf("flow suspend --task = %d %s", code, msg)
	}
	code, held, msg := suspendAs(t, ts, "suspend", "", "", suspend)
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	if got := heldBy("first"); len(got) != 2 || got[0] != trigger.HeldByOperator || got[1] != held.ID {
		t.Fatalf("the paused task under a suspension is held by %v, want the operator and %s", got, held.ID)
	}
	code, msg = runVerb(t, ts, "resume", "first")
	if code != http.StatusConflict || !strings.Contains(msg, held.ID) || !strings.Contains(msg, "verse flow --args") {
		t.Fatalf("flow resume --task of a suspended task = %d %q, want 409 naming %s and the verb that lifts it", code, msg, held.ID)
	}
	if code, msg := control(t, ts, "/tasks/first/resume", "", nil); code != http.StatusConflict || !strings.Contains(msg, held.ID) {
		t.Fatalf("task resume of a suspended task = %d %q, want 409 naming %s", code, msg, held.ID)
	}
	if first.count("resume") != 0 {
		t.Fatal("a refused resume resumed the task")
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "", lift); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	if got := heldBy("first"); len(got) != 1 || got[0] != trigger.HeldByOperator || first.count("resume") != 0 {
		t.Fatalf("after the lift the task is held by %v and resumed %d times, want the operator's hold standing",
			got, first.count("resume"))
	}
	if code, msg := runVerb(t, ts, "resume", "first"); code != http.StatusOK || first.count("resume") != 1 {
		t.Fatalf("flow resume --task once nothing suspends it = %d %s, resumed %d", code, msg, first.count("resume"))
	}
	close(first.ch)
	first.ch = make(chan task.Frame)

	// suspend, the runtime's task pause, lift → still held: `/tasks/{id}/pause` is the operator's
	// hold too.
	second := newIdleHandle(t)
	srv.Eng.Adopt("demo.pr.1", "second", second)
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", suspend); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	if second.count("pause") != 1 {
		t.Fatalf("the suspension paused the task %d times, want once", second.count("pause"))
	}
	if code, msg := control(t, ts, "/tasks/second/pause", "", nil); code != http.StatusNoContent {
		t.Fatalf("task pause = %d %s", code, msg)
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "", lift); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, msg)
	}
	if got := heldBy("second"); len(got) != 1 || got[0] != trigger.HeldByOperator || second.count("resume") != 0 {
		t.Fatalf("after the lift the task is held by %v and resumed %d times, want the operator's hold standing",
			got, second.count("resume"))
	}
}

// A task a suspension already holds names a second suspension that also covers it, without being
// paused again: its heldBy and the second suspension's listing both count it, and it runs only
// once neither holds it.
func TestASecondSuspensionIsNamedOnTheTaskItAlsoHolds(t *testing.T) {
	srv, ts := startedServer(t)
	testerOwns(t, srv, entity.RefAt("demo"))
	h := newIdleHandle(t)
	srv.Eng.Adopt("demo.pr.1", "run", h)
	narrow := api.FlowSelect{Subject: "demo.pr.1", Why: "this PR"}
	wide := api.FlowSelect{Subject: "demo.pr.*", Why: "every PR"}
	for _, sel := range []api.FlowSelect{narrow, wide} {
		if code, _, msg := suspendAs(t, ts, "suspend", "", "", sel); code != http.StatusOK {
			t.Fatalf("suspend %+v = %d %s", sel, code, msg)
		}
	}
	both := `*:*@demo.pr."1",*:*@demo.pr.*`
	if got := strings.Join(srv.Eng.HeldBy("run"), ","); got != both || h.count("pause") != 1 {
		t.Fatalf("the task is held by %s and paused %d times, want both suspensions named and one pause", got, h.count("pause"))
	}
	for _, row := range suspensionsIn(t, ts, "") {
		if row.Holding == nil || row.Holding.Tasks != 1 {
			t.Fatalf("%s lists as holding %+v, want the one task", row.ID, row.Holding)
		}
	}
	narrow.Why, wide.Why = "", ""
	if code, _, msg := suspendAs(t, ts, "resume", "", "", narrow); code != http.StatusOK {
		t.Fatalf("resume %+v = %d %s", narrow, code, msg)
	}
	if got := strings.Join(srv.Eng.HeldBy("run"), ","); got != `*:*@demo.pr.*` || h.count("resume") != 0 {
		t.Fatalf("after one lift the task is held by %s and resumed %d times, want the wider one holding it", got, h.count("resume"))
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "", wide); code != http.StatusOK {
		t.Fatalf("resume %+v = %d %s", wide, code, msg)
	}
	if len(srv.Eng.HeldBy("run")) != 0 || h.count("resume") != 1 {
		t.Fatalf("after both lifts the task is held by %v and resumed %d times", srv.Eng.HeldBy("run"), h.count("resume"))
	}
}

// A package's work is shown by the package's engine: `flow show` names who holds a package's live
// task and which of the package's suspensions cover its positions — the live one found in the
// engine tracking it, and one no engine tracks by the charter its id says minted it.
func TestAPackagesFlowIsShownByItsEngine(t *testing.T) {
	srv, ts := startedServer(t)
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}
	pkg := srv.packageEngineIfRunning("notes")
	if pkg == nil {
		t.Fatal("the package installed and is running in no engine")
	}
	record := func(id, subject string) {
		t.Helper()
		if _, _, err := srv.Store.Write(entity.Write{At: mirror.Ref(id).At(), Value: entity.Obj{
			"status": "running", "flow": subject, "chain": "note-the-head"}.Payload()}); err != nil {
			t.Fatal(err)
		}
		testerOwns(t, srv, mirror.Ref(id)) // the package is the tester's, and so is what it spawns
	}
	live, adrift := "notes:note-the-head@notes.seen.head", "notes:note-the-head@notes.seen.later"
	record(live, "notes.seen.head")
	pkg.AdoptChain("notes.seen.head", live, "note-the-head", newIdleHandle(t))
	record(adrift, "notes.seen.later")

	tree := func(subject string) trigger.FlowNode {
		t.Helper()
		resp, err := http.Get(ts.URL + "/flow/tree?subject=" + subject)
		if err != nil {
			t.Fatal(err)
		}
		var trees []trigger.FlowTree
		decodeResp(t, resp, &trees)
		if len(trees) != 1 || len(trees[0].Roots) != 1 {
			t.Fatalf("flow show %s = %+v, want one root", subject, trees)
		}
		return trees[0].Roots[0]
	}
	if code, msg := runVerb(t, ts, "suspend", live); code != http.StatusOK {
		t.Fatalf("flow suspend --task = %d %s", code, msg)
	}
	if got := tree("notes.seen.head").HeldBy; len(got) != 1 || got[0] != trigger.HeldByOperator {
		t.Fatalf("flow show of the package's held task says heldBy %v, want the operator", got)
	}
	code, held, msg := suspendAs(t, ts, "suspend", tester, "", api.FlowSelect{Charter: "notes:note-the-head", Why: "reading it"})
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	for _, subject := range []string{"notes.seen.head", "notes.seen.later"} {
		if got := tree(subject).Suspended; !slices.Contains(got, held.ID) {
			t.Fatalf("flow show %s says suspended %v, want the package's %s", subject, got, held.ID)
		}
	}
}

// A task tracked while a suspension covers it — re-attached after a restart, or run by hand
// about a suspended subject — is held as it is tracked, and says so on its record and its flow.
func TestATaskTrackedUnderASuspensionIsHeld(t *testing.T) {
	srv, ts := startedServer(t)
	testerOwns(t, srv, entity.RefAt("demo"))
	code, held, msg := suspendAs(t, ts, "suspend", "", "", api.FlowSelect{Charter: "*", Subject: "demo.pr.1", Why: "hold it"})
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	if _, _, err := srv.Store.Write(entity.Write{At: mirror.Ref("resumed").At(),
		Value: entity.Obj{"status": "running", "flow": "demo.pr.1"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	h := newIdleHandle(t)
	srv.Eng.AdoptChain("demo.pr.1", "resumed", "", h)
	if h.count("pause") != 1 {
		t.Fatalf("the task tracked under a suspension was paused %d times, want once", h.count("pause"))
	}
	e, _, _ := srv.Store.Get(mirror.Ref("resumed"))
	if got := entity.Strs(e.Payload, mirror.HeldByField); len(got) != 1 || got[0] != held.ID {
		t.Fatalf("the task record says heldBy %v, want %s", got, held.ID)
	}
	resp, err := http.Get(ts.URL + "/flow/tree?subject=demo.pr.1")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); !strings.Contains(body, held.ID) {
		t.Fatalf("flow show does not say what holds the task:\n%s", body)
	}
}

// A scope's suspensions load before its charter arms a rule. Across a restart, the scope's own
// review is restored when the dev reconnects, into an engine whose table already holds the
// scope's suspension of it — so its opening replay does not act on the PR waiting for it — and
// the scope's resume is what lets it act.
func TestAScopesSuspensionsLoadBeforeItsCharterArms(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	firstCtx, down := context.WithCancel(t.Context())
	first.Start(firstCtx)
	firstHTTP := httptest.NewServer(first.Handler())
	addScopedRule(t, firstHTTP, "me", `{
		name: scoped-review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	if code, _, msg := suspendAs(t, firstHTTP, "suspend", "", "me", api.FlowSelect{Charter: ":scoped-review", Subject: "*", Why: "reading it"}); code != http.StatusOK {
		t.Fatalf("suspend --scope me = %d %s", code, msg)
	}
	firstHTTP.Close()
	down()
	first.stopAllIOs()

	store := entitytest.StoreAt(t, addr)
	second := NewWithStore(ScriptedBuilder, store)
	second.Start(t.Context())
	ts := httptest.NewServer(second.Handler())
	defer ts.Close()
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, entity.As(store, "user:"+tester).Scope("me"), ref, entity.Obj{"stage": "open"}.Payload())
	addScopedRule(t, ts, "me", `{
		name: connect-probe,
		condition: {subject: {path: "never.never.*"}},
		action: {patch: {seen: true}},
	}`)
	if !second.engFor("me").Knows("scoped-review") {
		t.Fatal("the scope's charter was not restored")
	}
	time.Sleep(300 * time.Millisecond)
	if e, _, _ := store.Scope("me").Get(ref); entity.Str(e.Payload, "stage") != "open" {
		t.Fatalf("the restored rule acted on a PR its scope's suspension holds: %v", e.Payload)
	}
	if code, _, msg := suspendAs(t, ts, "resume", "", "me", api.FlowSelect{Charter: ":scoped-review", Subject: "*"}); code != http.StatusOK {
		t.Fatalf("resume --scope me = %d %s", code, msg)
	}
	deadline := time.After(3 * time.Second)
	for {
		if e, _, _ := store.Scope("me").Get(ref); entity.Str(e.Payload, "stage") == "reviewed" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the lifted scope rule did not act on the PR")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A suspension is never a mystery: status counts them in the scope asked about, the charter
// marks a rule suspended whole in its listing and its census, a flow's unfinished position says
// what suspends it, and the listing says what each suspension holds now and whether it names
// anything at all.
func TestASuspensionIsSeenWhereTheWorkIsRead(t *testing.T) {
	srv, ts := startedServer(t)
	addRule(t, ts, `{
		name: review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	testerOwns(t, srv, entity.RefAt("demo"), entity.RefAt("verse-dev"))
	srv.Eng.Adopt("demo.pr.1", "by-hand", newIdleHandle(t))
	if _, _, err := srv.Store.Write(entity.Write{At: mirror.Ref("review@demo.pr.2").At(),
		Value: entity.Obj{"status": "running", "flow": "demo.pr.2", "chain": "review"}.Payload()}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []api.FlowSelect{
		{Charter: "*", Subject: "demo.pr.1", Why: "a person is looking"},
		{Charter: ":review", Subject: "*", Why: "the review rule is wrong"},
		{Charter: "verse-dev.gone", Subject: "*", Why: "a package that is not installed"},
	} {
		if code, _, msg := suspendAs(t, ts, "suspend", "", "", body); code != http.StatusOK {
			t.Fatalf("suspend %+v = %d %s", body, code, msg)
		}
	}

	if st := statusOf(t, ts); st.Suspensions == nil ||
		*st.Suspensions != (api.SuspensionCount{Count: 3, Tasks: 1, Subjects: 1}) {
		t.Fatalf("status says suspensions %+v, want three in force holding the one task by hand", st.Suspensions)
	}
	resp, err := http.Get(ts.URL + "/status?scope=me")
	if err != nil {
		t.Fatal(err)
	}
	var scoped api.Status
	decodeResp(t, resp, &scoped)
	if scoped.Suspensions != nil {
		t.Fatalf("status in a scope with none says suspensions %+v, want it absent", scoped.Suspensions)
	}

	byID := map[string]api.FlowRow{}
	for _, s := range suspensionsIn(t, ts, "") {
		byID[s.ID] = s
	}
	covers := func(row api.FlowRow) bool { return row.Covers != nil && *row.Covers }
	if s := byID[`*:*@demo.pr."1"`]; s.Holding == nil || s.Holding.Tasks != 1 || !covers(s) {
		t.Fatalf("the PR's suspension lists as %+v, want one task held and covering", s)
	}
	if s := byID[":review@*"]; !covers(s) || s.Charter != ":review" {
		t.Fatalf("review's suspension lists as %+v, want it covering the installed rule, named --charter :review", s)
	}
	if s := byID["verse-dev.gone:*@*"]; s.Covers == nil || covers(s) {
		t.Fatalf("a suspension of a package nobody installed lists as covering: %+v", s)
	}

	// THE LISTING IS WHAT AN ACT REACHES: narrowed, it shows the live tasks a suspension in the
	// same words would hold, and the one suspension a resume in them would lift — the record the
	// flags spell, an absent one being `*`, and not a wider one that also holds the work.
	for _, c := range []struct {
		sel  api.FlowSelect
		want string
	}{
		{api.FlowSelect{Charter: "*", Subject: "demo.pr.1"}, `by-hand,*:*@demo.pr."1"`},
		{api.FlowSelect{Subject: `demo.pr."1"`}, `by-hand,*:*@demo.pr."1"`},
		{api.FlowSelect{Charter: ":review"}, `:review@*`},
		{api.FlowSelect{Charter: ":review", Subject: "demo.pr.1"}, ``},
		{api.FlowSelect{Subject: "demo.pr.2"}, ``},
		{api.FlowSelect{Charter: "verse-dev.gone"}, `verse-dev.gone:*@*`},
	} {
		var got []string
		for _, row := range flowRows(t, ts, "", c.sel) {
			got = append(got, row.Task+row.ID)
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("flows %+v = %v, want %s", c.sel, got, c.want)
		}
	}

	resp, err = http.Get(ts.URL + "/charter?path=rules")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); !strings.Contains(body, "suspended") || !strings.Contains(body, ":review@*") {
		t.Fatalf("the charter listing does not mark review suspended:\n%s", body)
	}
	resp, err = http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); !strings.Contains(body, "suspended:") {
		t.Fatalf("the status census does not name the suspended rule:\n%s", body)
	}
	resp, err = http.Get(ts.URL + "/flow/tree?subject=demo.pr.2")
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); !strings.Contains(body, ":review@*") {
		t.Fatalf("flow show does not say what suspends review's work:\n%s", body)
	}
	// THE TOP OF A TREE IS WHAT HOLDS THE SUBJECT'S WORK: what an installed rule that could select
	// it would be held by, and what holds its live tasks — not a suspension of a charter nobody
	// installed. A subject with no work is shown when something would hold it, with no roots, and
	// --active / --no-active narrow to the subjects with a live task or without one.
	entitytest.Set(t, srv.Store, entity.RefAt("demo", "pr", "3"), entity.Obj{"stage": "closed"}.Payload())
	trees := func(q string) []trigger.FlowTree {
		t.Helper()
		resp, err := http.Get(ts.URL + "/flow/tree?" + q)
		if err != nil {
			t.Fatal(err)
		}
		var out []trigger.FlowTree
		decodeResp(t, resp, &out)
		return out
	}
	shown := func(ts []trigger.FlowTree) string {
		var out []string
		for _, tr := range ts {
			out = append(out, fmt.Sprintf("%s active=%v roots=%d [%s]", tr.Subject, tr.Active, len(tr.Roots), strings.Join(tr.Suspended, " ")))
		}
		return strings.Join(out, "; ")
	}
	for _, c := range []struct{ q, want string }{
		{"subject=demo.pr.1", `demo.pr."1" active=true roots=0 [*:*@demo.pr."1" :review@*]`},
		{"subject=demo.pr.3", `demo.pr."3" active=false roots=0 [:review@*]`},
		{"subject=demo.issue.1", ``},
		{"subject=demo.pr.*", `demo.pr."1" active=true roots=0 [*:*@demo.pr."1" :review@*]; ` +
			`demo.pr."2" active=false roots=1 [:review@*]; demo.pr."3" active=false roots=0 [:review@*]`},
		{"subject=demo.pr.*&active=true", `demo.pr."1" active=true roots=0 [*:*@demo.pr."1" :review@*]`},
		{"subject=demo.pr.*&active=false", `demo.pr."2" active=false roots=1 [:review@*]; demo.pr."3" active=false roots=0 [:review@*]`},
	} {
		if got := shown(trees(c.q)); got != c.want {
			t.Errorf("flow show %s =\n  %s\nwant\n  %s", c.q, got, c.want)
		}
	}
}
