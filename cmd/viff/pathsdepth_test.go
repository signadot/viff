package main

import (
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
)

// A DEPTH IS COUNTED FROM THE ADDRESS, which is what makes the same number readable at every
// level: walking one level deeper is a step, not a step and a bigger number. Counted from the
// root it was two edits — `data a --paths --depth 2`, then `data a b --paths --depth 3` — and a
// script that bounded a walk carried the sum of an address written three lines above it.
func TestDepthIsTheSameNumberAtEveryLevel(t *testing.T) {
	store := serve(t)
	write := func(segs ...string) {
		t.Helper()
		if _, _, err := store.Write(entity.Write{
			At: entity.RefAt(segs...).At(), Value: entity.Obj{"stage": "open"}.Payload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("demo", "pr", "1")
	write("demo", "pr", "2")

	// One level under `demo`: its children, and nothing under them.
	top, err := runState(t, "demo", "--paths", "--depth", "1")
	if err != nil {
		t.Fatalf("data demo --paths --depth 1: %v", err)
	}
	if got := lines(top); !has(got, "demo pr") || has(got, "demo pr 1") {
		t.Errorf("one level under demo is `demo pr` alone; got %q", got)
	}
	// THE SAME NUMBER one level down — the records, without their fields. Under the old counting
	// this needed --depth 3.
	next, err := runState(t, "demo", "pr", "--paths", "--depth", "1")
	if err != nil {
		t.Fatalf("data demo pr --paths --depth 1: %v", err)
	}
	got := lines(next)
	for _, want := range []string{"demo pr 1", "demo pr 2"} {
		if !has(got, want) {
			t.Errorf("one level under demo pr lost %q; got %q", want, got)
		}
	}
	if has(got, "demo pr 1 stage") {
		t.Errorf("one level under demo pr walked into the payload: %q", got)
	}
}

// 0 IS THE ADDRESS ALONE, so a listing bounded there stands where the path does and answers
// nothing beneath it. No bound at all is the flag left off, never a number.
func TestDepthZeroIsTheAddressAlone(t *testing.T) {
	store := serve(t)
	if _, _, err := store.Write(entity.Write{
		At: entity.RefAt("demo", "pr", "1").At(), Value: entity.Obj{"stage": "open"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	// A path naming one node prints nothing at depth 0: the node is the address the reader typed.
	out, err := runState(t, "demo", "pr", "--paths", "--depth", "0")
	if err != nil {
		t.Fatalf("data demo pr --paths --depth 0: %v", err)
	}
	if got := lines(out); len(got) != 0 {
		t.Errorf("--depth 0 under a named node answered %q, want nothing beneath it", got)
	}
	// A wildcard prints each node it REACHED, and at depth 0 nothing under them — which is how a
	// script lists the instances themselves (scripts/attached-resolver.sh).
	out, err = runState(t, "demo", "pr", "*", "--paths", "--depth", "0")
	if err != nil {
		t.Fatalf("data demo pr '*' --paths --depth 0: %v", err)
	}
	got := lines(out)
	if !has(got, "demo pr 1") {
		t.Errorf("--depth 0 lost the node the path reached: %q", got)
	}
	if has(got, "demo pr 1 stage") {
		t.Errorf("--depth 0 walked beneath what the path reached: %q", got)
	}
}

// A DEPTH THAT MEANS NOTHING IS REFUSED, never dropped: a negative one bounds nothing, and one
// with no listing to bound is a reader believing they asked for something. Both say which flag.
func TestAMeaninglessDepthIsRefused(t *testing.T) {
	serve(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"demo", "--paths", "--depth", "-1"}, "never negative"},
		{[]string{"demo", "--depth", "1"}, "--paths"},
	} {
		_, err := runState(t, tc.args...)
		if err == nil {
			t.Fatalf("data %s was admitted", strings.Join(tc.args, " "))
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("data %s: %v — want it to say %q",
				strings.Join(tc.args, " "), err, tc.want)
		}
	}
}

// THE DEPTH IS THE STORE'S, handed over as it was given, for a path with a wildcard or a descent
// as for a place. A depth is one budget for the whole path, counted from the node the path names,
// and only what a `..` TAKES spends it — so `demo .. stage` at depth 1 reaches `demo a stage`,
// the descent having taken `a` and the literal `stage` costing nothing. Counted here instead, as
// segments past the literal prefix, that address was three long against a ceiling of two and was
// dropped: viff and the store disagreed about the one number they share.
func TestADescentsDepthIsTheStores(t *testing.T) {
	store := serve(t)
	for _, segs := range [][]string{{"demo", "stage"}, {"demo", "a", "stage"}, {"demo", "a", "b", "stage"}} {
		if _, _, err := store.Write(entity.Write{At: entity.RefAt(segs...).At(), Value: entity.Val("open")}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runState(t, "demo", "..", "stage", "--paths", "--depth", "1")
	if err != nil {
		t.Fatalf("data demo .. stage --paths --depth 1: %v", err)
	}
	got := lines(out)
	for _, want := range []string{"demo stage", "demo a stage"} {
		if !has(got, want) {
			t.Errorf("depth 1 lost %q, which the descent reaches within its budget: %q", want, got)
		}
	}
	if has(got, "demo a b stage") {
		t.Errorf("depth 1 reached an address the descent needs two segments for: %q", got)
	}
}

// A predicate is asked of what the path REACHED, and the listing is what it kept and what is
// beneath that — the same set `--if` answers as a document, as addresses, without reading it.
func TestAFilteredListingIsWhatTheFilterKeptAndBeneath(t *testing.T) {
	store := serve(t)
	for id, stage := range map[string]string{"1": "open", "2": "closed"} {
		if _, _, err := store.Write(entity.Write{
			At: entity.RefAt("demo", "pr", id).At(), Value: entity.Obj{"stage": stage}.Payload(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runState(t, "demo", "pr", "*", "--paths", "--if", "{stage: open}")
	if err != nil {
		t.Fatalf("filtered listing: %v", err)
	}
	got := lines(out)
	if !has(got, "demo pr 1") || !has(got, "demo pr 1 stage") {
		t.Errorf("the listing lost what the filter kept, or what is beneath it: %q", got)
	}
	if has(got, "demo pr 2") {
		t.Errorf("the listing names a node the filter did not keep: %q", got)
	}
}
