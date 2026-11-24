package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/codec"
)

// frames is what the server sends, in order. The one --until waits for is not the last, so a
// watch that ran to the end of the stream would print more than a watch that stopped.
var frames = []string{
	`{at: "demo.pr.1", op: set, to: {stage: open}, rev: 1}`,
	`{at: "demo.pr.1", op: set, to: {stage: merged}, rev: 2}`,
	`{at: "demo.pr.2", op: set, to: {stage: open}, rev: 3}`,
}

// watchServer serves /watch with the frames above. `hold` decides what happens after the last
// one: holding the connection open is the live viff instance — a watch ends when the watcher stops
// caring — while closing is a store that has finished talking.
func watchServer(t *testing.T, hold bool) *httptest.Server {
	t.Helper()
	h := http.NewServeMux()
	h.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		sse, err := codec.NewSSE(w, r)
		if err != nil {
			t.Errorf("sse: %v", err)
			return
		}
		for _, f := range frames {
			n, err := codec.ParseNode([]byte(f), codec.Tony)
			if err != nil {
				t.Errorf("parse %s: %v", f, err)
				return
			}
			if err := sse.SendNode(n); err != nil {
				return // the client left, which on the --until case is the point
			}
		}
		if hold {
			<-r.Context().Done()
		}
	})
	return httptest.NewServer(h)
}

func outLines(out string) []string {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// --until ENDS THE STREAM, and the matching frame is the last thing printed.
//
// The server here never closes, so returning at all is the flag working: without it this is
// the watch that runs until somebody presses ^C.
func TestUntilStopsTheStreamAndPrintsTheMatch(t *testing.T) {
	ts := watchServer(t, true)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--until", `{to: {stage: merged}}`, "demo")
	if err != nil {
		t.Fatalf("watch --until: %v (out=%q)", err, out)
	}
	got := outLines(out)
	if len(got) != 2 {
		t.Fatalf("printed %d frames, want the two up to and including the match:\n%s", len(got), out)
	}
	// PRINTED, not swallowed. This is what puts the answer on stdout and lets the exit code go
	// on meaning what it means everywhere else.
	if !strings.Contains(got[len(got)-1], "merged") {
		t.Errorf("the matching frame is not the last line printed:\n%s", out)
	}
	if strings.Contains(out, "demo.pr.2") {
		t.Errorf("the stream ran past the match:\n%s", out)
	}
}

// A STREAM THAT ENDS WITHOUT EVER MATCHING IS EXIT 0, with everything it saw on stdout. An
// empty answer is a true answer to a question here, the same as it is for --if — and asking
// "did it happen" is reading the output, not the exit status.
func TestUntilThatNeverMatchesIsNotAnError(t *testing.T) {
	ts := watchServer(t, false)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--until", `{to: {stage: abandoned}}`, "demo")
	if err != nil {
		t.Fatalf("a watch that matched nothing failed: %v (out=%q)", err, out)
	}
	if n := len(outLines(out)); n != len(frames) {
		t.Errorf("printed %d frames, want all %d:\n%s", n, len(frames), out)
	}
}

// Without --until a watch is what it always was: everything, until the server stops.
func TestWatchWithoutUntilIsUnchanged(t *testing.T) {
	ts := watchServer(t, false)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "demo")
	if err != nil {
		t.Fatalf("watch: %v (out=%q)", err, out)
	}
	if n := len(outLines(out)); n != len(frames) {
		t.Errorf("printed %d frames, want all %d:\n%s", n, len(frames), out)
	}
}

// --trim SHAPES THE OUTPUT, it does not decide when the stream is over. The projection here
// throws away the very key --until is waiting for, and the watch must still stop at it —
// otherwise a display flag silently changes what a watch waits for.
func TestTrimDoesNotChangeWhatUntilWaitsFor(t *testing.T) {
	ts := watchServer(t, true)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--trim", "{at rev}", "--until", `{to: {stage: merged}}`, "demo")
	if err != nil {
		t.Fatalf("watch --trim --until: %v (out=%q)", err, out)
	}
	got := outLines(out)
	if len(got) != 2 {
		t.Fatalf("printed %d frames, want the two up to and including the match:\n%s", len(got), out)
	}
	if strings.Contains(out, "stage") {
		t.Errorf("--trim did not project the frames it printed:\n%s", out)
	}
}
