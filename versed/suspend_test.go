package versed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// suspendAs posts a suspend or a resume as a principal, in scope when one is named, and hands
// back the status, the record when there is one, and the body's text.
func suspendAs(t *testing.T, ts *httptest.Server, verb, as, scope string, body api.Suspend) (int, api.Suspension, string) {
	t.Helper()
	url := ts.URL + "/" + verb
	if scope != "" {
		url += "?scope=" + scope
	}
	doc, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	resp := postAs(t, as, url, "application/tony", string(doc))
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out api.Suspension
	if resp.StatusCode == http.StatusOK {
		if err := codec.Unmarshal(raw, &out, codec.Tony); err != nil {
			t.Fatalf("decoding %s: %v\n%s", verb, err, raw)
		}
	}
	return resp.StatusCode, out, strings.TrimSpace(string(raw))
}

// suspensionsIn lists the suspensions in force in a scope.
func suspensionsIn(t *testing.T, ts *httptest.Server, scope string) []api.Suspension {
	t.Helper()
	resp, err := http.Get(ts.URL + "/suspensions?scope=" + scope)
	if err != nil {
		t.Fatal(err)
	}
	var out []api.Suspension
	decodeResp(t, resp, &out)
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

	for _, c := range []struct {
		as   string
		body api.Suspend
		want string
	}{
		{"alice@example.com", api.Suspend{Charter: "verse-dev.drift", Why: "reading its range logic"}, "verse-dev.drift:*@*"},
		{"alice@example.com", api.Suspend{Charter: "*", Subject: `demo.pr."1"`, Why: "a person is reviewing it"}, `*:*@demo.pr."1"`},
		{"bob@example.com", api.Suspend{Charter: "*", Subject: "demo.pr.1", Why: "and so am I"}, `*:*@demo.pr."1"`},
		{"alice@example.com", api.Suspend{Charter: "verse-dev.drift", Rule: "claim", Subject: "git.verse.head", Why: "the claim is wrong"}, "verse-dev.drift:claim@git.verse.head"},
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
	sus, err := trigger.NewSuspension("", "loop", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.hold("", sus, throttle, "the cycle throttle tripped"); err != nil {
		t.Fatal(err)
	}
	if code, _, msg := suspendAs(t, ts, "suspend", "alice@example.com", "", api.Suspend{Rule: "loop", Why: "and I want to read it"}); code != http.StatusOK {
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
	code, got, msg := suspendAs(t, ts, "resume", "bob@example.com", "", api.Suspend{Rule: "loop"})
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

	if code, _, msg := suspendAs(t, ts, "suspend", "", "me", api.Suspend{Charter: "*", Subject: "demo.pr.1", Why: "mine"}); code != http.StatusOK {
		t.Fatalf("suspend --scope me = %d %s", code, msg)
	}
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.Suspend{Charter: "*", Subject: "demo.pr.2", Why: "baseline's"}); code != http.StatusOK {
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
	code, _, msg := suspendAs(t, ts, "resume", "", "", api.Suspend{Charter: "*", Subject: "demo.pr.1"})
	refusesNaming(code, msg, "scope me's", "--scope me", "resume from baseline of the scope's")
	code, _, msg = suspendAs(t, ts, "resume", "", "me", api.Suspend{Charter: "*", Subject: "demo.pr.2"})
	refusesNaming(code, msg, "baseline's", "without --scope", "resume --scope me of baseline's")
	code, _, msg = suspendAs(t, ts, "suspend", "", "me", api.Suspend{Charter: "verse-dev.drift", Why: "not mine"})
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

	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.Suspend{Charter: "verse-dev.drift"}); code != http.StatusBadRequest || !strings.Contains(msg, "why") {
		t.Fatalf("suspend with no why = %d %s, want 400 asking for it", code, msg)
	}
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.Suspend{Charter: "*", Why: "everything"}); code != http.StatusBadRequest {
		t.Fatalf("suspend of every charter about every subject = %d %s, want 400", code, msg)
	}
	resp := putAs(t, "", ts.URL, "verse.trigger.suspension.x", entity.Obj{"charter": "*"}.Payload())
	body := readBody(t, resp)
	if resp.StatusCode/100 == 2 {
		t.Fatalf("a raw put of a suspension record = %s %s, want it refused", resp.Status, body)
	}
	resp, err := http.Post(ts.URL+"/charter", "application/tony", strings.NewReader(`{
		name: suspender, stratum: prs,
		condition: {subject: {system: demo, kind: pr}},
		action: {derive: [{path: [verse, trigger, suspension], value: {charter: "*"}}]},
	}`))
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "suspension") {
		t.Fatalf("a rule deriving a suspension = %s %s, want it refused at install", resp.Status, body)
	}
	if !strings.Contains(body, "verse suspend") {
		t.Fatalf("the refusal = %s, want it to name verse suspend", body)
	}
}

// The table is loaded before anything it could hold is re-attached or restored: until Start's
// first phase has read the records a server's table has not loaded, a suspend is refused rather
// than written over records the table does not know, and the phases that follow wait for it. A
// restart brings every suspension back.
func TestSuspensionsLoadBeforeTheRestore(t *testing.T) {
	addr := entitytest.Addr(t)
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	if first.susp.Loaded() {
		t.Fatal("a server nobody started has its suspensions loaded")
	}
	ts := httptest.NewServer(first.Handler())
	if code, _, msg := suspendAs(t, ts, "suspend", "", "", api.Suspend{Charter: "verse-dev.drift", Why: "early"}); code != http.StatusServiceUnavailable {
		t.Fatalf("a suspend before the table loaded = %d %s, want 503", code, msg)
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
	code, held, msg := suspendAs(t, ts, "suspend", "", "", api.Suspend{Charter: "verse-dev.drift", Why: "across a restart"})
	if code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, msg)
	}
	ts.Close()
	down()
	first.stopAllSources()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	sus, ok := second.susp.Get(held.ID)
	if !ok || sus.Held[entity.SystemDefault].Why != "across a restart" {
		t.Fatalf("after a restart the table has %s = %+v (ok=%v), want it back with its holder", held.ID, sus, ok)
	}
}
