package versed

import (
	"net/http"
	"strings"
	"testing"
)

// A RULE'S TASK TAKES NO THEN ON ITS AUTONOMY ROWS' ASKS, as a task spawned by hand takes none: the
// task waits on the question. The rule is refused at install, naming the field, rather than at the firing.
func TestARulesSpawnTakesNoThenOnItsAutonomyRowsAsks(t *testing.T) {
	_, ts := asking(t)
	resp := installAs(t, "user:alice@example.com", ts.URL, `{
		name: run-g,
		condition: {subject: {path: "demo.g.*", value: {stage: open}}},
		action: {spawn: {script: "true", environment: {systems: {commands: {run: {}}, network: {mode: open}}},
			autonomy: [{match: {system: commands}, then: {ask: {then: {spawn: {script: "true", environment: {systems: {network: {mode: open}}}}}}}}, {then: allow}]},
			onDone: {patch: {stage: ran}}},
	}`)
	body := bodyOf(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("install = %s, want refused: %s", resp.Status, body)
	}
	if !strings.Contains(body, "then") {
		t.Fatalf("refused without naming the autonomy row's then: %s", body)
	}
}
