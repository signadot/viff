package harness

import (
	"strings"
	"testing"
)

// The advice a model gets about waiting must match what the loop does, because the tool
// description is the only place it learns any of it.
//
// task.Task joins children where a turn ENDS — `!more` with children outstanding blocks,
// folds the completion and calls the driver again — so ending the turn is the correct wait and
// a free one. Told only that a result "comes back when it finishes", a model reaches for the
// wait it can see: `run_bash {command: "sleep 120; echo done"}`, at a model round trip per
// poll. A staging review spent eighteen minutes and twenty-five boundaries mostly asleep that
// way (git-issue 91r674qzh12ks35vfnn0). task.TestEndingATurnIsHowAParentWaits pins the
// mechanism; this pins that somebody is told.
func TestCallAgentSaysHowToWait(t *testing.T) {
	tds, err := selectTools(nil)
	if err != nil {
		t.Fatalf("selectTools: %v", err)
	}
	var desc string
	for _, td := range tds {
		if td.Function.Name == "call_agent" {
			desc = td.Function.Description
		}
	}
	if desc == "" {
		t.Fatal("call_agent is not declared")
	}
	for _, must := range []string{"END YOUR TURN", "Never sleep"} {
		if !strings.Contains(desc, must) {
			t.Errorf("call_agent does not say %q, so a model that wants to wait will invent a "+
				"wait, and the only one it can see is sleeping:\n%s", must, desc)
		}
	}
}
