package main

import (
	"strings"
	"testing"
)

// A READ OF A PATH NOTHING STANDS AT SAYS SO, at every depth, and exits non-zero.
//
// Every one of these answered exit 0 before: `{}` for the root and for a slice, `null` for an
// entity, and an error about tony's own types for a field of one — four answers for one fact,
// none of them a not-found (git-issue 7e5gv3n9h12krd10jhn0).
func TestAReadSaysWhenNothingStandsThere(t *testing.T) {
	serve(t)
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"the root of a verse nobody has written to", "the verse is empty", []string{"data"}},
		{"a system", "nothing stands at nope", []string{"data", "nope"}},
		{"a kind", "nothing stands at demo.pr", []string{"data", "demo", "pr"}},
		{"an entity", `nothing stands at demo.pr."1"`, []string{"data", "demo", "pr", "1"}},
		{"as one path", `nothing stands at demo.pr."1"`, []string{"data", "--at", `demo.pr."1"`}},
		{"a listing of paths", "nothing stands at nope", []string{"data", "nope", "--paths"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("answered %q and exit 0; a read of what is not there must not succeed", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("said %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// …and a path that DOES stand still reads, including when it holds nothing a filter keeps.
func TestAReadOfWhatStandsStillAnswers(t *testing.T) {
	serve(t)
	if _, err := runCLI(t, "data", "demo", "pr", "1", "--put", `{stage: open}`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"the entity", []string{"data", "demo", "pr", "1"}},
		{"its slice", []string{"data", "demo", "pr"}},
		{"the root, now that something is written", []string{"data"}},
	} {
		if _, err := runCLI(t, tc.args...); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// A predicate that keeps nothing is an empty answer, not a not-found: the path stands, and
	// the question was about the filter.
	if _, err := runCLI(t, "data", "demo", "pr", "--if", `{stage: closed}`); err != nil {
		t.Errorf("a filter that kept nothing reported not-found: %v", err)
	}
}
