package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/scott-cotton/cli"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// query renders the predicate as tony whatever dialect it was typed in, because a GET has no
// body and so no Content-Type to declare one with. JSON is the case that matters: --ifmt json
// is what the tutorial suggests for pasting patterns, and a client that passed the text
// straight through would be asking the server to guess.
func TestWatchQueryRendersThePredicateAsTony(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)

	want := entity.Obj{"stage": "open"}.Payload()
	for _, c := range []struct{ ifmt, pattern string }{
		{"tony", "{stage: open}"},
		{"json", `{"stage": "open"}`},
		{"yaml", "stage: open"},
	} {
		t.Run(c.ifmt, func(t *testing.T) {
			g.IFmt = c.ifmt
			cfg := &watchConfig{Since: "0", If: c.pattern}
			q, err := cfg.query()
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			got, err := codec.ParseNode([]byte(q.Get("if")), codec.Tony)
			if err != nil {
				t.Fatalf("the server would not parse %q: %v", q.Get("if"), err)
			}
			// Compared as a MATCH rather than as bytes: the point is that the server asks the
			// same question, not that the rendering is character-for-character anything.
			if !(entity.Pattern{Predicate: got}).MatchEvent(entity.Event{
				Ref: entity.Ref{System: "demo", Kind: "pr", ID: "1"}, Op: entity.Set, To: want,
			}) {
				t.Errorf("--ifmt %s: %q reached the wire as %q, which does not match %v",
					c.ifmt, c.pattern, q.Get("if"), want)
			}
		})
	}
}

// A tag is the reason the canonical form is tony rather than JSON: `!glob` is not JSON, and
// an operator is most of what makes the predicate a match instead of a bag of equalities. It
// has to survive the parse-and-re-render the wire format costs.
func TestWatchQueryKeepsOperators(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: `{id: !glob "hotfix-*"}`}
	q, err := cfg.query()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.Contains(q.Get("if"), "!glob") {
		t.Fatalf("the operator did not survive: %q", q.Get("if"))
	}
}

// --if-at goes on the wire a SEGMENT AT A TIME, as `entity rm --at` sends its path: the
// address is a list there, so a field name is never re-split at the far end.
func TestWatchQuerySendsThePathBySegment(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: "true", IfAt: "labels.urgent"}
	q, err := cfg.query()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := q["if-at"]; len(got) != 2 || got[0] != "labels" || got[1] != "urgent" {
		t.Errorf("if-at = %v, want [labels urgent]", got)
	}
}

// --if-at names where a predicate is rooted, so without one it roots nothing. Sending it
// alone would be a filter that reads as narrowing and does not narrow — the store answers the
// same stream with it and without it — so it is a usage error, said before anything is sent.
func TestWatchRejectsAPathWithoutAPredicate(t *testing.T) {
	cfg := &watchConfig{Since: "0", IfAt: "labels"}
	_, err := cfg.query()
	if !errors.Is(err, cli.ErrUsage) {
		t.Fatalf("--if-at alone gave %v, want a usage error", err)
	}
}

// A predicate that does not parse is the operator's typo, and it is caught HERE rather than
// becoming a stream that silently matches nothing.
func TestWatchRejectsAMalformedPredicate(t *testing.T) {
	defer func(f string) { g.IFmt = f }(g.IFmt)
	g.IFmt = "tony"

	cfg := &watchConfig{Since: "0", If: "{stage: "}
	if _, err := cfg.query(); err == nil {
		t.Fatal("a malformed --if was accepted")
	}
}
