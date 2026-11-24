package viffd

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
)

// THE VIFF SYSTEM'S EXAMPLE HOLDS: the row of `on` docs/systems/viff.md raises is within the
// task its spawn launches, as the raise checks it (taskBound.holds). Read out of the doc rather than a
// copy, so the example cannot drift into one the raise refuses.
func TestTheViffSystemsExampleOnHoldsWithinItsTask(t *testing.T) {
	src, err := os.ReadFile("../../docs/systems/viff.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	spawnAt := strings.Index(doc, "```tony\nspawn:")
	if spawnAt < 0 {
		t.Fatal("docs/systems/viff.md has no spawn example")
	}
	block := doc[spawnAt+len("```tony\n"):]
	block = block[:strings.Index(block, "```")]
	on := regexp.MustCompile(`(?s)-on '([^']*)'`).FindStringSubmatch(doc)
	if on == nil {
		t.Fatal("docs/systems/viff.md has no -on example")
	}
	parse := func(what, src string) entity.Payload {
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return n
	}
	spec := func(what string, n entity.Payload) launch.Spec {
		s, err := thenSpec(entity.Get(n, "spawn"))
		if err != nil {
			t.Fatalf("%s's spawn%v", what, err)
		}
		return s
	}
	rows := ir.Uncomment(parse("the on", on[1]))
	if rows.Type != ir.ArrayType || len(rows.Values) != 1 {
		t.Fatalf("the doc's on is not one row: %s", on[1])
	}
	task, follows := spec("the task", parse("the task", block)), spec("the row", entity.Get(rows.Values[0], "then"))
	bound := &taskBound{
		launched: &launched{task.Environment, "mirror.runners.viff-runner", task.VirtualKey},
		autonomy: task.Autonomy,
	}
	if err := bound.holds(follows); err != nil {
		t.Errorf("the doc's row is refused at its raise: spawn%v", err)
	}
}
