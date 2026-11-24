package launch

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A spawn's autonomy is the task's own: which call each row is about, what happens to it, and for
// an ask how many approvals, who answers, how long it stands and what silence settles on, and
// what the question says about itself. The runner carries the rows whole; viff keeps what the
// runner never reads.
func TestASpawnCarriesItsAutonomy(t *testing.T) {
	var spec Spec
	err := codec.Unmarshal([]byte(`{
		agent: {name: viff}, prompt: "review $[subject.ref]",
		autonomy: [
		  {name: release, match: {system: git, verb: push, ref: refs/heads/main}, then: {ask: {need: 2, answerers: ["user:*"],
		   within: "4h", default: deny, labels: {slack: C0EXAMPLE000}}}},
		  {name: shell, match: {system: commands, verb: run}, then: ask},
		  {match: {system: git, verb: push, ref: !glob "refs/heads/viff/*"}, then: deny},
		  {then: allow},
		],
	}`), &spec, codec.Tony)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	p := spec.Autonomy
	if len(p) != 4 || p[0].Then.Ask == nil || p[0].Then.Ask.Need != 2 || p[0].Then.Ask.Default != "deny" ||
		p[0].Then.Ask.Labels["slack"] != "C0EXAMPLE000" || p[0].Then.Ask.Within != "4h" || p[0].Then.Ask.Answerers[0] != "user:*" {
		t.Fatalf("policy = %+v", p)
	}
	if p[0].Match.System != "git" || p[0].Match.Verb != "push" || systems.Words(p[0].Match.Params["ref"]) != "refs/heads/main" {
		t.Fatalf("the first row's match did not survive: %+v", p[0].Match)
	}
	if p[1].Then.Mode != task.ThenAsk || p[1].Then.Ask != nil || p[2].Then.Mode != task.ThenDeny || p[2].Match.System != "git" || p[3].Match != nil || p[3].Then.Mode != task.ThenAllow {
		t.Fatalf("the rows: %+v", p)
	}
	if err := p.Check(); err != nil {
		t.Error(err)
	}
	out, err := codec.Marshal(spec, codec.Tony)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, want := range []string{"need: 2", "within: 4h", "slack: C0EXAMPLE000", "system: git", "verb: push", "then: deny", "then: allow"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("written back without %q:\n%s", want, out)
		}
	}
	bound := bind(t, spec, Binding{"subject": map[string]any{"ref": "demo.pr.1"}})
	bound.Autonomy[0].Then.Ask.Need = 9
	bound.Autonomy[0].Then.Ask.Labels["slack"] = "changed"
	bound.Autonomy[0].Match.Params["ref"] = ir.FromString("changed")
	if p[0].Then.Ask.Need != 2 || p[0].Then.Ask.Labels["slack"] != "C0EXAMPLE000" || systems.Words(p[0].Match.Params["ref"]) != "refs/heads/main" {
		t.Fatal("a bound spec aliases the charter's autonomy")
	}
}
