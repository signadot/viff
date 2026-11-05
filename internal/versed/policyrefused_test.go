package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/stock"
	"github.com/signadot/verse/pkg/systems/systemstest"
	"github.com/signadot/verse/pkg/task"
)

// A CHANGE THE RUNNER REFUSES NEVER HAPPENED, verse's part of it included: what a row's
// silence settles on, and whom it asks, stay the policy's that stands, so the task's next
// question is not posed under a policy its runner turned down.
func TestARefusedPolicyLeavesVersesPart(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := serverWith(t, func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(l.Environment, l.Autonomy, reg), l.Environment), &blockedDriver{release: release}, okExecutor{})
	}, entitytest.Store(t))
	describeLocal(t, srv, stock.Open().Describe([]string{runner.NetworkOpen}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	t.Cleanup(func() {
		if tk := localOf(t, srv).Lookup("held"); tk != nil {
			tk.Cancel("the test is over", "test")
		}
	})
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "held", "spawn": map[string]any{"script": "true", "environment": map[string]any{"systems": map[string]any{"network": map[string]any{"mode": "open"}}}, "autonomy": []map[string]any{
		{"name": "deploy", "then": map[string]any{"ask": map[string]any{"within": "1h", "default": "deny"}}},
	}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spawn = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	// A row on commands, which the local runner does not intercept for a script: it refuses it.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/tasks/held/autonomy", strings.NewReader(
		`{autonomy: [{match: {system: commands}, then: deny}, {name: deploy, then: {ask: {within: 1m, default: proceed}}}]}`))
	req.Header.Set("Content-Type", "application/tony")
	resp, err := sendAsTester(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusNoContent {
		t.Fatalf("the runner took a policy with a row on a system it does not intercept")
	}
	resp.Body.Close()
	row, ok := srv.hostedOf("held").autonomy.Named("deploy")
	if !ok || row.Then.Ask == nil || row.Then.Ask.Default != task.Deny {
		t.Fatalf("after a refused change verse's part of the policy is %v, want the launch's default deny", srv.hostedOf("held").autonomy)
	}
}
