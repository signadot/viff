package main

import (
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
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
		// The root always stands: a verse nobody has written to already carries what the
		// system holds out (versed.seedPerms), so the root reads before anyone writes.
		{"the root", []string{"data"}},
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

// A WILDCARD STANDS WHERE IT REACHES, not where its literal prefix does: a `*` or an access that
// picks nothing is a path nothing stands at, and `{}` with exit 0 stays the answer about a
// filter (git-issue anbz58g6h12krp0sn5n0).
func TestAWildcardThatReachesNothingSaysSo(t *testing.T) {
	serve(t)
	for _, put := range [][]string{
		{"demo", "pr", "1", "--put", `{stage: open, votes: [{choice: a}], labels: {x: true}}`},
		{"demo", "hollow", "1", "--put", `{}`},
	} {
		if _, err := runCLI(t, append([]string{"data"}, put...)...); err != nil {
			t.Fatalf("put %q: %v", put, err)
		}
	}
	for _, steps := range [][]string{
		{"demo", "pr", "[*]"},                // an array access over an object of ids
		{"demo", "pr", "1", "labels", "[*]"}, // …and over an object inside a payload
		{"demo", "*", "2"},                   // no kind holds a 2
		{"demo", "pr", "*", "nope"},          // no id holds the field
		{"demo", "hollow", "1", "*"},         // an entity with no fields
	} {
		for _, extra := range [][]string{nil, {"--if", `{stage: open}`}} {
			args := append(append([]string{"data"}, steps...), extra...)
			if out, err := runCLI(t, args...); err == nil {
				t.Errorf("%q answered %q and exit 0; the wildcard reached nothing", args, out)
			} else if !strings.Contains(err.Error(), "nothing stands at") {
				t.Errorf("%q said %q, want nothing stands at the path", args, err)
			}
		}
	}
	// What a wildcard does reach still reads, and a filter that keeps none of it is the empty
	// answer rather than a not-found.
	for _, args := range [][]string{
		{"data", "demo", "pr", "*"},
		{"data", "demo", "*", "1"},
		{"data", "demo", "pr", "1", "votes", "[*]"},
		{"data", "demo", "pr", "*", "labels"},
		{"data", "demo", "pr", "*", "--if", `{stage: closed}`},
	} {
		if _, err := runCLI(t, args...); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// A WATCH IS REFUSED THE SAME WAY, and says the same thing about the same address.
//
// It required two things. `verse watch` observed the ROOT and filtered, so it never asked the
// store about the address a person typed — it asks at the path now, which also stops every commit
// in the verse crossing for a watch on one slice. And the refusal had to arrive before the stream
// was confirmed: go-tony v0.0.202 asks before confirming, where v0.0.201 failed the watch a moment
// after, by which time an SSE endpoint has committed its status code and cannot go back and say
// 404 (git-issue 7e5gv3n9h12krd10jhn0).
func TestAWatchSaysWhenNothingStandsThere(t *testing.T) {
	serve(t)
	_, err := runCLI(t, "watch", "nope")
	if err == nil {
		t.Fatal("a watch on a path nothing stands at was opened")
	}
	if !strings.Contains(err.Error(), "nothing stands at nope") {
		t.Fatalf("said %q, want it to name the path the way a read of it does", err)
	}
}

// …and --wait-if-absent is how a caller says it meant to wait, which is the case a watch exists
// for: seeing what a task is about to write.
func TestAWatchCanAskToWait(t *testing.T) {
	srv := serve(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		entitytest.Set(t, srv, entity.RefAt("later", "k", "1"), entity.Obj{"stage": "open"}.Payload())
	}()
	out, err := runCLI(t, "watch", "later", "--wait-if-absent", "--until", `{to: {stage: open}}`)
	if err != nil {
		t.Fatalf("a waiting watch was refused: %v", err)
	}
	if !strings.Contains(out, "stage: open") {
		t.Fatalf("waited but did not report the arrival: %q", out)
	}
}
