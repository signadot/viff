package main

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

// --nowire PRINTS THE BLOCK FORM, which is the whole ask: a frame wider than the terminal
// becomes a frame several lines tall.
func TestNoWirePrintsBlocks(t *testing.T) {
	ts := watchServer(t, false)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--nowire", "demo")
	if err != nil {
		t.Fatalf("watch --nowire: %v (out=%q)", err, out)
	}
	got := outLines(out)
	if len(got) <= len(frames) {
		t.Fatalf("--nowire printed %d lines for %d frames, so it is still on one line each:\n%s",
			len(got), len(frames), out)
	}
	// The fields are on their own lines, which is what "not wire" means here.
	for _, line := range got {
		if strings.Contains(line, "at:") && strings.Contains(line, "rev:") {
			t.Errorf("a frame is still on one line: %q", line)
		}
	}
	// THE BRACKETS GO WITH THE WIRE. Replayed over six lines they are the wire's punctuation
	// without its compactness, which is the one shape nobody asked for.
	if strings.Contains(out, "{") || strings.Contains(out, "}") {
		t.Errorf("--nowire kept the wire's brackets:\n%s", out)
	}
	// Nothing is lost in the reshaping: it is the same document, differently laid out.
	for _, want := range []string{"demo.pr.1", "demo.pr.2", "merged", "rev:"} {
		if !strings.Contains(out, want) {
			t.Errorf("--nowire dropped %q:\n%s", want, out)
		}
	}
}

// IT IS A DOCUMENT STREAM, not a picture of one. Without the brackets nothing would separate
// one frame from the next — `rev: 1` above `at: demo.pr.2` is a single document with a repeated
// key — so each frame opens with `---`, and what comes out parses back as the frames that went
// in. This is the assertion that the output is still data.
func TestNoWireRoundTripsAsDocuments(t *testing.T) {
	ts := watchServer(t, false)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--nowire", "demo")
	if err != nil {
		t.Fatalf("watch --nowire: %v (out=%q)", err, out)
	}
	docs, err := codec.ParseNodes([]byte(out), codec.Tony)
	if err != nil {
		t.Fatalf("--nowire output does not parse as a tony stream: %v\n%s", err, out)
	}
	if len(docs) != len(frames) {
		t.Fatalf("parsed %d documents from %d frames:\n%s", len(docs), len(frames), out)
	}
	// The same documents, not merely the same number of them. Compared in the wire form, which
	// is the canonical one-line spelling of a document and so ignores the layout this test has
	// just spent its time changing.
	for i, d := range docs {
		want, err := codec.ParseNode([]byte(frames[i]), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		got := wireOf(t, d)
		if w := wireOf(t, want); got != w {
			t.Errorf("frame %d came back as %s, want %s", i, got, w)
		}
	}
}

func wireOf(t *testing.T, n *ir.Node) string {
	t.Helper()
	b, err := codec.NodeBytesWire(codec.StripLayout(n), codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// THE DEFAULT IS UNMOVED. One frame per line is what makes a stream greppable, so --nowire has
// to be the thing you ask for rather than the thing you get.
func TestWireIsStillTheDefault(t *testing.T) {
	ts := watchServer(t, false)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "demo")
	if err != nil {
		t.Fatalf("watch: %v (out=%q)", err, out)
	}
	for _, line := range outLines(out) {
		if !strings.Contains(line, "at:") || !strings.Contains(line, "rev:") {
			t.Errorf("a frame is not on one line: %q", line)
		}
	}
}

// The two flags are about different things — where the stream ends, and what shape it is
// printed in — so they compose.
func TestNoWireComposesWithUntil(t *testing.T) {
	ts := watchServer(t, true)
	defer ts.Close()
	testAddr = strings.TrimPrefix(ts.URL, "http://")

	out, err := runCLI(t, "watch", "--nowire", "--until", `{to: {stage: merged}}`, "demo")
	if err != nil {
		t.Fatalf("watch --nowire --until: %v (out=%q)", err, out)
	}
	if strings.Contains(out, "demo.pr.2") {
		t.Errorf("the stream ran past the match:\n%s", out)
	}
	if !strings.Contains(out, "merged") {
		t.Errorf("the matching frame was not printed:\n%s", out)
	}
	if len(outLines(out)) <= 2 {
		t.Errorf("--nowire did not reach the printing:\n%s", out)
	}
}
