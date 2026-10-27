package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A TASK HANDS WHAT FOLLOWS ITS YES NO MORE THAN IT HAS ITSELF (taskBound): a row's then that
// spawns is held within the task's own environment and autonomy, at the spawn and when the
// autonomy is replaced.
func TestATasksThenSpawnIsHeldWithinTheTask(t *testing.T) {
	srv := askingServer(t)
	ops := httptest.NewServer(srv.Handler())
	defer ops.Close()
	spawnWith := func(id string, then map[string]any) *http.Response {
		return postTony(t, ops.URL+"/tasks", map[string]any{"id": id, "spawn": map[string]any{
			"script": "true", "environment": stepEnv,
			"autonomy": []map[string]any{
				{"match": map[string]any{"system": "commands"}, "then": map[string]any{"ask": map[string]any{"then": map[string]any{"spawn": then}}}},
				{"then": "ask"},
			}}})
	}
	for name, c := range map[string]struct {
		then map[string]any
		says string
	}{
		"a system the task does not reach": {
			map[string]any{"script": "true", "environment": map[string]any{"systems": map[string]any{"git": map[string]any{"repo": "github.com/signadot/verse", "from": "main", "fetch": map[string]any{}}}}},
			"reaches no git"},
		"a dial above the task's": {
			map[string]any{"script": "true", "environment": openEnv, "autonomy": []map[string]any{{"then": "allow"}}},
			"`then: allow` on every call, where the autonomy it must lie within says ask"},
	} {
		resp := spawnWith("cb-"+strings.Fields(name)[0], c.then)
		body := bodyOf(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: spawn = %s, want refused: %s", name, resp.Status, body)
		} else if !strings.Contains(body, c.says) {
			t.Errorf("%s: refused for another reason: %s", name, body)
		}
	}
	// And within it, it spawns: the network alone, asking as the task does.
	resp := spawnWith("cb-within", map[string]any{"script": "true", "environment": openEnv})
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("a then within the task: spawn = %s", resp.Status)
	}
}
