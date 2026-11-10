package codec

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// A frame is read as it was written, however many lines it took: a document with comments is
// several `data:` lines on the wire and one frame to the reader, which parses to the document.
func TestAFrameOfSeveralLinesIsOneFrame(t *testing.T) {
	commented, err := ParseNode([]byte("at: x\nto:\n  # why the rule is here\n  # and a second line\n  stage: seen\n"), Tony)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ParseNode([]byte("{at: y, to: {stage: open}}"), Tony)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	sse, err := NewSSE(rec, httptest.NewRequest("GET", "/watch", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := sse.SendNode(commented); err != nil {
		t.Fatal(err)
	}
	if err := sse.SendNode(plain); err != nil {
		t.Fatal(err)
	}
	wire := rec.Body.String()
	if strings.Count(wire, "data: ") < 3 {
		t.Fatalf("the commented frame went out on one line, so this tests nothing: %q", wire)
	}

	frames := NewFrames(strings.NewReader(wire), 1<<20)
	var got []string
	for frames.Next() {
		n, err := ParseNode([]byte(frames.Text()), sse.Format())
		if err != nil {
			t.Fatalf("frame %d does not parse: %v\n%s", len(got), err, frames.Text())
		}
		at, _ := NodeBytesWire(n.Values[0], Tony)
		got = append(got, strings.TrimSpace(string(at)))
	}
	if frames.Err() != nil {
		t.Fatal(frames.Err())
	}
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("read frames at %q; want x then y", got)
	}
	if !strings.Contains(NewFramesText(t, wire), "why the rule is here") {
		t.Fatal("the comment did not reach the reader")
	}
}

// NewFramesText is the first frame of wire.
func NewFramesText(t *testing.T, wire string) string {
	t.Helper()
	frames := NewFrames(strings.NewReader(wire), 1<<20)
	if !frames.Next() {
		t.Fatalf("no frame: %v", frames.Err())
	}
	return frames.Text()
}

// A frame is bounded as a whole, not a line at a time, and a stream that stops inside a frame
// did not send it.
func TestAFrameIsBoundedAndWhole(t *testing.T) {
	long := strings.Repeat("data: "+strings.Repeat("x", 40<<10)+"\n", 4) + "\n"
	frames := NewFrames(strings.NewReader(long), 100<<10)
	if frames.Next() || !errors.Is(frames.Err(), ErrFrameTooLong) {
		t.Fatalf("a frame over the limit was read: err=%v", frames.Err())
	}
	frames = NewFrames(strings.NewReader("data: whole\n\ndata: cut\ndata: sho"), 1<<20)
	var got []string
	for frames.Next() {
		got = append(got, frames.Text())
	}
	if len(got) != 1 || got[0] != "whole" || frames.Err() != nil {
		t.Fatalf("read %q, err=%v; want only the frame that ended", got, frames.Err())
	}
}
