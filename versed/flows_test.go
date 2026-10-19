package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// idleHandle is a task that is running and does nothing: the engine holds a flow open around
// it until its bus closes, which is what makes "there is live work about this subject" a state
// a test can stand in rather than race against. It counts each control verb it is sent, so a
// test can say WHICH task a steer reached rather than only how many.
type idleHandle struct {
	ch    chan task.Event
	mu    sync.Mutex
	verbs map[string]int
}

func newIdleHandle(t *testing.T) *idleHandle {
	h := &idleHandle{ch: make(chan task.Event), verbs: map[string]int{}}
	t.Cleanup(func() { close(h.ch) }) // the reaper exits with the test rather than the process
	return h
}

func (h *idleHandle) got(verb string) error {
	h.mu.Lock()
	h.verbs[verb]++
	h.mu.Unlock()
	return nil
}

// count is how many times the handle was sent verb.
func (h *idleHandle) count(verb string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.verbs[verb]
}

func (h *idleHandle) Subscribe() (<-chan task.Event, func())           { return h.ch, func() {} }
func (h *idleHandle) SubscribeSince(int64) (<-chan task.Event, func()) { return h.ch, func() {} }
func (h *idleHandle) Pause(string) error                               { return h.got("pause") }
func (h *idleHandle) Resume(string) error                              { return h.got("resume") }
func (h *idleHandle) Cancel(string, string) error                      { return h.got("cancel") }
func (h *idleHandle) Steer(string, string, string) error               { return h.got("say") }
func (h *idleHandle) SetPolicy(task.Policy, string) error              { return h.got("policy") }
func (h *idleHandle) Result() string                                   { return "" }
func (h *idleHandle) Err() string                                      { return "" }

// flowRows lists the rows `GET /flows` answers in a scope, narrowed by a selection.
func flowRows(t *testing.T, ts *httptest.Server, scope string, sel api.FlowSelect) []api.FlowRow {
	t.Helper()
	q := url.Values{}
	for k, v := range map[string]string{"scope": scope, "task": sel.Task, "charter": sel.Charter, "subject": sel.Subject} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if sel.Recursive {
		q.Set("recursive", "true")
	}
	resp, err := http.Get(ts.URL + "/flows?" + q.Encode())
	if err != nil {
		t.Fatalf("flows: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("flows %s = %s %s", q.Encode(), resp.Status, readBody(t, resp))
	}
	var out []api.FlowRow
	decodeResp(t, resp, &out)
	return out
}

// taskIDs is the live tasks among rows, in order.
func taskIDs(rows []api.FlowRow) []string {
	var out []string
	for _, row := range rows {
		if row.Kind == api.FlowRowTask {
			out = append(out, row.Task)
		}
	}
	return out
}

// flowAct posts a flow verb as a principal, in scope when one is named, and hands back the status
// and the body's text.
func flowAct(t *testing.T, ts *httptest.Server, verb, as, scope string, sel api.FlowSelect) (int, string) {
	t.Helper()
	u := ts.URL + "/flow/" + verb
	if scope != "" {
		u += "?scope=" + scope
	}
	doc, err := codec.Marshal(sel, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if as == "" {
		as = tester // a steer is somebody's; a caller that does not care who is the tester
	}
	resp := postAs(t, as, u, "application/tony", string(doc))
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

// steered decodes a say's or a cancel's answer.
func steered(t *testing.T, msg string) api.Steered {
	t.Helper()
	var out api.Steered
	if err := codec.Unmarshal([]byte(msg), &out, codec.Tony); err != nil {
		t.Fatalf("decoding %s: %v", msg, err)
	}
	return out
}

// A flow is EVERY ENGINE's, on both surfaces: a live task is tracked by the engine that
// spawned it and a package's charter runs in one of its own, so baseline's registry alone is
// blind to the work a packaged rule started. Staging is that verse: its whole charter sits under
// `verse-dev`. The listing is a row per task, and status counts the tasks and the distinct
// subjects they are about.
func TestFlowsAreEveryEnginesOnBothSurfaces(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	statusFlows := func(q string) api.FlowCount {
		t.Helper()
		resp, err := http.Get(ts.URL + "/status" + q)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var st api.Status
		decodeResp(t, resp, &st)
		return st.Flows
	}

	if got := flowRows(t, ts, "", api.FlowSelect{}); len(got) != 0 {
		t.Fatalf("flows = %+v, want none on a verse with no live work", got)
	}

	// A package, and nothing in baseline: the deployment this is about. Installing the charter
	// is what puts its rules in an engine of their own, which is the engine this is about.
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

	// A task of that package's, live — adopted rather than spawned, so the flow is open for
	// as long as the assertions take rather than for as long as a shell command runs.
	subject := entity.RefAt("notes", "seen", "head").String()
	pkg.AdoptChain(subject, "note-the-head-run", "note-the-head", newIdleHandle(t))

	if n := len(srv.Eng.Flows()); n != 0 {
		t.Fatalf("baseline holds %d flows over work it did not start", n)
	}
	got := flowRows(t, ts, "", api.FlowSelect{})
	if len(got) != 1 || got[0].Task != "note-the-head-run" || got[0].Subject != subject || got[0].Charter != "notes:note-the-head" {
		t.Fatalf("flows = %+v, want the package's task, named by its charter — a package's live work is this verse's", got)
	}
	if n := statusFlows(""); n != (api.FlowCount{Tasks: 1, Subjects: 1}) {
		t.Fatalf("status flows = %+v over a listing of one task", n)
	}

	// TWO TASKS, ONE SUBJECT. Two engines with work about the same entity are two rows and one
	// busy subject.
	srv.Eng.Adopt(subject, "baseline-run", newIdleHandle(t))
	if got := taskIDs(flowRows(t, ts, "", api.FlowSelect{})); strings.Join(got, ",") != "baseline-run,note-the-head-run" {
		t.Fatalf("flows = %v, want both tasks", got)
	}
	if n := statusFlows(""); n != (api.FlowCount{Tasks: 2, Subjects: 1}) {
		t.Fatalf("status flows = %+v, want two tasks about one subject", n)
	}
	// Narrowed by a charter, the listing is that charter's work.
	if got := taskIDs(flowRows(t, ts, "", api.FlowSelect{Charter: "notes"})); strings.Join(got, ",") != "note-the-head-run" {
		t.Fatalf("flows --charter notes = %v, want the package's task alone", got)
	}

	// A SCOPE ANSWERS FOR ITSELF: a scope nobody has opened has no live work, and says so
	// rather than reporting baseline's.
	if got := flowRows(t, ts, "nobody", api.FlowSelect{}); len(got) != 0 {
		t.Fatalf("scope flows = %+v, want none — a scope is not baseline", got)
	}
	if n := statusFlows("?scope=nobody"); n != (api.FlowCount{}) {
		t.Fatalf("status flows = %+v in a scope with no live work", n)
	}
}

// A flow verb reaches every engine its scope may see, and only those: a scope owns what it
// creates (docs/reference/scopes.md#what-a-scope-owns). Baseline's verbs reach baseline's work
// and a package's; a scope's reach the scope's own; and a scope asked to steer work it merely
// sees through the overlay is refused naming whose it is, rather than answering `affected: 0`
// over work that keeps running — or, worse, stopping baseline's work from a scope.
func TestFlowSteeringActsOnItsOwn(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A package's task, live, and nothing in baseline: the deployment this is about.
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
	subject := entity.RefAt("notes", "seen", "head").String()
	packaged := newIdleHandle(t)
	pkg.Adopt(subject, "note-the-head-run", packaged)
	testerOwns(t, srv, mirror.Ref("note-the-head-run")) // the package's task is its installer's
	about := api.FlowSelect{Subject: subject, Text: "hurry"}

	// Baseline's verbs reach the package's task: the row `verse flow` lists is steerable.
	if code, msg := flowAct(t, ts, "say", tester, "", about); code != 200 || steered(t, msg).Affected != 1 || packaged.count("say") != 1 {
		t.Fatalf("flow say = %d %s, reaching %d; want the package's task", code, msg, packaged.count("say"))
	}
	for _, verb := range []string{"suspend", "resume", "cancel"} {
		if code, msg := flowAct(t, ts, verb, tester, "", api.FlowSelect{Task: "note-the-head-run"}); code != 200 {
			t.Fatalf("flow %s --task = %d %s", verb, code, msg)
		}
	}
	if packaged.count("pause") != 1 || packaged.count("resume") != 1 || packaged.count("cancel") != 1 {
		t.Fatalf("suspend, resume, cancel --task reached the package's task %d, %d, %d times; want once each",
			packaged.count("pause"), packaged.count("resume"), packaged.count("cancel"))
	}

	// Busy in baseline too: the count is of tasks reached, so two.
	base := newIdleHandle(t)
	srv.Eng.Adopt(subject, "baseline-run", base)
	testerOwns(t, srv, mirror.Ref("baseline-run"))
	if code, msg := flowAct(t, ts, "say", tester, "", about); code != 200 || steered(t, msg).Affected != 2 {
		t.Fatalf("flow say = %d %s, want 2 over one subject busy in two engines", code, msg)
	}

	// A SCOPE SEES BASELINE'S WORK AND MAY NOT STEER IT. Refused, naming whose it is, and no
	// handle is touched — the bug this pins is `--scope me` stopping baseline's work.
	for _, c := range []struct {
		verb string
		sel  api.FlowSelect
	}{
		{"say", about},
		{"suspend", api.FlowSelect{Task: "baseline-run"}},
		{"cancel", api.FlowSelect{Task: "baseline-run"}},
	} {
		code, msg := flowAct(t, ts, c.verb, tester, "me", c.sel)
		if code != http.StatusConflict || !strings.Contains(msg, "baseline's") || !strings.Contains(msg, "without --scope") {
			t.Fatalf("flow %s --scope me = %d %q, want 409 naming baseline and saying how to re-address", c.verb, code, msg)
		}
	}
	if base.count("say") != 1 || base.count("pause") != 0 || base.count("cancel") != 0 {
		t.Fatal("a scoped verb reached baseline's task — a scope steered baseline")
	}

	// The scope's own task, about the same subject: the scope's say reaches it and it alone.
	mine := newIdleHandle(t)
	srv.scopeEngine("me").Adopt(subject, "me-run", mine)
	// In the scope, where a spawn there would have written the task and its record.
	if err := entity.Establish(srv.Store.Scope("me"), mirror.Ref("me-run"), "user:"+tester, entity.Ref{}, nil); err != nil {
		t.Fatal(err)
	}
	if code, msg := flowAct(t, ts, "say", tester, "me", about); code != 200 || steered(t, msg).Affected != 1 {
		t.Fatalf("flow say --scope me = %d %s, want 1 over the scope's own task", code, msg)
	}
	if mine.count("say") != 1 || base.count("say") != 1 {
		t.Fatalf("say --scope me reached mine %d, baseline %d; want 1, 1", mine.count("say"), base.count("say"))
	}

	// And baseline may not steer the scope's: once baseline's own work is gone, its say about
	// the subject is refused naming the scope rather than answering zero, and its suspend of
	// the scope's run is refused the same way.
	close(base.ch)
	close(packaged.ch)
	base.ch, packaged.ch = make(chan task.Event), make(chan task.Event) // the cleanups close these
	for i := 0; ; i++ {
		if len(srv.Eng.Flows()) == 0 && len(pkg.Flows()) == 0 {
			break
		}
		if i > 200 {
			t.Fatal("baseline's tasks were not reaped after their buses closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, msg := flowAct(t, ts, "say", tester, "", about)
	if code != http.StatusConflict || !strings.Contains(msg, "scope me's") || !strings.Contains(msg, "--scope me") {
		t.Fatalf("flow say from baseline = %d %q, want 409 naming scope me", code, msg)
	}
	if code, msg := flowAct(t, ts, "suspend", tester, "", api.FlowSelect{Task: "me-run"}); code != http.StatusConflict || !strings.Contains(msg, "scope me's") {
		t.Fatalf("flow suspend --task me-run from baseline = %d %q, want 409 naming scope me", code, msg)
	}
	if mine.count("say") != 1 || mine.count("pause") != 0 {
		t.Fatal("baseline's verb reached the scope's task")
	}

	// A scope nobody has opened, over a subject nobody is working: a normal zero.
	if code, msg := flowAct(t, ts, "say", tester, "nobody", api.FlowSelect{Subject: "demo.pr.99", Text: "hi"}); code != 200 || steered(t, msg).Affected != 0 {
		t.Fatalf("flow say --scope nobody = %d %s, want a plain 0", code, msg)
	}
}

// A verb asks control over every task it reaches, as `task cancel` does of one: a stranger's
// `flow say` over somebody else's work is refused, 403, and no handle is signalled — not even the
// tasks beside it that the stranger could have reached — and so is their suspend or cancel of
// that run, while the owner's reaches.
func TestFlowSteeringAsksControl(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	subject := entity.RefAt("demo", "pr", "1").String()
	// alice's task, with the record that says it is hers, and one whose owner holds `control`
	// out to everyone — a task with no record at all is the system's, and nobody's to steer.
	alices := newIdleHandle(t)
	if _, _, err := entity.As(srv.Store, "user:alice@example.com").Write(entity.Write{
		At: mirror.Ref("alice-run").At(), Value: entity.Obj{"status": "running", "flow": subject}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	srv.Eng.Adopt(subject, "alice-run", alices)
	open := newIdleHandle(t)
	owns(t, srv, "user:carol@example.com",
		[]entity.Grant{{To: map[entity.Level][]string{entity.Control: {entity.Everyone}, entity.Read: {entity.Everyone}}}},
		mirror.Ref("open-run"))
	srv.Eng.Adopt(subject, "open-run", open)

	for _, c := range []struct {
		verb string
		sel  api.FlowSelect
	}{
		{"say", api.FlowSelect{Subject: "demo.pr.*", Text: "hurry"}},
		{"suspend", api.FlowSelect{Task: "alice-run"}},
		{"cancel", api.FlowSelect{Task: "alice-run"}},
	} {
		code, msg := flowAct(t, ts, c.verb, "bob@example.com", "", c.sel)
		if code != http.StatusForbidden || !strings.Contains(msg, "alice-run") {
			t.Fatalf("bob's flow %s = %d %q, want 403 naming alice's task", c.verb, code, msg)
		}
	}
	for _, verb := range []string{"say", "pause", "cancel"} {
		if alices.count(verb)+open.count(verb) != 0 {
			t.Fatalf("bob's refused verbs reached alice's task %d times and the open one %d with %s",
				alices.count(verb), open.count(verb), verb)
		}
	}
	code, msg := flowAct(t, ts, "say", "alice@example.com", "", api.FlowSelect{Subject: "demo.pr.*", Text: "hurry"})
	if code != http.StatusOK || alices.count("say") != 1 || open.count("say") != 1 {
		t.Fatalf("alice's flow say = %d %q, reaching hers %d and the open one %d; want both once",
			code, msg, alices.count("say"), open.count("say"))
	}
}

// An act names something or is refused: a verb that names nothing is 400 with its usage and
// changes nothing, a task combines with no other flag, cancel names one run, and every charter
// about every subject is not a suspension. A charter or a subject given alone names its side, and
// the side left out is every one.
func TestAFlowActIsNarrowedOrRefused(t *testing.T) {
	srv, ts := startedServer(t)
	testerOwns(t, srv, entity.RefAt("demo"), trigger.CharterRef("review"), mirror.Ref("run"))
	h := newIdleHandle(t)
	srv.Eng.Adopt("demo.pr.1", "run", h)
	for _, c := range []struct {
		verb string
		sel  api.FlowSelect
		says string
	}{
		{"suspend", api.FlowSelect{Why: "bare"}, "--charter, --subject or both"},
		{"resume", api.FlowSelect{}, "--charter, --subject or both"},
		{"suspend", api.FlowSelect{Charter: "*", Why: "every charter, and so every subject"}, "every charter about every subject"},
		{"say", api.FlowSelect{Text: "to nobody"}, "--task, --charter or --subject"},
		{"cancel", api.FlowSelect{Subject: "demo.pr.1"}, "--task"},
		{"suspend", api.FlowSelect{Task: "run", Subject: "demo.pr.1", Why: "both"}, "combines with neither"},
		{"suspend", api.FlowSelect{Charter: "*", Subject: "*", Why: "everything"}, "every charter about every subject"},
		{"suspend", api.FlowSelect{Subject: "demo.pr.1", Recursive: true, Why: "beneath nothing"}, "--charter"},
	} {
		code, msg := flowAct(t, ts, c.verb, "", "", c.sel)
		if code != http.StatusBadRequest || !strings.Contains(msg, c.says) {
			t.Errorf("flow %s %+v = %d %q, want 400 saying %q", c.verb, c.sel, code, msg, c.says)
		}
	}
	if h.count("pause")+h.count("say")+h.count("cancel") != 0 || len(srv.susp.List()) != 0 {
		t.Fatal("a refused act reached a task or wrote a suspension")
	}
	for _, c := range []struct {
		sel api.FlowSelect
		id  string
	}{
		{api.FlowSelect{Charter: ":review", Why: "a rule, about everything"}, ":review@*"},
		{api.FlowSelect{Subject: "demo.pr.1", Why: "a subject, for every charter"}, `*:*@demo.pr."1"`},
	} {
		if code, row, msg := suspendAs(t, ts, "suspend", "", "", c.sel); code != http.StatusOK || row.ID != c.id {
			t.Fatalf("flow suspend %+v = %d %+v %s, want %s", c.sel, code, row, msg, c.id)
		}
		lift := c.sel
		lift.Why = ""
		if code, row, msg := suspendAs(t, ts, "resume", "", "", lift); code != http.StatusOK || row.ID != c.id {
			t.Fatalf("flow resume %+v = %d %+v %s, want %s lifted", lift, code, row, msg, c.id)
		}
	}
	if n := len(srv.susp.List()); n != 0 {
		t.Fatalf("%d suspensions stand after each was resumed by the flag that made it", n)
	}
	resp, err := http.Get(ts.URL + "/flows?task=run&subject=demo.pr.1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a listing by task and subject = %s, want 400", resp.Status)
	}
	resp.Body.Close()
}

// A task somebody runs by hand is a flow, as a rule's is: `task run --subject demo.pr.1` is
// listed by `verse flow` and held by `flow suspend --task`, in baseline and in a scope, each from
// its own scope. One about no subject is the flow of its own id.
func TestAHandSpawnedTaskIsAFlow(t *testing.T) {
	srv := askingServer(t) // each run blocks on its first action, so it is live while this looks
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	run := func(id, subject, scope string) {
		t.Helper()
		url := ts.URL + "/tasks"
		if scope != "" {
			url += "?scope=" + scope
		}
		body := map[string]any{"id": id, "doer": map[string]any{"script": "true"}, "policy": stepAsks}
		if subject != "" {
			body["subject"] = subject
		}
		resp := postTony(t, url, body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("task run %s: %s %s", id, resp.Status, raw)
		}
	}
	hold := func(id, scope string) []string {
		t.Helper()
		code, msg := flowAct(t, ts, "suspend", tester, scope, api.FlowSelect{Task: id})
		if code != http.StatusOK {
			t.Fatalf("flow suspend --task %s --scope %q = %d %s", id, scope, code, msg)
		}
		var row api.FlowRow
		if err := codec.Unmarshal([]byte(msg), &row, codec.Tony); err != nil {
			t.Fatal(err)
		}
		return row.HeldBy
	}

	run("base-run", "demo.pr.1", "")
	run("mine-run", "demo.pr.2", "me")
	run("loose-run", "", "")

	subjects := func(rows []api.FlowRow) string {
		var out []string
		for _, row := range rows {
			out = append(out, row.Task+"@"+row.Subject)
		}
		return strings.Join(out, ",")
	}
	if got := subjects(flowRows(t, ts, "", api.FlowSelect{})); got != "base-run@demo.pr.1,loose-run@loose-run" {
		t.Fatalf("flows = %v, want the subject run by hand and the subjectless run as its own id", got)
	}
	if got := subjects(flowRows(t, ts, "me", api.FlowSelect{})); got != "mine-run@demo.pr.2" {
		t.Fatalf("flows --scope me = %v, want the scope's run", got)
	}
	for _, c := range [][2]string{{"base-run", ""}, {"mine-run", "me"}, {"loose-run", ""}} {
		if by := hold(c[0], c[1]); len(by) != 1 || by[0] != "operator" {
			t.Fatalf("flow suspend --task %s is held by %v, want the operator", c[0], by)
		}
	}
}
