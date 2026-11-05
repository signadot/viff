package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
)

// A FRAME, as the server sends one. --until is matched against this and not against the
// payload, so every case below is written in the frame's coordinates.
const frame = `{at: "demo.pr.1", op: set, to: {stage: merged, who: ana}, rev: 42}`

// --until ends the stream on the first frame that matches, and matches nothing else.
func TestUntilMatchesTheFrame(t *testing.T) {
	for _, c := range []struct {
		pat  string
		want bool
	}{
		{`{to: {stage: merged}}`, true},
		{`{to: {stage: open}}`, false},
		// A frame's own fields are askable, which is the point of matching the frame: `op` is
		// not in any payload, so a delete is something --until can wait for and --if cannot.
		{`{op: set}`, true},
		{`{op: delete}`, false},
		{`{at: "demo.pr.1"}`, true},
		// Presence, the same spelling it has in --if: a null matches whatever the key holds.
		{`{rev}`, true},
		{`{gap}`, false},
	} {
		u, err := readUntil(c.pat)
		if err != nil {
			t.Errorf("--until %s: %v", c.pat, err)
			continue
		}
		got, err := u.reached(frame, codec.Tony)
		if err != nil {
			t.Errorf("--until %s: %v", c.pat, err)
			continue
		}
		if got != c.want {
			t.Errorf("--until %s reached=%v, want %v", c.pat, got, c.want)
		}
	}
}

// An absent --until never ends the stream, which is what watch has always done.
func TestNoUntilNeverStops(t *testing.T) {
	u, err := readUntil("")
	if err != nil {
		t.Fatal(err)
	}
	got, err := u.reached(frame, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("a watch with no --until stopped")
	}
}

// A line that does not parse cannot answer a question asked of a document. It is still printed
// — that is streamChanges' rule for a surprising line — but it does not end the stream, which
// would otherwise make a garbled frame indistinguishable from the awaited one.
func TestUnparseableFrameDoesNotStop(t *testing.T) {
	u, err := readUntil(`{to: {stage: merged}}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := u.reached(`not a document {{{`, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an unparseable line ended the stream")
	}
}

// THE MISTAKE WORTH A REFUSAL: --until written against the payload, the way --if takes it.
// Against a frame it matches nothing, and a watch matching nothing looks exactly like a watch
// waiting — so it is refused where it is typed, naming the spelling that means it.
func TestPayloadRootedUntilIsRefused(t *testing.T) {
	_, err := readUntil(`{stage: merged}`)
	if err == nil {
		t.Fatal("a payload-rooted --until was accepted")
	}
	if !errors.Is(err, cli.ErrUsage) {
		t.Errorf("not a usage error: %v", err)
	}
	if !strings.Contains(err.Error(), "{to: {stage: …}}") {
		t.Errorf("the refusal does not spell the frame-rooted form: %v", err)
	}
}

// The refusal is for the pattern that can only be a mistake. A pattern naming even one of a
// frame's fields is written in the frame's coordinates and is the operator's business, and an
// operator at the root is not a set of field names at all.
func TestFrameRootedUntilIsAccepted(t *testing.T) {
	for _, pat := range []string{
		`{to: {stage: merged}}`,
		`{op: set}`,
		`{at: "demo.pr.1", stage: merged}`, // one frame field: taken at its word
		`!or [{op: set}, {op: delete}]`,    // an operator, whose keys are not fields
		`{}`,
	} {
		if _, err := readUntil(pat); err != nil {
			t.Errorf("--until %s was refused: %v", pat, err)
		}
	}
}

// The refusal describes the frame verse actually sends. Reading the shape off entity.Event is
// what keeps it that way, so this checks the two have not come apart.
func TestFrameShapeIsTheEventsFields(t *testing.T) {
	for _, f := range []string{"at", "op", "to", "rev", "gap"} {
		if !frameFields[f] {
			t.Errorf("entity.Event no longer has a %q field, or the shape stopped reading it", f)
		}
	}
	if got := frameShape(); got != "{at gap op rev to}" {
		t.Errorf("frame shape is %q", got)
	}
	// The set is exactly the event's, not a superset somebody added by hand.
	if n := len(frameFields); n != 5 {
		t.Errorf("frame has %d fields, want the 5 on entity.Event (%+v)", n, entity.Event{})
	}
}
