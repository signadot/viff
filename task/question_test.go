package task

import "testing"

// A QUESTION IS INSIDE EVERY GRANT. `read-only` admits reads and nothing else, and a task under
// it can still ask the person it runs for: asking is how the envelope is enforced, not a
// capability it bounds.
func TestAQuestionIsPermittedUnderReadOnly(t *testing.T) {
	ro, ok, err := NamedGrant("read-only")
	if err != nil || !ok {
		t.Fatalf("NamedGrant: %v %v", ok, err)
	}
	q := ToolUse{Name: "ask", Effects: []Effect{{Verb: Question}}}
	if !ro.Permits(q) {
		t.Fatal("read-only refuses a question")
	}
	// …and only a question: an action that also does something else is judged on that.
	mixed := ToolUse{Name: "ask", Effects: []Effect{{Verb: Question}, {Verb: Write, Resource: File("x")}}}
	if ro.Permits(mixed) {
		t.Fatal("read-only admitted a write that came with a question")
	}
}
