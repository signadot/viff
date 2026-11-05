package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// ONLY A PERSON'S ASK CARRIES WHAT FOLLOWS ITS ANSWER (consequence.go): a rule's gate and an
// autonomy row's ask are waited on by what asked, so neither takes an `on`. What a person's ask
// runs is pinned in askcharter_test.go and askthen_test.go.

// waitValue waits for a field of a record to hold a value.
func waitValue(t *testing.T, srv *Server, ref entity.Ref, field, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok && entity.Str(e.Payload, field) == want {
			return
		}
		select {
		case <-deadline:
			e, _, _ := srv.Store.Get(ref)
			t.Fatalf("%s never carried %s: %q; it says %v", ref, field, want, entity.ToObj(e.Payload))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A RULE'S GATE STARTS NOTHING OF ITS OWN: what a rule does about a yes is its action, and about
// a no its gate's onDenied. A gate writing `on` is refused at install, naming the field.
func TestARulesGateTakesNoOn(t *testing.T) {
	_, ts := asking(t)
	resp := installAs(t, "user:alice@example.com", ts.URL, `{name: r,
		condition: {subject: {path: "demo.e.*", value: {stage: open}}},
		gates: [{name: approval, mode: ask, on: [{then: {spawn: {prompt: "announce", environment: {systems: {network: {mode: open}}}}}}]}],
		action: {patch: {stage: reviewing}}}`)
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a gate with on: install = %s, want refused: %s", resp.Status, body)
	} else if !strings.Contains(body, `"on"`) {
		t.Errorf("a gate with on: refused without naming it: %s", body)
	}
}

// A TASK'S QUESTION STARTS NOTHING OF ITS OWN: the task waits on it, and what it does about a yes
// is its next step. An autonomy row's ask writing `on` is refused at the spawn, naming the field,
// before anything starts.
func TestAnAutonomyRowsAskTakesNoOn(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	const id = "cq-0"
	resp := postTony(t, ops.URL+"/tasks", map[string]any{"id": id, "spawn": map[string]any{
		"script": "true", "environment": stepEnv,
		"autonomy": []map[string]any{
			{"match": map[string]any{"system": "commands"}, "then": map[string]any{"ask": map[string]any{
				"on": []map[string]any{{"then": map[string]any{"spawn": map[string]any{"prompt": "log the step", "environment": map[string]any{"systems": map[string]any{"network": map[string]any{"mode": "open"}}}}}}}}}},
			{"then": "ask"},
		}}})
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an autonomy row's ask with on: spawn = %s, want refused: %s", resp.Status, body)
	} else if !strings.Contains(body, `"on"`) {
		t.Errorf("an autonomy row's ask with on: refused without naming it: %s", body)
	}
	if _, ok, _ := srv.Store.Get(mirror.Ref(id)); ok {
		t.Errorf("an autonomy row's ask with on: the refused spawn left a task record")
	}
}
