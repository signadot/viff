package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
)

// control posts a control write — a task verb or a gate answer — as the tester, in scope when
// one is named, and hands back the status and the body's text.
func control(t *testing.T, ts *httptest.Server, path, scope string, body any) (int, string) {
	t.Helper()
	url := ts.URL + path
	if scope != "" {
		url += "?scope=" + scope
	}
	resp := postTony(t, url, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

// pendingIn waits for the question a task posed, as seen from a view.
func pendingIn(t *testing.T, store entity.Store, taskID string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if ids := questionsIn(store, taskID); len(ids) > 0 {
			return ids[0]
		}
		select {
		case <-deadline:
			t.Fatalf("%s never posed a question", taskID)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A control write reaches the scope's own and is refused at anything inherited: a scope owns
// what it creates (docs/reference/connect.md#what-a-scope-owns, owns.go).
//
// The runtime hosts baseline's tasks and every scope's, and by id a task is reached whatever
// scope asked — so `task cancel --scope me <baseline's task>` cancelled baseline's task, and
// `gate answer --scope me <baseline's question>` was delivered to baseline's awaiter while the
// answer was recorded in the overlay, leaving baseline's record pending over work that had
// moved on. Each is refused now, naming whose the thing is and how to re-address; the scope's
// own are reached as before; and baseline is refused a scope's the same way rather than told
// there is no such task.
func TestControlWritesActOnTheirOwnScope(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	spawnScoped(t, ts, "base-1", "")
	spawnScoped(t, ts, "mine-1", "me")
	baseQ := pendingIn(t, srv.Store, "base-1")
	mineQ := pendingIn(t, srv.Store.Scope("me"), "mine-1")

	refusesNaming := func(code int, msg, whose, redo, what string) {
		t.Helper()
		if code != http.StatusConflict || !strings.Contains(msg, whose) || !strings.Contains(msg, redo) {
			t.Fatalf("%s = %d %q; want 409 naming %s and saying %q", what, code, msg, whose, redo)
		}
	}

	// A task by id, from the wrong scope, each way.
	code, msg := control(t, ts, "/tasks/base-1/say", "me", map[string]any{"instruction": "hurry", "by": tester})
	refusesNaming(code, msg, "baseline's", "without --scope", "say --scope me to baseline's task")
	code, msg = control(t, ts, "/tasks/mine-1/cancel", "", nil)
	refusesNaming(code, msg, "scope me's", "--scope me", "cancel from baseline of the scope's task")
	code, msg = control(t, ts, "/tasks/base-1/cancel", "me", nil)
	refusesNaming(code, msg, "baseline's", "without --scope", "cancel --scope me of baseline's task")

	// A question, from the wrong scope, each way — and the record is untouched: baseline's
	// question is still pending, which is the split this pins (delivered there, recorded here).
	answer := func(id string) map[string]any { return map[string]any{"id": id, "by": tester, "choice": "proceed"} }
	code, msg = control(t, ts, "/gate/resolve", "me", answer(baseQ))
	refusesNaming(code, msg, "baseline's", "without --scope", "answer --scope me to baseline's question")
	code, msg = control(t, ts, "/gate/escalate", "me", map[string]any{"id": baseQ, "by": tester})
	refusesNaming(code, msg, "baseline's", "without --scope", "escalate --scope me of baseline's question")
	code, msg = control(t, ts, "/tasks/base-1/resolve", "me", answer(baseQ))
	refusesNaming(code, msg, "baseline's", "without --scope", "task resolve --scope me of baseline's question")
	code, msg = control(t, ts, "/gate/resolve", "", answer(mineQ))
	refusesNaming(code, msg, "scope me's", "--scope me", "answer from baseline of the scope's question")
	if got := questionsIn(srv.Store, "base-1"); len(got) != 1 || got[0] != baseQ {
		t.Fatalf("baseline's question after the refusals = %v, want it still pending", got)
	}
	if got := questionsIn(srv.Store.Scope("me"), "mine-1"); len(got) != 1 || got[0] != mineQ {
		t.Fatalf("the scope's question after the refusals = %v, want it still pending", got)
	}

	// The scope's own, from the scope: reached, and settled in the scope's record.
	if code, msg := control(t, ts, "/gate/resolve", "me", answer(mineQ)); code != http.StatusNoContent {
		t.Fatalf("answer --scope me to the scope's own question = %d %q, want 204", code, msg)
	}
	deadline := time.After(3 * time.Second)
	for len(questionsIn(srv.Store.Scope("me"), "mine-1")) != 0 {
		select {
		case <-deadline:
			t.Fatal("the scope's question is still pending after its own scope answered it")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// And baseline's own, from baseline: reached, as ever. (A say re-prompts the task, which
	// drops the question it was holding — so it comes after the question has served its turn.)
	if code, msg := control(t, ts, "/tasks/base-1/say", "", map[string]any{"instruction": "hurry", "by": tester}); code != http.StatusNoContent {
		t.Fatalf("say to baseline's own task = %d %q, want 204", code, msg)
	}
	if code, msg := control(t, ts, "/tasks/base-1/cancel", "", nil); code != http.StatusNoContent {
		t.Fatalf("cancel of baseline's own task = %d %q, want 204", code, msg)
	}
}

// A charter removal is a control write like the others: from a scope it reaches the scope's
// own rules and is refused at an inherited one, naming baseline; from baseline it is refused
// at a scope's, naming the scope. Neither is "no such rule", which the asking engine could
// say truthfully and the verse could not.
func TestCharterRemovalActsOnItsOwnScope(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	addRule(t, ts, `{
		name: review,
		condition: {subject: {path: [demo, pr, "*"], value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	addScopedRule(t, ts, "me", `{
		name: only-mine,
		condition: {subject: {path: [demo, pr, "*"], value: {stage: open}}},
		action: {patch: {stage: mine}},
	}`)
	remove := func(path, scope string) (int, string) {
		t.Helper()
		url := ts.URL + path
		if scope != "" {
			if strings.Contains(path, "?") {
				url += "&scope=" + scope
			} else {
				url += "?scope=" + scope
			}
		}
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(raw))
	}

	for _, path := range []string{"/charter/review", "/charter?path=rules.review"} {
		if code, msg := remove(path, "me"); code != http.StatusConflict || !strings.Contains(msg, "baseline's") || !strings.Contains(msg, "without --scope") {
			t.Fatalf("DELETE %s --scope me = %d %q, want 409 naming baseline", path, code, msg)
		}
	}
	if code, msg := remove("/charter/only-mine", ""); code != http.StatusConflict || !strings.Contains(msg, "scope me's") || !strings.Contains(msg, "--scope me") {
		t.Fatalf("DELETE /charter/only-mine from baseline = %d %q, want 409 naming scope me", code, msg)
	}
	if !srv.Eng.Knows("review") || !srv.engFor("me").Knows("only-mine") {
		t.Fatal("a refused removal removed something")
	}
	if code, msg := remove("/charter/nobody-has-this", "me"); code != http.StatusNotFound {
		t.Fatalf("DELETE of a rule nobody holds = %d %q, want 404", code, msg)
	}

	// Each scope's own, from itself: removed.
	if code, msg := remove("/charter/only-mine", "me"); code != http.StatusNoContent {
		t.Fatalf("DELETE /charter/only-mine --scope me = %d %q, want 204", code, msg)
	}
	if code, msg := remove("/charter/review", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE /charter/review = %d %q, want 204", code, msg)
	}
}
