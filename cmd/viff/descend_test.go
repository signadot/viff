package main

import (
	"strings"
	"testing"
)

// `..` IS A STEP, and it reaches every depth beneath where it stands — the node included. It is
// what asks a question about the viff instance without naming a depth for it: `viff data '..' -if X`
// answers every node X holds of, nested where it stands.
//
// The shell is why it is a whole word: `..` is not expanded, and the name that is two dots is
// the quoted clause `'".."'`.
func TestADescentReadsEveryDepth(t *testing.T) {
	serve(t)
	for _, put := range [][]string{
		{"demo", "pr", "1", "--put", `{stage: open, labels: {urgent: true}}`},
		{"demo", "pr", "2", "--put", `{stage: merged}`},
		{"demo", "note", "--put", `{stage: open}`},
	} {
		if _, err := runCLI(t, append([]string{"data"}, put...)...); err != nil {
			t.Fatalf("seed %v: %v", put, err)
		}
	}

	// Every node beneath demo, nested where it stands.
	out, err := runState(t, "demo", "..", "--paths")
	if err != nil {
		t.Fatalf("data demo .. --paths: %v", err)
	}
	for _, want := range []string{"demo note", "demo pr 1", "demo pr 1 labels urgent", "demo pr 2 stage"} {
		if !has(lines(out), want) {
			t.Errorf("`demo ..` did not reach %q:\n%s", want, out)
		}
	}
	// The filter is asked of every node the descent reaches, at any depth: the two nodes that
	// say they are open are a pr and the note beside it, which no single depth names together.
	out, err = runState(t, "demo", "..", "--if", `{stage: open}`, "--paths")
	if err != nil {
		t.Fatalf("data demo .. --if: %v", err)
	}
	if !has(lines(out), "demo note") || !has(lines(out), "demo pr 1") {
		t.Errorf("`demo .. -if {stage: open}` missed one of the open nodes:\n%s", out)
	}
	if has(lines(out), "demo pr 2") {
		t.Errorf("`demo .. -if {stage: open}` kept the merged pr:\n%s", out)
	}
	// A descent that reaches nothing is a path naming nothing, as any other is.
	if _, err := runState(t, "nosuch", ".."); err == nil || !strings.Contains(err.Error(), "nothing stands at") {
		t.Errorf("`nosuch ..` = %v, want it to say nothing stands there", err)
	}
	// And the NAME that is two dots is the quoted clause, which reaches nothing here.
	if _, err := runState(t, "demo", `".."`); err == nil || !strings.Contains(err.Error(), "nothing stands at") {
		t.Errorf(`data demo '".."' = %v, want a path naming a node called ..`, err)
	}
}
