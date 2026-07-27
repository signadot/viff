package answer

import (
	"strings"
	"testing"
)

const contractSrc = `class: !or [bug, nit, risk]
severity: !or [low, high]
why: !irtype string`

func mustParse(t *testing.T, src string) *Contract {
	t.Helper()
	c, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return c
}

// The contract is satisfied by the document and says so with the document, which is what a
// charter's $data derefs.
func TestCheckAcceptsAndReturnsTheDocument(t *testing.T) {
	c := mustParse(t, contractSrc)
	doc, err := c.Check(`{class: bug, severity: high, why: "nil deref in handler"}`)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if doc == nil {
		t.Fatal("no document returned")
	}
	if got := doc.Fields[0].String; got != "class" {
		t.Fatalf("first field %q, want class", got)
	}
}

// Matching is open-world: an answer carrying more than it was asked for is still an answer.
func TestExtraFieldsAreFine(t *testing.T) {
	c := mustParse(t, contractSrc)
	if _, err := c.Check(`{class: nit, severity: low, why: "style", note: "also fixed"}`); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// The schema form and the bare-pattern form are the same contract.
func TestSchemaFormAndBarePatternAgree(t *testing.T) {
	full := mustParse(t, `signature: {name: verdict}
accept:
  class: !or [bug, nit]
`)
	bare := mustParse(t, `class: !or [bug, nit]`)
	for _, c := range []*Contract{full, bare} {
		if _, err := c.Check(`{class: bug}`); err != nil {
			t.Fatalf("good answer rejected: %v", err)
		}
		if _, err := c.Check(`{class: wat}`); err == nil {
			t.Fatal("bad answer accepted")
		}
	}
}

// The point of the whole exercise: every failure names its path, what was expected there,
// and what was found — and reports ALL of them, so one repair round can fix everything
// that is wrong rather than everything minus one.
func TestFailureNamesEveryPath(t *testing.T) {
	c := mustParse(t, contractSrc)
	_, err := c.Check(`{class: critical, why: 3}`)
	if err == nil {
		t.Fatal("bad answer accepted")
	}
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T, want *Failure", err)
	}
	if f.Kind != Mismatched {
		t.Fatalf("kind %v, want Mismatched", f.Kind)
	}
	if len(f.Mismatches) != 3 {
		t.Fatalf("%d mismatches, want 3: %v", len(f.Mismatches), f.Mismatches)
	}
	by := map[string]Mismatch{}
	for _, m := range f.Mismatches {
		by[m.Path] = m
	}
	if m := by[".class"]; m.Reason != Wrong || !strings.Contains(m.Expected, "bug") || !strings.Contains(m.Found, "critical") {
		t.Errorf(".class -> %+v", m)
	}
	if m := by[".severity"]; m.Reason != Absent {
		t.Errorf(".severity -> %+v, want absent", m)
	}
	if m := by[".why"]; m.Reason != Wrong || m.Found != "3" {
		t.Errorf(".why -> %+v", m)
	}
}

// Nesting: the path has to reach the field that is actually wrong, not stop at the object
// containing it — the difference between a usable diagnosis and "something in here".
func TestNestedPaths(t *testing.T) {
	c := mustParse(t, `review: {verdict: !or [ship, hold], of: {pr: !irtype string}}`)
	_, err := c.Check(`{review: {verdict: maybe, of: {pr: 12}}}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	want := map[string]bool{".review.verdict": true, ".review.of.pr": true}
	for _, m := range f.Mismatches {
		if !want[m.Path] {
			t.Errorf("unexpected path %q", m.Path)
		}
		delete(want, m.Path)
	}
	for p := range want {
		t.Errorf("no mismatch reported at %s", p)
	}
}

// A list pattern matches length-exactly, so the length IS the diagnosis; reporting each
// index on top of it would bury it.
func TestListLength(t *testing.T) {
	c := mustParse(t, `findings: [{severity: !or [low, high]}]`)
	_, err := c.Check(`{findings: [{severity: low}, {severity: high}]}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if len(f.Mismatches) != 1 || f.Mismatches[0].Reason != WrongLen || f.Mismatches[0].Path != ".findings" {
		t.Fatalf("mismatches = %v", f.Mismatches)
	}
}

// An element inside `!all` is exactly what the walk cannot see into — the operator holds
// that knowledge. It must still say something true and located, rather than nothing.
func TestAllOperatorReportsThePathItCanSee(t *testing.T) {
	c := mustParse(t, `findings: !all {severity: !or [low, high]}`)
	if _, err := c.Check(`{findings: [{severity: low}, {severity: high}]}`); err != nil {
		t.Fatalf("good answer rejected: %v", err)
	}
	_, err := c.Check(`{findings: [{severity: low}, {severity: catastrophic}]}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if len(f.Mismatches) != 1 || f.Mismatches[0].Path != ".findings" {
		t.Fatalf("mismatches = %v", f.Mismatches)
	}
	if !strings.Contains(f.Mismatches[0].Expected, "all") {
		t.Errorf("expected does not show the operator: %q", f.Mismatches[0].Expected)
	}
}

// Narration then a fenced answer is the observed failure this whole thing exists for: the
// contract judges the answer, not the layout.
func TestExtractTakesTheAnswerOutOfNarration(t *testing.T) {
	c := mustParse(t, contractSrc)
	final := "I read the handler and the two callers.\n\n" +
		"```tony\n{class: bug, severity: high, why: \"nil deref\"}\n```\n\n" +
		"Let me know if you want the patch."
	doc, err := c.Check(final)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if doc == nil {
		t.Fatal("no document")
	}
}

// Prose is not an answer, and that is a different failure from a wrong answer: one asks the
// model to answer at all, the other to fix a field.
func TestProseIsNoDocument(t *testing.T) {
	c := mustParse(t, contractSrc)
	for _, s := range []string{
		"I reviewed the file and found one bug: a nil deref. Severity high.",
		"LGTM!",
		"high",
		"",
	} {
		_, err := c.Check(s)
		f, ok := err.(*Failure)
		if !ok {
			t.Fatalf("%q -> %T (%v), want *Failure", s, err, err)
		}
		if f.Kind != NoDocument {
			t.Errorf("%q -> kind %v, want NoDocument", s, f.Kind)
		}
	}
}

// A contract that cannot be parsed fails where it is written, not after a model call.
func TestBadContractFailsAtParse(t *testing.T) {
	for _, src := range []string{"", "   ", "{class: !or [bug"} {
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q) accepted a contract it cannot mean", src)
		}
	}
}

// What goes back to the model has to carry the diagnosis, the contract, and the honest exit
// — a repair loop that only says "wrong, try again" is the thing this design rejects.
func TestRepairMessageCarriesTheDiagnosis(t *testing.T) {
	c := mustParse(t, contractSrc)
	_, err := c.Check(`{class: critical, why: 3}`)
	msg := err.(*Failure).Repair(c, "fail")
	for _, want := range []string{".class", "critical", ".severity", "absent", ".why", "!or", "fail"} {
		if !strings.Contains(msg, want) {
			t.Errorf("repair message missing %q:\n%s", want, msg)
		}
	}
}

// A schema with a real defect must fail as a schema. The reading "not a schema, so the whole
// document is the pattern" would take an author's broken `accept:` clause and silently
// reinterpret it as a demand that the ANSWER contain fields called `signature` and `define`
// — turning a diagnosed error into a mismatch about the contract's own keywords.
func TestBrokenSchemaDoesNotDegradeIntoAPattern(t *testing.T) {
	_, err := Parse(`signature: {name: c}
define:
  node: {next: .[node]}
accept: .[node]
`)
	if err == nil {
		t.Fatal("a schema with an impossible definition cycle was accepted")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error lost the diagnosis: %v", err)
	}

}

// `accept:` with nothing under it is tony's own "present, whatever the value", and it is
// kept meaning that rather than being made an error: it is the contract that says "answer
// with a document, any document", which is a real thing to want and still rules out the
// failure the contract exists for — an agent that stops on prose.
func TestEmptyAcceptStillRequiresADocument(t *testing.T) {
	c := mustParse(t, "signature: {name: any}\naccept:\n")
	if _, err := c.Check(`{whatever: [1, 2]}`); err != nil {
		t.Fatalf("a document was rejected: %v", err)
	}
	_, err := c.Check("I had a look and it seems fine.")
	f, ok := err.(*Failure)
	if !ok || f.Kind != NoDocument {
		t.Fatalf("prose -> %v, want a NoDocument failure", err)
	}
}
