package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
)

// questionsIn lists the pending gate questions a given view can see.
func questionsIn(store entity.Store, taskID string) []string {
	var out []string
	for _, ent := range entity.Below(store, mirror.IsQuestion, 1, entity.VerseRoot, mirror.AskSystem) {
		if mirror.TaskFrom(ent.Payload) == taskID && entity.Str(ent.Payload, "status") == mirror.QuestionPending {
			out = append(out, ent.Ref.Name())
		}
	}
	return out
}

// A scoped task's gate belongs to the scope that spawned it, and to nobody else.
//
// It did not. `mirror.Reflect` writes `task:instance` into the SPAWNING store, but the gate
// question was published by a single `runnerd.OnQuestion` callback registered once at
// construction, closing over baseline — and runnerd is one runtime shared by baseline and every
// scoped engine. So one run left two records in two different stores, and the consequences
// were all of the wrong kind: one dev's pending decision appeared on every other dev's
// docket, `verse gates` listed a gate whose task `verse ps` would not show, and the question
// named content in the scope the reader had no way to read. The isolation
// a scope promises held for everything except the one record a human is asked to act
// on (git-issue kwxxm8cth12ksn99fhn0).
//
// The fix carries the scope on the launch, so it is known before the task exists rather than
// looked up afterwards. This test is the property, stated from both sides: the scope sees it,
// and baseline does not.
func TestAScopedTasksGateStaysInItsScope(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const scope = "dev-a"
	spawnScoped(t, ts, "scoped-1", scope)

	// The scope's own view: its task's question is here.
	scoped := srv.Store.Scope(scope)
	var ids []string
	deadline := time.After(3 * time.Second)
	for len(ids) == 0 {
		select {
		case <-deadline:
			t.Fatal("the scope never saw its own task's question")
		case <-time.After(5 * time.Millisecond):
		}
		ids = questionsIn(scoped, "scoped-1")
	}

	// And baseline's: it is NOT here. This is the assertion the bug was about — a docket
	// that answers for everybody is worse than one that answers for nobody, because it
	// offers a decision about work its reader cannot see.
	if got := questionsIn(srv.Store, "scoped-1"); len(got) != 0 {
		t.Fatalf("a scoped task's question reached baseline's docket: %v", got)
	}
}

// The other half of the same property: baseline's own tasks still work. A fix that routed
// everything into scopes would pass the test above and break every unscoped deployment,
// which is most of them.
func TestABaselineTasksGateStaysInBaseline(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	spawnScoped(t, ts, "plain-1", "")
	if id := waitQuestion(t, srv, "plain-1"); id == "" {
		t.Fatal("no question id")
	}
}

// spawnScoped posts a task run, in scope when one is named.
func spawnScoped(t *testing.T, ts *httptest.Server, id, scope string) {
	t.Helper()
	url := ts.URL + "/tasks"
	if scope != "" {
		url += "?scope=" + scope
	}
	resp := postTony(t, url, map[string]any{"id": id, "spawn": map[string]any{"script": "true", "environment": stepEnv, "autonomy": stepAsks}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("spawn %s: %s: %s", id, resp.Status, strings.TrimSpace(string(raw)))
	}
}

// firingQuestions lists the pending questions a view can see that NO task raised — a
// firing's gates, which are posed before anything is running and so carry no task.
func firingQuestions(store entity.Store, origin string) []string {
	var out []string
	for _, ent := range entity.Below(store, mirror.IsQuestion, 1, entity.VerseRoot, mirror.AskSystem) {
		from := entity.Get(ent.Payload, mirror.FromField)
		if entity.Str(from, "rule") == origin && entity.Str(ent.Payload, "status") == mirror.QuestionPending {
			out = append(out, ent.Ref.Name())
		}
	}
	return out
}

// The same property, one plane over: a scoped RULE's gate belongs to the scope too.
//
// The task seam above was fixed by carrying the scope on the launch. This one was left as it
// was, on a rationale that reads true and is not — "the registry's own posts are baseline's:
// this seam has no task and therefore no scope to be in". A firing's gate is posed BEFORE any
// task exists, which is why there is no task; the trigger that posed it is nonetheless running
// in a scope. So one registry, shared by baseline and every scoped engine, published every
// dev's rule gates to baseline: `verse gate` in baseline listed a question about
// `demo.ask2.1`, and `verse data demo ask2 1` there answered nothing stands there.
func TestAScopedRulesGateStaysInItsScope(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const scope = "dev-b"
	addScopedRule(t, ts, scope, `{
		name: scoped-ask,
		condition: {subject: {path: "demo.ask.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask, onDenied: {patch: {stage: rejected}}}],
		action: {patch: {stage: reviewing}},
	}`)

	scoped := srv.Store.Scope(scope)
	entitytest.Set(t, scoped, entity.RefAt("demo", "ask", "1"), entity.Obj{"stage": "open"}.Payload())

	var ids []string
	deadline := time.After(3 * time.Second)
	for len(ids) == 0 {
		select {
		case <-deadline:
			t.Fatal("the scope never saw its own rule's question")
		case <-time.After(5 * time.Millisecond):
		}
		ids = firingQuestions(scoped, "scoped-ask")
	}

	// And baseline does not: the subject it names does not exist there, so the question is
	// one its reader cannot answer for a reason they cannot see.
	if got := firingQuestions(srv.Store, "scoped-ask"); len(got) != 0 {
		t.Fatalf("a scoped rule's question reached baseline's docket: %v", got)
	}
}
