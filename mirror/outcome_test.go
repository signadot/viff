package mirror

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// parseResult accepts a parse ONLY for a composite. The parser is strict enough that English
// fails it outright, so this gate is not about prose accidentally becoming a record — it is
// that a bare word parses fine as a tony string, and `data` present for every one-word answer
// would signal nothing at all.
func TestParseResultOnlyStructure(t *testing.T) {
	// answers that are NOT structure: no data field at all
	for _, s := range []string{
		"yes", "high", "SHIPIT", "LGTM!", "3",
		"Verdict: ship it",
		"looks good to me",
		"Error could not open file",
		"No blocking issues found. Approving.",
		"severity: high\nreason: null deref",
		"",
	} {
		if got := ParseResult(s); got != nil {
			t.Errorf("%q -> data %v, want none", s, got.Type)
		}
	}

	// answers that ARE structure
	for _, s := range []string{
		`{"severity": "high", "findings": []}`,
		`{severity: high}`,
		`["a", "b"]`,
	} {
		got := ParseResult(s)
		if got == nil {
			t.Errorf("%q -> no data, want a composite", s)
			continue
		}
		if got.Type != ir.ObjectType && got.Type != ir.ArrayType {
			t.Errorf("%q -> %v, want an object or a list", s, got.Type)
		}
	}
}

// The two faces are not alternatives: a structured answer keeps its text as well, so a
// charter that wants the whole thing verbatim is never forced through the parse.
func TestParseResultKeepsBothFaces(t *testing.T) {
	const answer = `{severity: high}`
	data := ParseResult(answer)
	if data == nil {
		t.Fatal("expected data")
	}
	sev := ir.Get(data, "severity")
	if sev == nil || sev.String != "high" {
		t.Fatalf("data.severity = %v, want high", sev)
	}
}
