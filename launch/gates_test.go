package launch

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// A spawn's `gates:` are read as `task vector --file` reads them, question and all, and come
// back as written.
func TestASpawnCarriesItsGates(t *testing.T) {
	var spec Spec
	err := codec.Unmarshal([]byte(`{
		driver: llm, prompt: "review $subject", vector: auto,
		gates: [
		  {name: shell, mode: ask, match: {verbs: [exec]}, need: 2, deadline: {within: "4h", default: deny}},
		  {name: route, mode: ask, match: {tools: [deploy]}, options: [{id: canary}, {id: full, need: 2}]},
		],
	}`), &spec, codec.Tony)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(spec.Gates) != 2 || spec.Gates[0].Need != 2 || spec.Gates[0].Deadline == nil ||
		spec.Gates[0].Deadline.Default != task.Deny || spec.Gates[1].Options[1].Need != 2 {
		t.Fatalf("gates = %+v", spec.Gates)
	}
	if spec.Gates[0].Mode != task.Ask || spec.Gates[0].Match.Verbs[0] != task.Exec {
		t.Fatalf("the first gate's rule did not survive: %+v", spec.Gates[0])
	}
	out, err := codec.Marshal(spec, codec.Tony)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, want := range []string{"need: 2", "within: 4h", "id: canary"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("written back without %q:\n%s", want, out)
		}
	}
	// Bound, the gates are the spawn's own: a rule is not a template, and a bound spec must not
	// alias the charter's slice.
	bound := spec.Bind(Binding{"subject": "demo.pr.1"})
	bound.Gates[0].Need = 9
	if spec.Gates[0].Need != 2 {
		t.Fatal("Bind aliased the charter's gates")
	}
}
