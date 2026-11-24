package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
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
		// The root always stands: a viff nobody has written to already carries what the
		// system holds out (viffd.seedPerms), so the root reads before anyone writes.
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

// A WATCH IS REFUSED THE SAME WAY, and says the same thing about the same address.
//
// It required two things. `viff watch` observed the ROOT and filtered, so it never asked the
// store about the address a person typed — it asks at the path now, which also stops every commit
// in the viff crossing for a watch on one slice. And the refusal had to arrive before the stream
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
	// At the depth the arrival is about: `later` alone would report the change at `later`.
	out, err := runCLI(t, "watch", "later", "*", "*", "--wait-if-absent", "--until", `{to: {stage: open}}`)
	if err != nil {
		t.Fatalf("a waiting watch was refused: %v", err)
	}
	if !strings.Contains(out, "stage: open") {
		t.Fatalf("waited but did not report the arrival: %q", out)
	}
}

// A FRAME TOO LONG TO READ IS SAID, not taken for a quiet stream: a node's opening state is one
// frame, and `viff watch mirror task instance` on a viff with a few hundred tasks printed
// nothing and exited 0 (git-issue cmk9g2t4h12ksz7zxsn0).
func TestAWatchSaysWhenAFrameIsTooLong(t *testing.T) {
	defer func(n int) { watchFrameMax = n }(watchFrameMax)
	watchFrameMax = 64 << 10 // no lower than the scan's first buffer, which also bounds a frame
	fakeViff(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {at: big, op: set, rev: 1, to: %q}\n\n", strings.Repeat("x", 128<<10))
	})
	out, err := runCLI(t, "watch", "big")
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("a frame over the limit ended the watch as a quiet one: err=%v out=%q", err, out)
	}
}

// A FRAME OF SEVERAL LINES IS ONE FRAME. A payload with comments goes out as several `data:`
// lines, and --until has to match it and end the watch there, with --trim applied to it.
func TestAWatchReadsACommentedFrameAsOne(t *testing.T) {
	fakeViff(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {at: demo.pr.1 op: set rev: 1 to: {note:\ndata: # why it is here\ndata: kept stage: seen}}\n\n")
		fmt.Fprint(w, "data: {at: demo.pr.2 op: set rev: 2 to: {stage: after}}\n\n")
	})
	out, err := runCLI(t, "watch", "demo", "pr", "*", "--until", `{to: {stage: seen}}`, "--trim", `{to: {stage: null}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stage: seen") || strings.Contains(out, "after") {
		t.Fatalf("--until did not end the watch at the commented frame: %q", out)
	}
	if strings.Contains(out, "kept") {
		t.Fatalf("--trim was not applied to the commented frame: %q", out)
	}
}
