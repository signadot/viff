package task

import (
	"reflect"
	"sort"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// staysHome is the fields of a Launch that do not cross to a runner, each with why. A field is
// here or it is in SpawnRequest: there is no third place for one.
var staysHome = map[string]string{
	"Scope": "where the task's records land is verse's to know; the runner never sees it",
	"Answerers": "whom a question naming nobody asks is applied where verse poses it, from the " +
		"launch for a hosted task and from the task's record for one a runner reports",
}

// fullLaunch is a launch with every field said, so that one the wire loses reads as lost.
func fullLaunch() Launch {
	return Launch{
		ID: "review@demo.pr.1", Prompt: "review it", Driver: "llm",
		Frame: "you are a reviewer", Retain: 512, Converse: true,
		Tools: []string{"read_file", "run_bash"}, VirtualKey: "vk-123",
		Answer:      ir.FromMap(map[string]*ir.Node{"verdict": ir.FromString("ok")}),
		Calls:       []Call{{Tool: "run_bash", Input: map[string]any{"cmd": "true"}}},
		Vector:      &Vector{Default: Ask},
		Grant:       &Grant{},
		Environment: "verse-repo", Scope: "me", Occurrence: 4217, Answerers: []string{PrincipalAnswerer},
		Provenance: Provenance{Principal: "user:ana@example.com", Subject: "demo.pr.1", Chain: "review"},
	}
}

// EVERY FIELD OF A LAUNCH CROSSES OR IS NAMED AS STAYING HOME. A field added to Launch and to
// neither is a field a remote task runs without, and nothing else would say so: the sender
// would not send it, the runner would read its zero, and both would compile.
func TestEveryLaunchFieldCrossesOrStaysHome(t *testing.T) {
	full := fullLaunch()
	fv := reflect.ValueOf(full)
	for i := 0; i < fv.NumField(); i++ {
		if name := fv.Type().Field(i).Name; fv.Field(i).IsZero() {
			t.Fatalf("fullLaunch leaves %s unsaid, so this test cannot see whether it crosses", name)
		}
	}

	back := reflect.ValueOf(full.Wire().Launch())
	var lost []string
	for i := 0; i < fv.NumField(); i++ {
		name := fv.Type().Field(i).Name
		crossed := reflect.DeepEqual(fv.Field(i).Interface(), back.Field(i).Interface())
		_, home := staysHome[name]
		switch {
		case crossed && home:
			t.Errorf("%s is named as staying home, and crosses", name)
		case !crossed && !home:
			lost = append(lost, name)
		case !crossed && !back.Field(i).IsZero():
			t.Errorf("%s stays home, and arrives as %v", name, back.Field(i).Interface())
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Fatalf("a launch loses %v on its way to a runner: add each to SpawnRequest, Launch.Wire "+
			"and SpawnRequest.Launch, or name it in staysHome with why", lost)
	}
}

// A launch that says nothing sends nothing: each field absent is the runner's own default, and
// an empty grant written out would refuse every action of a task nobody meant to bound.
func TestALaunchThatSaysNothingSendsNothing(t *testing.T) {
	r := Launch{ID: "t1"}.Wire()
	if r.Grant != nil || r.Vector != nil || r.Provenance != nil || r.Answer != nil {
		t.Fatalf("a bare launch crosses as %+v, want nothing said but the id", r)
	}
	node, err := r.ToTonyIR()
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Fields) != 1 || node.Fields[0].String != "id" {
		var said []string
		for _, f := range node.Fields {
			said = append(said, f.String)
		}
		t.Fatalf("a bare launch renders %v, want the id alone", said)
	}
}
