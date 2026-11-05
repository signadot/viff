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

// An agent that has just read nine files narrates and fences before it answers, and that
// was the observed shape of the failure: `data` absent, and a hook reading `.[data.class]`
// failing at patch time. A fenced answer is a formatting habit, not a refusal to answer, so
// the document inside the fence is the answer — and it is the SAME document the spawn's
// answer contract validated, because both go through answer.Extract.
func TestParseResultReadsAFencedAnswer(t *testing.T) {
	const final = "I read the handler and both callers.\n\n" +
		"```tony\n{class: bug, severity: high}\n```\n\n" +
		"Happy to write the patch if you want it."
	got := ParseResult(final)
	if got == nil {
		t.Fatal("no data from a fenced answer")
	}
	if c := ir.Get(got, "class"); c == nil || c.String != "bug" {
		t.Fatalf("data.class = %v, want bug", c)
	}
}

// Tolerance stops at the fence. Mining a trailing {…} out of a paragraph would turn a
// sentence that happens to contain braces into a record, and a wrong document is worse than
// no document — the charter derefing it cannot tell.
func TestParseResultDoesNotMineProse(t *testing.T) {
	for _, s := range []string{
		"I'd set {class: bug} but I'm not certain, and severity is unclear.",
		"The config file has a `{class: bug}` entry, which is unrelated.",
	} {
		if got := ParseResult(s); got != nil {
			t.Errorf("%q -> data %v, want none", s, got.Type)
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
