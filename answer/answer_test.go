package answer

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

const contractSrc = `class: !or [bug, nit, risk]
severity: !or [low, high]
why: !irtype string`

func mustParse(t *testing.T, src string) *Contract {
	t.Helper()
	c, err := ParseSource(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return c
}

// The contract is satisfied by the document and says so with the document, which is what a
// charter's `data` reads.
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

// A definition reference is refused where it is written, in either form. Nothing resolves one
// here, and a match compares `.[string]` as the nine characters it is spelled with — so a clause
// that says "a string" accepts a number, accepts a list, accepts anything, and no error anywhere
// says so (avsp4c4jh12krzqzmdn0). The refusal names the reference, since the author's next move
// is to write the operator that means it.
func TestADefinitionReferenceIsRefused(t *testing.T) {
	for _, pattern := range []string{
		`{why: .[string]}`,
		`{ids: .[array(string)]}`,
		`{review: {of: {pr: .[int]}}}`,
		`{class: !or [bug, .[string]]}`,
	} {
		for _, src := range []string{pattern, "accept: " + pattern} {
			c, err := ParseSource(src)
			if err == nil {
				t.Errorf("%s: parsed, and accepts %s", src, c.Example())
				continue
			}
			if !strings.Contains(err.Error(), ".[") {
				t.Errorf("%s: the error does not name the reference: %v", src, err)
			}
		}
	}
}

// And a `define:` clause, for the same reason: a contract that carries definitions is a contract
// whose author expects them to be resolved, and nothing here resolves them. Refused whole rather
// than read halfway — the schema form is a spelling of a pattern, not a schema verse evaluates.
func TestADefineClauseIsRefused(t *testing.T) {
	_, err := ParseSource("define:\n  sev: !or [low, high]\naccept:\n  s: !irtype string\n")
	if err == nil {
		t.Fatal("a contract carrying definitions was accepted")
	}
	if !strings.Contains(err.Error(), "define") {
		t.Errorf("the error does not name the clause: %v", err)
	}
}

// A contract may use every operator the match vocabulary has, in both forms, because it is
// matched by the same tony.Match as a `when`. Nothing narrows it to a subset some other reader
// of the pattern — a schema's satisfiability check, say — happens to know.
func TestAContractKeepsEveryOperator(t *testing.T) {
	for _, src := range []string{`{name: !glob "head*"}`, `accept: {name: !glob "head*"}`} {
		c, err := ParseSource(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if _, err := c.Check(`{name: header}`); err != nil {
			t.Errorf("%s: a good answer was rejected: %v", src, err)
		}
		if _, err := c.Check(`{name: footer}`); err == nil {
			t.Errorf("%s: bad answer accepted", src)
		}
	}
}

// Parse leaves the document it is given as it found it, and shows the agent the contract its
// author wrote. The document is the charter's own — versed holds it and `verse data` prints it —
// and a contract that resolved anything would be building its pattern out of those nodes: the
// expansion that once stood here took the clause's leaves into itself and rewrote a string
// holding $[...] with its value, so the document came back changed and the agent was shown text
// its author never wrote (kyt2we4jh12krzqzmdn0).
func TestParseLeavesTheContractAsWritten(t *testing.T) {
	for _, src := range []string{
		`accept: {why: !irtype string, note: "x $[1+1]"}`,
		`{why: !irtype string, note: "x $[1+1]"}`,
	} {
		doc, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		was := doc.Clone()
		c, err := Parse(doc)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if !doc.DeepEqual(was) {
			t.Errorf("%s: Parse changed the document it was given", src)
		}
		for _, at := range unlinked(doc, "") {
			t.Errorf("%s: after Parse, %s names a parent that does not hold it", src, at)
		}
		if !strings.Contains(c.Source(), "$[1+1]") {
			t.Errorf("%s: the source shown is not what the author wrote: %s", src, c.Source())
		}
	}
}

// unlinked lists the nodes under n whose parent link does not lead back to where they are held.
func unlinked(n *ir.Node, at string) []string {
	var out []string
	for i, v := range n.Values {
		if v.Parent != n || v.ParentIndex != i {
			out = append(out, fmt.Sprintf("value %d of %q", i, at))
		}
		out = append(out, unlinked(v, fmt.Sprintf("%s[%d]", at, i))...)
	}
	for i, f := range n.Fields {
		if f.Parent != n || f.ParentIndex != i {
			out = append(out, fmt.Sprintf("field %q of %q", f.String, at))
		}
	}
	return out
}

// The point of the whole exercise: every failure names its path, what was expected there,
// and what was found — and reports ALL of them, so one repair round can fix everything that
// is wrong rather than everything minus one. None of this is verse's to derive: it is the
// match's own account of how it walked (tony.Explaining, v0.0.102).
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
	by := map[string]*tony.Frame{}
	for _, fr := range f.Why.Failures {
		by[fr.Path] = fr
	}
	if len(by) != 3 {
		t.Fatalf("%d failures, want 3: %s", len(by), f.Why)
	}
	// present-but-wrong, and the operator that judged it
	if fr := by["class"]; fr == nil || fr.Op != "or" || fr.Found == nil || fr.Found.String != "critical" {
		t.Errorf("class -> %+v", fr)
	}
	// absent, which is the distinction a caller can most often repair by itself
	if fr := by["severity"]; fr == nil || fr.Reason != tony.ReasonAbsent {
		t.Errorf("severity -> %+v, want absent", fr)
	}
	if fr := by["why"]; fr == nil || fr.Op != "irtype" {
		t.Errorf("why -> %+v", fr)
	}
}

// Nesting: the path reaches the field that is actually wrong, not the object containing it —
// the difference between a usable diagnosis and "something in here".
func TestNestedPaths(t *testing.T) {
	c := mustParse(t, `review: {verdict: !or [ship, hold], of: {pr: !irtype string}}`)
	_, err := c.Check(`{review: {verdict: maybe, of: {pr: 12}}}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	want := map[string]bool{"review.verdict": true, "review.of.pr": true}
	for _, fr := range f.Why.Failures {
		if !want[fr.Path] {
			t.Errorf("unexpected path %q", fr.Path)
		}
		delete(want, fr.Path)
	}
	for p := range want {
		t.Errorf("no failure reported at %s", p)
	}
}

// A list pattern matches element by element and so length-exactly, and the length is decided
// before any element is: the diagnosis is the length, not five confusing element errors.
func TestListLength(t *testing.T) {
	c := mustParse(t, `findings: [{severity: !or [low, high]}]`)
	_, err := c.Check(`{findings: [{severity: low}, {severity: high}]}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if len(f.Why.Failures) != 1 {
		t.Fatalf("failures = %s", f.Why)
	}
	if fr := f.Why.Failures[0]; fr.Path != "findings" || fr.Reason != tony.ReasonLength {
		t.Fatalf("failure = %+v, want a length failure at findings", fr)
	}
}

// The element inside an `!all` is what no caller outside the match could name — only the
// operator knows which element it was walking. This is the case the upstream request was
// filed for, and it lands: `findings[1].severity`, not "findings".
func TestAllOperatorNamesTheElement(t *testing.T) {
	c := mustParse(t, `findings: !all {severity: !or [low, high]}`)
	if _, err := c.Check(`{findings: [{severity: low}, {severity: high}]}`); err != nil {
		t.Fatalf("good answer rejected: %v", err)
	}
	_, err := c.Check(`{findings: [{severity: low}, {severity: catastrophic}]}`)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if len(f.Why.Failures) != 1 || f.Why.Failures[0].Path != "findings[1].severity" {
		t.Fatalf("failures = %s", f.Why)
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

// A document that was written and is broken must come back with the parser's complaint. This
// is the observed failure (friction.entry.f024): a classifier answered `class: design drift`
// — two bare words, which makes the whole document unparseable — and was told only that its
// message "carries no answer document". Both repair attempts went on the container while the
// line that actually broke went unmentioned, which is the budget misspend the design set out
// to delete, reached from the other side.
func TestBrokenDocumentReportsTheParseError(t *testing.T) {
	c := mustParse(t, contractSrc)
	_, err := c.Check("class: design drift\nseverity: low\nwhy: \"it drifted\"\n")
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if f.Kind != NoDocument {
		t.Fatalf("kind %v, want NoDocument", f.Kind)
	}
	if f.ParseErr == nil {
		t.Fatal("the parse error was discarded")
	}
	if !strings.Contains(f.ParseErr.Error(), "line") {
		t.Errorf("the parse error names no line: %v", f.ParseErr)
	}
	if !strings.Contains(f.Error(), f.ParseErr.Error()) {
		t.Errorf("Error() drops the parse error: %v", f)
	}
	if msg := f.Repair(c, ""); !strings.Contains(msg, f.ParseErr.Error()) {
		t.Errorf("the repair message does not carry the parse error:\n%s", msg)
	}
}

// The error kept is the fenced block's, not the whole message's. Narration then a fence fails
// the whole-message parse on the prose at line 0 every time, so reporting that one would hand
// back an error about a sentence the agent never meant as a document — the same wrong
// diagnosis, differently sourced.
func TestParseErrorComesFromTheFencedBlock(t *testing.T) {
	c := mustParse(t, contractSrc)
	final := "I read the handler and the two callers.\n\n" +
		"```tony\nclass: design drift\nseverity: low\n```\n"
	_, err := c.Check(final)
	f, ok := err.(*Failure)
	if !ok {
		t.Fatalf("err is %T (%v), want *Failure", err, err)
	}
	if f.ParseErr == nil {
		t.Fatal("the parse error was discarded")
	}
	// The premise: parsing the whole message DOES fail, and that error is the one worth not
	// reporting. If this ever stops failing the test below proves nothing.
	if _, whole := parseComposite(final); whole == nil {
		t.Fatal("the whole message now parses; this test no longer tests anything")
	} else if f.ParseErr.Error() == whole.Error() {
		t.Errorf("the whole-message error won over the block's: %v", f.ParseErr)
	}
}

// Prose carries no parse error worth reporting only when it parses as something — a bare word
// is a String, and nothing about it is broken. The distinction keeps a repair from citing a
// line for text that was never a document.
func TestBareWordHasNoParseError(t *testing.T) {
	c := mustParse(t, contractSrc)
	for _, s := range []string{"LGTM!", "high", ""} {
		_, err := c.Check(s)
		f, ok := err.(*Failure)
		if !ok {
			t.Fatalf("%q -> %T (%v), want *Failure", s, err, err)
		}
		if f.ParseErr != nil {
			t.Errorf("%q -> ParseErr %v, want none", s, f.ParseErr)
		}
	}
}

// A contract that cannot be parsed fails where it is written, not after a model call.
func TestBadContractFailsAtParse(t *testing.T) {
	for _, src := range []string{"", "   ", "{class: !or [bug"} {
		if _, err := ParseSource(src); err == nil {
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
	for _, want := range []string{"class", "critical", "severity", "absent", "why", "!or", "fail"} {
		if !strings.Contains(msg, want) {
			t.Errorf("repair message missing %q:\n%s", want, msg)
		}
	}
}

// A schema-shaped contract fails as what it is. The reading "not a pattern I can use, so the
// whole document is the pattern" would take an author's schema and silently reinterpret it as a
// demand that the ANSWER contain fields called `signature` and `define` — a mismatch about the
// contract's own keywords, reported to a model that cannot act on it, instead of an error at the
// charter that wrote it.
func TestBrokenSchemaDoesNotDegradeIntoAPattern(t *testing.T) {
	_, err := ParseSource(`signature: {name: c}
define:
  node: {next: .[node]}
accept: .[node]
`)
	if err == nil {
		t.Fatal("a schema carrying definitions was accepted")
	}
	if !strings.Contains(err.Error(), "define") {
		t.Errorf("error lost the diagnosis: %v", err)
	}
}

// `accept: null` is tony's own "present, whatever the value", and it is kept meaning that
// rather than being made an error: it is the contract that says "answer with a document, any
// document", which is a real thing to want and still rules out the failure the contract exists
// for — an agent that stops on prose.
//
// SPELLED WITH THE `null`. A bare `accept:` meant the same thing until go-tony v0.0.194, which
// refuses a colon with no value after it and says which two spellings to reach for instead.
func TestEmptyAcceptStillRequiresADocument(t *testing.T) {
	c := mustParse(t, "signature: {name: any}\naccept: null\n")
	if _, err := c.Check(`{whatever: [1, 2]}`); err != nil {
		t.Fatalf("a document was rejected: %v", err)
	}
	_, err := c.Check("I had a look and it seems fine.")
	f, ok := err.(*Failure)
	if !ok || f.Kind != NoDocument {
		t.Fatalf("prose -> %v, want a NoDocument failure", err)
	}
}

// A repair for a message that did not PARSE says the one mechanical thing the contract cannot.
//
// The contract is a match pattern — `report: !irtype "text"` — which specifies the shape and
// says nothing about how to write one. A model that has just answered in prose reads that as a
// description rather than a form to fill in, and its second attempt was a document whose
// `report:` held unquoted prose: read as more keys, refused at the first word that was not one,
// two repair attempts spent, task failed. Both attempts were reasonable readings of what it was
// told.
func TestRepairForAnUnparseableAnswerSaysToQuote(t *testing.T) {
	c, err := ParseSource(`{files: !irtype 0, report: !irtype "text"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, checkErr := c.Check("No Go files were touched by this commit, so there is nothing to do.")
	if checkErr == nil {
		t.Fatal("prose satisfied an answer contract")
	}
	var f *Failure
	if !errors.As(checkErr, &f) {
		t.Fatalf("check failed with %T, want a *Failure", checkErr)
	}
	repair := f.Repair(c, "fail")
	if !strings.Contains(repair, "report") {
		t.Errorf("the repair does not carry the contract:\n%s", repair)
	}
	// The instance, not only the specification: three runs of one charter failed writing a
	// document from a pattern, each in a different way, and every attempt was a reasonable
	// reading of a description by something that needed an example.
	if !strings.Contains(repair, `{"files":0,"report":"text"}`) {
		t.Errorf("the repair carries no example of the shape:\n%s", repair)
	}
	if !strings.Contains(repair, "json") {
		t.Errorf("the repair does not say json is accepted, which is the escape hatch:\n%s", repair)
	}
}

// The example is derived from the pattern, so a contract and its example cannot drift.
func TestExampleIsAnInstanceOfTheContract(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`{files: !irtype 0, report: !irtype "text"}`, `{"files":0,"report":"text"}`},
		{`{class: !or [bug, nit, risk]}`, `{"class":"bug"}`},
		{`{stage: reviewed}`, `{"stage":"reviewed"}`},
		// Every conjunct of an `!and` holds of its example, rather than each getting one of
		// its own in a list. It is how base.tony writes a container of something: `array(t)`
		// is `!and [.[array], !all t]`.
		{`{ids: !and [!irtype [], !all.irtype "a"]}`, `{"ids": ["a"]}`},
		{`{m: !and [!irtype {}, !all.irtype "a"]}`, `{"m": {}}`},
		// `!ir` asks how a node is kept, which is how base.tony tells an int from a float: the
		// example is a number of that kind, not the representation's own shape.
		{`{n: !ir {int: !irtype 1}}`, `{"n":1}`},
		{`{x: !ir {float: !irtype 1}}`, `{"x":0.5}`},
	} {
		c, err := ParseSource(tc.src)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		got := c.Example()
		if got != tc.want {
			t.Errorf("example of %s\n  got  %s\n  want %s", tc.src, got, tc.want)
		}
		// And it is not merely plausible: it SATISFIES the contract it was rendered from.
		if _, err := c.Check(got); err != nil {
			t.Errorf("the example of %s does not satisfy it: %v", tc.src, err)
		}
	}
}

// `!all P` constrains every element of a list, so its example is a LIST — the operator that
// says "each of these" would otherwise exemplify as one of them, which is the shape of a member
// rather than of the field.
//
// It matters because `!all` is how a contract states an element shape at ALL, and an element
// shape is what a fan-out needs: a derive reading `.[item.says]` fails the firing when one
// element omits it, after the task has finished and its work is done. Stated in the contract,
// the same omission is caught while there is still a repair loop to fix it.
func TestExampleOfAllIsAList(t *testing.T) {
	c, err := ParseSource(`{findings: !all {symbol: !irtype "pkg.Name", status: !or [open, resolved]}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"findings": [{"status":"open","symbol":"pkg.Name"}]}`
	if got := c.Example(); got != want {
		t.Errorf("example\n  got  %s\n  want %s", got, want)
	}
	if _, err := c.Check(c.Example()); err != nil {
		t.Errorf("the example does not satisfy its own contract: %v", err)
	}
}

// An example the heuristic gets wrong is not shown. A repair presents it as an answer that
// satisfies the contract, so a wrong one teaches the model the mistake it is being asked to
// fix: `!not bug` peels to `bug`, the one value it rules out.
func TestAnExampleThatFailsIsNotShown(t *testing.T) {
	c := mustParse(t, `{class: !not bug}`)
	if ex := c.Example(); ex != "" {
		t.Errorf("an example that does not satisfy its contract was rendered: %s", ex)
	}
	_, err := c.Check(`{class: bug}`)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("check failed with %v, want a *Failure", err)
	}
	if msg := f.Repair(c, ""); strings.Contains(msg, "An answer of this shape") {
		t.Errorf("the repair offers an example:\n%s", msg)
	}
}

// An example is built beside the pattern, not out of it. Check matches against the pattern
// after a repair has rendered one, and a literal of the pattern put into the example would
// name the example as its parent.
func TestExampleLeavesThePatternLinked(t *testing.T) {
	c := mustParse(t, `{stage: reviewed, class: !or [bug, nit], n: !irtype 0}`)
	c.Example()
	for _, at := range unlinked(c.accept, "") {
		t.Errorf("after Example, %s names a parent that does not hold it", at)
	}
}
