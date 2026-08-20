package main

import (
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

func seedVoted(t *testing.T, store entity.Store) {
	t.Helper()
	v, err := codec.ParseNode([]byte(`{"[0]": iAmAField, votes: [{by: ana}, {by: bo}]}`), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write(entity.Write{At: entity.RefAt("demo", "pr", "1").At(), Value: v}); err != nil {
		t.Fatal(err)
	}
}

// A STEP BEGINNING `[`, `{` OR `(` IS THAT ACCESS, and a read reaches the element.
//
// The step form used to be able to say only "a field of this name", so an array element needed
// the kinded-path spelling. It says both now, by the leading character — one word, one place,
// no splitting.
func TestAnArrayElementIsAddressedAsAStep(t *testing.T) {
	store := serve(t)
	seedVoted(t, store)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"data", "demo", "pr", "1", "votes", "[0]"}, "by: ana"},
		{[]string{"data", "demo", "pr", "1", "votes", "[1]", "by"}, "bo"},
		// THE QUOTED CLAUSE: the field literally named `[0]`, which the access clause would
		// otherwise have taken.
		{[]string{"data", "demo", "pr", "1", `"[0]"`}, "iAmAField"},
	} {
		out, err := runCLI(t, c.args...)
		if err != nil || strings.TrimSpace(out) != c.want {
			t.Errorf("%q = (%q, %v), want %q", c.args[1:], strings.TrimSpace(out), err, c.want)
		}
	}
}

// AND A WRITE LANDS IN PLACE, which is the whole reason to offer it: the element changes and
// the array stays an array. It used to be refused here, on a hazard that is gone.
func TestAWriteAtAnArrayElementLandsInPlace(t *testing.T) {
	store := serve(t)
	seedVoted(t, store)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "votes", "[0]", "by", "--put", `"zed"`); err != nil {
		t.Fatalf("writing an element: %v", err)
	}
	e, _, _ := store.Get(entity.RefAt("demo", "pr", "1"))
	got, _ := codec.Marshal(e.Payload, codec.Tony)
	for _, want := range []string{"by: zed", "by: bo"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("after the write the payload is\n%s\nwant it to still carry %q", got, want)
		}
	}
	if strings.Contains(string(got), "votes:\n  ") && !strings.Contains(string(got), "- by:") {
		t.Errorf("the array became an object:\n%s", got)
	}
}

// THE SUBSTRATE GUARDS THE INDEX, which is why the CLI does not have to. An index past the end
// is refused at the patch, naming the length — the hazard that justified refusing these writes
// until go-tony v0.0.149.
func TestAnIndexPastTheEndIsRefused(t *testing.T) {
	store := serve(t)
	seedVoted(t, store)
	_, err := runCLI(t, "data", "demo", "pr", "1", "votes", "[9]", "--put", `"oops"`)
	if err == nil {
		t.Fatal("a write past the end was accepted")
	}
	if !strings.Contains(err.Error(), "past the end") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	e, _, _ := store.Get(entity.RefAt("demo", "pr", "1"))
	got, _ := codec.Marshal(e.Payload, codec.Tony)
	if !strings.Contains(string(got), "by: ana") {
		t.Errorf("a refused write damaged the array:\n%s", got)
	}
}

// A PRECONDITION AT AN ACCESS IS REFUSED, and says why. It nests by field name and an index has
// no name to nest under — an implementation limit, not a principle, and refusing beats checking
// the entity instead of the element.
func TestAGuardAtAnArrayElementIsRefused(t *testing.T) {
	store := serve(t)
	seedVoted(t, store)
	for _, flag := range []string{"--create", "--update"} {
		_, err := runCLI(t, "data", "demo", "pr", "1", "votes", "[0]", flag, "--put", `"guard"`)
		if err == nil {
			t.Errorf("%s at an array element was accepted", flag)
			continue
		}
		if !strings.Contains(err.Error(), "precondition") {
			t.Errorf("%s: the refusal does not name the reason: %v", flag, err)
		}
	}
}
