package versed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

const asker = "user:victor@example.com"

// datedRule is one standing rule posing every `ask.*` question under the clock its record names
// (`by`), settling on proceed when it passes, and latching the record when it is answered.
const datedRule = `{
	name: ask,
	condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {answer: null}]}},
	gates: [{name: answer, deadline: {atFrom: by, default: proceed}}],
	action: {patch: {answer: yes}},
}`

func datedServer(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	if srv.clock == (wallClock{}) {
		clockOf(t, srv)
	}
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp := installAs(t, "alice@example.com", ts.URL, datedRule)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("install: %s: %s", resp.Status, body)
	}
	resp.Body.Close()
	return ts
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// stampOf is the instant a posed question's deadline is armed against.
// The question record lands a moment after the record saying who may answer it, which is what
// waitRuleQuestion waits for, and the stamp a moment after that; so this waits for the stamp.
func stampOf(t *testing.T, srv *Server, qid string) time.Time {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.QuestionRef(qid)); ok {
			if _, _, at := mirror.DeadlineOf(e.Payload); at != "" {
				stamped, err := time.Parse(time.RFC3339Nano, at)
				if err != nil {
					t.Fatalf("question %s carries a stamp no clock reads: %q", qid, at)
				}
				return stamped
			}
		}
		select {
		case <-deadline:
			t.Fatalf("question %s was never stamped", qid)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// settledByDeadline moves the clock to at and waits a moment for the question to settle, and
// requires the clock to have settled it, on proceed (the rule's default).
func settledByDeadline(t *testing.T, srv *Server, qid string, at time.Time) {
	t.Helper()
	clock := srv.clock.(*fakeClock)
	if d := at.Sub(clock.Now()); d > 0 {
		clock.Advance(d)
	}
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(mirror.QuestionRef(qid)); ok && entity.Str(e.Payload, "status") != mirror.QuestionPending {
			if by, v := entity.Str(e.Payload, "by"), entity.Str(e.Payload, "verdict"); by != "deadline" || v != "proceed" {
				t.Fatalf("question %s settled by %q on %q, want the deadline on proceed", qid, by, v)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("question %s never settled", qid)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A QUESTION DATED BY ITS RECORD settles at that date: the stamp is the date the record says, not
// a duration from when the question happened to be posed.
func TestAQuestionSettlesAtTheDateItsRecordSays(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	datedServer(t, srv)
	ask := entity.RefAt("ask", "one")
	by := srv.now().Add(90 * time.Second)
	entitytest.Set(t, srv.Store, ask, entity.Obj{"question": "q", "by": rfc(by)}.Payload())
	qid := waitRuleQuestion(t, srv, "ask", ask.String())
	if got := stampOf(t, srv, qid); !got.Equal(by) {
		t.Fatalf("stamped %s, want the record's date %s", got, by)
	}
	// Short of its date it stands, however long it was posed; at its date it settles.
	srv.clock.(*fakeClock).Advance(89 * time.Second)
	if e, _, _ := srv.Store.Get(mirror.QuestionRef(qid)); entity.Str(e.Payload, "status") != mirror.QuestionPending {
		t.Fatalf("settled before its date: %v", entity.ToObj(e.Payload))
	}
	settledByDeadline(t, srv, qid, by)
}

// EDITING THE DATE MOVES THE CLOCK. Whoever may write the record moves the question's deadline by
// writing a new date; the question is not re-posed, its instant is.
func TestEditingTheRecordsDateMovesTheQuestionsDeadline(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	datedServer(t, srv)
	ask := entity.RefAt("ask", "one")
	later := srv.now().Add(time.Hour)
	entitytest.Set(t, srv.Store, ask, entity.Obj{"question": "q", "by": rfc(later)}.Payload())
	qid := waitRuleQuestion(t, srv, "ask", ask.String())
	// Armed first, at the date it was posed with — so what moves it next can only be the edit
	// followed while it stands, and not the arming reading the date again.
	if got := stampOf(t, srv, qid); !got.Equal(later) {
		t.Fatalf("armed at %s, want the date it was posed with %s", got, later)
	}

	soon := srv.now().Add(time.Minute)
	entitytest.Set(t, srv.Store, ask, entity.Obj{"question": "q", "by": rfc(soon)}.Payload())
	deadline := time.After(3 * time.Second)
	for !stampOf(t, srv, qid).Equal(soon) {
		select {
		case <-deadline:
			t.Fatalf("the stamp stayed %s after the date moved to %s", stampOf(t, srv, qid), soon)
		case <-time.After(20 * time.Millisecond):
		}
	}
	settledByDeadline(t, srv, qid, soon)
}

// A SETTLED QUESTION KEEPS THE CLOCK IT SETTLED UNDER: what it records is what happened.
func TestASettledQuestionsDeadlineDoesNotMove(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	datedServer(t, srv)
	ask := entity.RefAt("ask", "one")
	by := srv.now().Add(time.Minute)
	entitytest.Set(t, srv.Store, ask, entity.Obj{"question": "q", "by": rfc(by)}.Payload())
	qid := waitRuleQuestion(t, srv, "ask", ask.String())
	settledByDeadline(t, srv, qid, by)
	before := stampOf(t, srv, qid)
	moved, err := mirror.MoveDeadline(entity.As(srv.Store, entity.SystemDocket), qid, srv.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if moved || !stampOf(t, srv, qid).Equal(before) {
		t.Fatalf("a settled question's deadline moved (moved=%v, now %s)", moved, stampOf(t, srv, qid))
	}
}

// A DATE NO CLOCK READS asks nobody: a question that would never settle as its asker meant.
func TestARecordWhoseDateIsUnreadableIsNotAsked(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	datedServer(t, srv)
	for _, by := range []any{"friday", 42} {
		bad := entity.RefAt("ask", "bad")
		rec := entity.Obj{"question": "q"}
		if by != nil {
			rec["by"] = by
		}
		entitytest.Set(t, srv.Store, bad, rec.Payload())
		stop := time.Now().Add(150 * time.Millisecond)
		for time.Now().Before(stop) {
			for _, p := range srv.Reg.Pending() {
				if p.Question.Subject == bad.String() {
					t.Fatalf("by %v: a question was posed", by)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		entitytest.Set(t, srv.Store, bad, entity.Removed())
	}
	good := entity.RefAt("ask", "good")
	entitytest.Set(t, srv.Store, good, entity.Obj{"question": "q", "by": rfc(srv.now().Add(time.Hour))}.Payload())
	waitRuleQuestion(t, srv, "ask", good.String())
}

// A RECORD THAT GIVES NO DATE GIVES NO CLOCK: the question is asked, and stands until answered.
func TestARecordGivingNoDateIsAskedWithNoClock(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	datedServer(t, srv)
	ask := entity.RefAt("ask", "undated")
	entitytest.Set(t, srv.Store, ask, entity.Obj{"question": "q"}.Payload())
	qid := waitRuleQuestion(t, srv, "ask", ask.String())
	time.Sleep(100 * time.Millisecond)
	e, ok, err := srv.Store.Get(mirror.QuestionRef(qid))
	if err != nil || !ok {
		t.Fatalf("question %s: %v", qid, err)
	}
	if within, _, at := mirror.DeadlineOf(e.Payload); within != "" || at != "" {
		t.Fatalf("an undated ask stands under a clock: within %q at %q", within, at)
	}
}

// A MOVED DATE HOLDS ACROSS A RESTART: the second verse settles the question at the date it was
// moved to, not the one it was first posed with.
func TestAMovedDeadlineHoldsAcrossARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	clock := clockOf(t, first)
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	resp := installAs(t, "alice@example.com", ts.URL, datedRule)
	resp.Body.Close()
	ask := entity.RefAt("ask", "one")
	later := clock.Now().Add(time.Hour)
	entitytest.Set(t, first.Store, ask, entity.Obj{"question": "q", "by": rfc(later)}.Payload())
	qid := waitRuleQuestion(t, first, "ask", ask.String())
	if got := stampOf(t, first, qid); !got.Equal(later) {
		t.Fatalf("armed at %s, want the date it was posed with %s", got, later)
	}
	soon := clock.Now().Add(time.Minute)
	entitytest.Set(t, first.Store, ask, entity.Obj{"question": "q", "by": rfc(soon)}.Payload())
	deadline := time.After(3 * time.Second)
	for !stampOf(t, first, qid).Equal(soon) {
		select {
		case <-deadline:
			t.Fatalf("the date never moved before the restart")
		case <-time.After(20 * time.Millisecond):
		}
	}
	ts.Close()
	stop()

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.clock = clock
	second.Start(t.Context())
	settledByDeadline(t, second, qid, soon)
	if got := stampOf(t, second, qid); !got.Equal(soon) {
		t.Fatalf("after the restart it settled against %s, want the moved date %s", got, soon)
	}
}

// THE DATE IS A RECORD'S TO SAY, and a rule says only where: `within` and `atFrom` are not
// written together, `at` is never a rule's, and a spawn's gate has no record to read one from.
func TestADatedDeadlineIsWrittenAsWhereTheDateIs(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cond := `condition: {subject: {path: "ask.*", value: !and [{question: null}, !not {answer: null}]}}`
	for name, c := range map[string]struct{ rule, says string }{
		"within and atFrom": {`{name: r, ` + cond + `, gates: [{name: a, deadline: {within: "1h", atFrom: by}}], action: {patch: {answer: yes}}}`, "both `within` and `atFrom`"},
		"a written at":      {`{name: r, ` + cond + `, gates: [{name: a, deadline: {at: "2026-10-02T17:00:00Z"}}], action: {patch: {answer: yes}}}`, "`at:` is the engine's"},
		"a spawn row":       {`{name: r, ` + cond + `, action: {spawn: {script: "true", autonomy: [{name: g, match: {system: commands, verb: run}, then: {ask: {within: soon}}}, {then: ask}]}, onDone: {patch: {answer: yes}}}}`, "is not a duration"},
	} {
		resp := installAs(t, "alice@example.com", ts.URL, c.rule)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), c.says) {
			t.Errorf("%s: install = %s: %s", name, resp.Status, body)
		}
	}
}
