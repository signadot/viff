package entity

import (
	"strings"
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"go.viff.dev/viff/pkg/codec"
)

// A PRECONDITION AT AN ARRAY ELEMENT IS ABOUT THE ELEMENT. A write at `votes[1].choice` whose
// guard names no place of its own is asking about `votes[1].choice` — not about the entity
// that holds it, and not about the value some other ballot happens to carry.
func TestAGuardAtAnArrayElementAsksAboutTheElement(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref) // [{by: walter, choice: approve}, {by: dee, choice: reject}]

	won, _, err := s.Write(Write{At: ref.Into("votes[1].choice"), Value: Val("approve"),
		When: []Require{{Pattern: Val("reject")}}})
	if err != nil {
		t.Fatalf("guarded write at an element: %v", err)
	}
	if !won {
		t.Fatal("the guard did not hold, and `reject` is what stands at votes[1].choice")
	}
	if got := renderPayload(t, s, ref); strings.Contains(got, "reject") {
		t.Fatalf("the write did not land:\n%s", got)
	}

	// The same pattern at the OTHER element: `reject` was never what votes[0].choice held, and
	// the entity holding one somewhere is not an answer to the question asked.
	won, _, err = s.Write(Write{At: ref.Into("votes[0].choice"), Value: Val("reject"),
		When: []Require{{Pattern: Val("reject")}}})
	if err != nil {
		t.Fatalf("guarded write at the other element: %v", err)
	}
	if won {
		t.Fatal("a guard at votes[0] was satisfied by what votes[1] held")
	}
}

// AND IT SAYS NOTHING ABOUT THE OTHER ELEMENTS, which is the difference between `!at` and an
// array-shaped pattern: an array pattern matches positionally and by length, so a guard on one
// ballot would refuse the moment somebody cast another.
func TestAGuardAtAnElementIsUndisturbedByTheOthers(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref)
	guarded := func() bool {
		t.Helper()
		won, _, err := s.Write(Write{At: ref.Into("votes[0]"),
			Value: Obj{"choice": "approve"}.Payload(),
			When:  []Require{{Pattern: Obj{"by": "walter"}.Payload()}}})
		if err != nil {
			t.Fatalf("guarded write: %v", err)
		}
		return won
	}
	if !guarded() {
		t.Fatal("the guard did not hold on the array as seeded")
	}
	// A third ballot, and the first one is untouched.
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{votes: [{by: walter, choice: approve}, {by: dee, choice: reject}, {by: kai, choice: approve}]}`))}); err != nil {
		t.Fatalf("appending a ballot: %v", err)
	}
	if !guarded() {
		t.Fatal("a ballot cast beside votes[0] refused a guard about votes[0]")
	}
}

// A REQUIRE THAT SPELLS A PLACE OUT is about that place. The write is at an element; the guard
// names the entity, so it is asked about the entity — the two are not the same question, and
// which one was meant is what the spelling says.
func TestAGuardNamingTheEntityIsAboutTheEntity(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref)
	if _, _, err := s.Write(Write{At: ref.At(), Value: Obj{"stage": "open"}.Payload()}); err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	for _, c := range []struct {
		stage string
		want  bool
	}{{"open", true}, {"closed", false}} {
		won, _, err := s.Write(Write{At: ref.Into("votes[0].choice"), Value: Val("approve"),
			When: []Require{{At: ref.At(), Pattern: Obj{"stage": c.stage}.Payload()}}})
		if err != nil {
			t.Fatalf("guarded write (stage %s): %v", c.stage, err)
		}
		if won != c.want {
			t.Errorf("a guard for stage %q on an entity at stage open committed=%v, want %v",
				c.stage, won, c.want)
		}
	}
}

// CREATE ASKS ABOUT THE ELEMENT TOO, and an element that stands there answers it: the write is
// refused, rather than committing over the ballot already cast.
func TestACreateAtAnElementThatStandsIsRefused(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref)
	won, _, err := s.Write(Write{At: ref.Into("votes[0]"), Value: Val("clobbered"),
		When: []Require{{Absent: true}}})
	if err != nil {
		t.Fatalf("create at an element: %v", err)
	}
	if won {
		t.Fatal("a create committed over an element that was already there")
	}
	if got := renderPayload(t, s, ref); !strings.Contains(got, "by: walter") {
		t.Fatalf("a refused create damaged the ballot:\n%s", got)
	}
}

// AND WHERE NO ELEMENT STANDS the guard holds, so what answers is the SUBSTRATE — which knows
// whether an array is there and how long it is, and says so. A write cannot create an element
// in place; the guard not being the thing that refuses is what lets the reader hear why.
func TestACreateAtAnElementPastTheEndIsAnsweredByTheSubstrate(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	seedVotes(t, s, ref)
	won, _, err := s.Write(Write{At: ref.Into("votes[9]"), Value: Val("nowhere"),
		When: []Require{{Absent: true}}})
	if won {
		t.Fatal("a write past the end committed")
	}
	if err == nil {
		t.Fatal("a create past the end was refused as a condition; the array's length is what " +
			"refused it and the caller cannot learn that from a silent no")
	}
	if !strings.Contains(err.Error(), "element") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// AN ADDRESS THAT CANNOT BE WRITTEN AS A TAG ARGUMENT is refused where it is asked for. `!at`
// carries the address in its tag and a tag argument has no quoting, so a step holding a
// top-level comma reads back as two arguments — a match that asks something else. The
// UNCONDITIONAL write at the same place is unaffected: it is a path, not a tag.
func TestAGuardAtAnElementNoTagCanCarryIsRefused(t *testing.T) {
	s := openStore(t)
	ref := RefAt("demo", "pr", "1")
	if _, _, err := s.Write(Write{At: ref.At(), Value: Raw(mustParseNode(t,
		`{"a,b": [one, two]}`))}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sub := `"a,b"[0]`
	if _, _, err := s.Write(Write{At: ref.Into(sub), Value: Val("plain")}); err != nil {
		t.Fatalf("an unconditional write at %s: %v", sub, err)
	}
	_, _, err := s.Write(Write{At: ref.Into(sub), Value: Val("guarded"),
		When: []Require{{Pattern: Val("plain")}}})
	if err == nil {
		t.Fatal("a guard at an address no tag can carry was accepted")
	}
	if !strings.Contains(err.Error(), "tag argument") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if got := renderPayload(t, s, ref); !strings.Contains(got, "plain") {
		t.Fatalf("the refused guard changed something:\n%s", got)
	}
}

// THE PATTERN IS ASKED IN PROCESS TOO, and there a COMMENT WRAPPER is where the `!at` would go
// missing: a head comment wraps the value it precedes, and a match reads under a wrapper before
// it reads a tag — so a tag composed onto the wrapper is dropped, and what is left is the bare
// pattern asked about the ENTITY. That is the guard checking the wrong place, silently.
//
// It is asked of tony directly rather than through a write, because a write does not reach it:
// the encoder writes the tag onto the value and the comment nowhere, so the wire rescues the
// pattern the constructor got wrong. The constructor is what has to be right.
func TestTheElementPatternIsAboutTheElementUnderAComment(t *testing.T) {
	doc := mustParseComment(t, `{demo: {pr: {"1": {votes: [{by: walter}, {by: dee}]}}}}`)
	for _, tc := range []struct {
		name    string
		pattern Payload
		want    bool
	}{
		{"plain", Obj{"by": "walter"}.Payload(), true},
		{"under a head comment", mustParseComment(t, "# who cast it\n{by: walter}"), true},
		{"the ballot beside it", mustParseComment(t, "# who cast it\n{by: dee}"), false},
	} {
		p := pathPattern{at: RefAt("demo", "pr", "1").At(), sub: "votes[0]", pattern: tc.pattern}
		got, err := tony.Match(doc, elemPattern(p, p.at.segments()))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: the pattern matched=%v, want %v — it is asked about votes[0], "+
				"where walter's ballot stands", tc.name, got, tc.want)
		}
	}
}

// mustParseComment parses KEEPING the comments, which parse.Parse does not: a wrapper node is
// the whole point of the test above.
func mustParseComment(t *testing.T, src string) Payload {
	t.Helper()
	n, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}
