package viffd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/parse"

	"go.viff.dev/viff/pkg/task"
)

// A CHARTER'S PUSH IS THE PUSH IT NAMES: a row that allows a push to a branch says whether that
// push may rewrite it or delete it, and one that does not say lets the task do both; a row that
// keeps a task to its own branches says so on `exists` and `own`, the two facts the runner fills,
// and one that does not lets the task push onto another task's. Each charter's spawn
// policy is read from the file and asked about the pushes its comment promises, and the ones it
// refuses.
func TestACharteredPushIsThePushItNames(t *testing.T) {
	// A push as the runner decides it: what the task asked, and what the forge says of the
	// branch. new: not there; own: there, with this task's commit at its tip; theirs: there,
	// with somebody else's.
	push := func(ref, force, del, state string) task.Step {
		exists, own := "true", "false"
		switch state {
		case "new":
			exists = "false"
		case "own":
			own = "true"
		}
		return task.NewStep("git.push", map[string]string{
			"repo": "github.com/signadot/viff", "ref": ref, "force": force, "delete": del, "exists": exists, "own": own})
	}
	for _, c := range []struct {
		file  string
		allow []task.Step
		deny  []task.Step
	}{
		{"../../.viff/charters/comment-drift/drift.tony",
			[]task.Step{push("refs/heads/viff/auto/x", "false", "false", "new"), push("refs/heads/viff/auto/x", "false", "false", "own")},
			[]task.Step{push("refs/heads/viff/auto/x", "false", "false", "theirs"), push("refs/heads/viff/auto/x", "true", "false", "own"),
				push("refs/heads/viff/auto/x", "false", "true", "own"), push("refs/heads/main", "false", "false", "theirs")}},
		{"../../.viff/charters/comment-drift/merge.tony",
			[]task.Step{push("refs/heads/main", "false", "false", "theirs")},
			[]task.Step{push("refs/heads/main", "true", "false", "theirs"), push("refs/heads/main", "false", "true", "theirs")}},
	} {
		policies := chartedPolicies(t, c.file)
		if len(policies) == 0 {
			t.Fatalf("%s: no spawn policy", c.file)
		}
		// The first policy in the file is the spawn that does the work; a later one cleans up.
		p := policies[0]
		for _, s := range c.allow {
			if r, _ := p.Decide(s); r.Then.Mode != task.ThenAllow {
				t.Errorf("%s: %v is %s, want allow", filepath.Base(c.file), s.Fields, r.Then.Mode)
			}
		}
		for _, s := range c.deny {
			if r, _ := p.Decide(s); r.Then.Mode != task.ThenDeny {
				t.Errorf("%s: %v is %s, want deny", filepath.Base(c.file), s.Fields, r.Then.Mode)
			}
		}
	}
}

// chartedPolicies is every `autonomy` list in a charter file, in the order the file has them.
func chartedPolicies(t *testing.T, file string) []task.Autonomy {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parse.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out []task.Autonomy
	var walk func(n *ir.Node)
	walk = func(n *ir.Node) {
		if n == nil {
			return
		}
		for i, v := range n.Values {
			if n.Type == ir.ObjectType && n.Fields[i].String == "autonomy" && v.Type == ir.ArrayType {
				var p task.Autonomy
				for _, row := range v.Values {
					var r task.Row
					if err := r.FromTonyIR(row); err != nil {
						t.Fatalf("%s: %v", file, err)
					}
					p = append(p, r)
				}
				out = append(out, p)
				continue
			}
			walk(v)
		}
	}
	walk(doc)
	return out
}
