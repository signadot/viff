package main

import (
	"strings"
	"testing"
)

// THE SAME COMMAND MEANT TWO OPPOSITE THINGS, and nothing in what a reader typed or saw back
// said which they had got: `votes bo --put '"yes"'` merged a field where `votes` held an object
// and replaced the whole array where it held one, both exit 0, both answering with the value
// written rather than the entity (git-issue mbjpdr6sh12ksa5zgsn0).
func TestAPutDoesNotReplaceTheContainerItDescendsThrough(t *testing.T) {
	serve(t)

	// The ordinary case is untouched: a field lands beside what is already there.
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{votes: {ana: yes}}`); err != nil {
		t.Fatalf("seeding the object case: %v", err)
	}
	if _, err := runCLI(t, "data", "demo", "pr", "1", "votes", "bo", "--put", `"yes"`); err != nil {
		t.Fatalf("a put through an object was refused: %v", err)
	}
	out, err := runCLI(t, "data", "demo", "pr", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ana") || !strings.Contains(out, "bo") {
		t.Fatalf("the merge lost something:\n%s", out)
	}

	// The array case is refused, and says what it ran into — in the address the reader typed,
	// not the store path, which carries the logd root and means nothing to them.
	if _, err := runCLI(t, "data", "demo", "pr", "2", "--put",
		`{votes: [{by: ana}, {by: bo}]}`); err != nil {
		t.Fatalf("seeding the array case: %v", err)
	}
	_, err = runCLI(t, "data", "demo", "pr", "2", "votes", "bo", "--put", `"yes"`)
	if err == nil {
		t.Fatal("a put replaced an array and said nothing")
	}
	for _, want := range []string{"holds an array", `"bo" is not a place in it`, `demo.pr."2".votes`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "verse.demo") {
		t.Fatalf("the refusal leaks the store root: %v", err)
	}
	out, err = runCLI(t, "data", "demo", "pr", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ana") || !strings.Contains(out, "bo") {
		t.Fatalf("the ballots did not survive:\n%s", out)
	}
}

// `--create` SOUNDS LIKE THE SAFE ONE — "only if nothing is there yet, the write loses a race
// rather than clobbering" — and it was the spelling that committed, because `votes.bo` genuinely
// is absent and the loss is at its parent.
func TestCreateDoesNotClobberTheContainer(t *testing.T) {
	serve(t)
	if _, err := runCLI(t, "data", "demo", "pr", "3", "--put",
		`{votes: [{by: ana}, {by: bo}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "data", "demo", "pr", "3", "votes", "bo",
		"--create", "--put", `"yes"`); err == nil {
		t.Fatal("--create committed over an array")
	}
	out, err := runCLI(t, "data", "demo", "pr", "3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ana") {
		t.Fatalf("the ballots did not survive --create:\n%s", out)
	}
}
