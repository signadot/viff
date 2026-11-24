package runner

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A ROW THAT CAN MATCH NOTHING THE ENVIRONMENT GIVES IS REFUSED, where the launch is checked: one
// with no value in common with any shape never fires. A row wider than the environment is not, nor
// one that overlaps a shape without containing it or being contained by it; nor the default row.
func TestARowMustReachTheEnvironment(t *testing.T) {
	env := systems.Environment{Systems: systems.Systems{"git": {{
		Attributes: map[string]string{"repo": "github.com/acme/app"},
		Verbs: map[string][]systems.Shape{
			"fetch": {{}},
			"push":  {{"ref": systems.Glob("refs/heads/viff/*")}},
		},
	}}}}
	push := func(ref *ir.Node) task.Row {
		return task.Row{Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ref}}, Then: task.Then{Mode: task.ThenAllow}}
	}
	dflt := task.Row{Then: task.Then{Mode: task.ThenAsk}}
	for _, c := range []struct {
		row  task.Row
		says string // "" = accepted
	}{
		{push(systems.Glob("refs/tags/*")), "autonomy[0]: matches nothing the environment gives (system=git verb=push ref=!glob refs/tags/*)"},
		{push(systems.Regexp("^refs/heads/")), ""}, // wider: the environment bounds it
		{push(systems.Glob("refs/*/viff/x")), ""},  // overlaps refs/heads/viff/*, contains it neither way
		{push(ir.FromString("refs/heads/viff/fix")), ""},
		{task.Row{Match: &task.Match{System: "git"}, Then: task.Then{Mode: task.ThenAsk}}, ""},
		{task.Row{Match: &task.Match{System: "git", Params: map[string]*ir.Node{"repo": systems.Glob("github.com/other/*")}}, Then: task.Then{Mode: task.ThenDeny}}, "matches nothing the environment gives (system=git repo=!glob github.com/other/*)"},
		{task.Row{Match: &task.Match{System: "slack"}, Then: task.Then{Mode: task.ThenAllow}}, "give it in environment.systems.slack"},
	} {
		err := CheckReach(task.Autonomy{c.row, dflt}, env)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%+v: refused: %v", c.row.Match, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%+v: %v, want it refused saying %q", c.row.Match, err, c.says)
		}
	}
	// Not where a launch is checked: a launch is bound, and a row naming one repository of a
	// charter that binds another is dead for that run, not wrong. The rule is held where a spawn
	// is written, on the environment as written.
	l := &Launch{ID: "x", Script: "echo", Environment: env, Autonomy: task.Autonomy{push(systems.Glob("refs/tags/*")), dflt}}
	if err := l.Check(); err != nil {
		t.Errorf("a bound launch with a row that never fires is refused: %v", err)
	}
}
