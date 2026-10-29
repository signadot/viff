package versed

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// THE VERSE SYSTEM'S EXAMPLE HOLDS: the `then` docs/systems/verse.md raises is within the task
// its spawn launches, as the raise checks it (taskBound.holds). Read out of the doc rather than a
// copy, so the example cannot drift into one the raise refuses.
func TestTheVerseSystemsExampleThenHoldsWithinItsTask(t *testing.T) {
	src, err := os.ReadFile("../docs/systems/verse.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	spawnAt := strings.Index(doc, "```tony\nspawn:")
	if spawnAt < 0 {
		t.Fatal("docs/systems/verse.md has no spawn example")
	}
	block := doc[spawnAt+len("```tony\n"):]
	block = block[:strings.Index(block, "```")]
	then := regexp.MustCompile(`(?s)-then '([^']*)'`).FindStringSubmatch(doc)
	if then == nil {
		t.Fatal("docs/systems/verse.md has no -then example")
	}
	spec := func(what, src string) launch.Spec {
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		s, err := thenSpec(entity.Get(n, "spawn"))
		if err != nil {
			t.Fatalf("%s's spawn%v", what, err)
		}
		return s
	}
	task, follows := spec("the task", block), spec("the then", then[1])
	bound := &taskBound{
		launched: &launched{task.Environment, "verse.runners.verse-runner", task.VirtualKey},
		autonomy: task.Autonomy,
	}
	if err := bound.holds(follows); err != nil {
		t.Errorf("the doc's then is refused at its raise: spawn%v", err)
	}
}
