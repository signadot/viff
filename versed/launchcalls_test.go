package versed

import (
	"strings"
	"testing"

	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// A charter's call list is typed and checked when the trigger FIRES — where its author can see
// the message — rather than on whatever machine the runner happens to be.
func TestLaunchOfTypesACallList(t *testing.T) {
	l, err := launchOf("merge-1", launch.Spec{Calls: []launch.Call{
		{Tool: "run_bash", Input: map[string]any{"command": "git merge --ff-only x"}},
		{Tool: "push", Input: map[string]any{"branch": "main"}, Fatal: &[]string{"err"}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Calls) != 2 || l.Calls[0].Tool != "run_bash" {
		t.Fatalf("calls did not reach the launch: %+v", l.Calls)
	}
	if l.Calls[1].Fatal == nil || (*l.Calls[1].Fatal)[0] != task.FatalErr {
		t.Errorf("fatal = %v", l.Calls[1].Fatal)
	}
	// The field IS the driver: a charter writing a list need not also name one.
	if l.Driver != task.DriverCalls {
		t.Errorf("driver = %q, want %q — the calls select it", l.Driver, task.DriverCalls)
	}
	// And an absent fatal stays absent, because absent means "anything ends the list".
	if l.Calls[0].Fatal != nil {
		t.Errorf("an absent fatal became %v", l.Calls[0].Fatal)
	}
}

// What the firing refuses. Each of these is a charter that cannot mean what it says, and the
// point of checking here is that it fails once, visibly, instead of every firing forever.
func TestLaunchOfRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		spec       launch.Spec
	}{
		{"a contradicting driver", "cannot also be driver", launch.Spec{
			Driver: "llm", Calls: []launch.Call{{Tool: "run_bash"}}}},
		{"an outcome that is not one", "not one of", launch.Spec{
			Calls: []launch.Call{{Tool: "push", Fatal: &[]string{"sideways"}}}}},

		{"a call with no tool", "no tool", launch.Spec{
			Calls: []launch.Call{{Input: map[string]any{"command": "x"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := launchOf("x", tc.spec, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// `driver: calls` written explicitly alongside the list is agreement, not contradiction.
func TestLaunchOfAcceptsTheDriverWrittenOut(t *testing.T) {
	l, err := launchOf("x", launch.Spec{
		Driver: task.DriverCalls, Calls: []launch.Call{{Tool: "run_bash"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Driver != task.DriverCalls {
		t.Errorf("driver = %q", l.Driver)
	}
}
