package launch

import (
	"github.com/signadot/verse/task"
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
)

func TestBindExpandsSubject(t *testing.T) {
	s := Spec{
		Driver:      "harness",
		Prompt:      "review $subject and report",
		Environment: "sandbox-$subject",
		Vector:      "supervised",
	}
	got := s.Bind(Binding{"subject": `demo.pr."1"`})

	if got.Prompt != `review demo.pr."1" and report` {
		t.Fatalf("Prompt = %q", got.Prompt)
	}
	if got.Environment != `sandbox-demo.pr."1"` {
		t.Fatalf("Environment = %q", got.Environment)
	}
	// fields without a $var are untouched
	if got.Driver != "harness" || got.Vector != "supervised" {
		t.Fatalf("plain fields changed: %+v", got)
	}
}

func TestBindLeavesUnboundVarsAndIsPure(t *testing.T) {
	s := Spec{Prompt: "do $subject for $owner"}
	got := s.Bind(Binding{"subject": "x.y.1"}) // no "owner" binding
	if got.Prompt != "do x.y.1 for $owner" {
		t.Fatalf("unbound var not preserved: %q", got.Prompt)
	}
	if s.Prompt != "do $subject for $owner" {
		t.Fatalf("Bind mutated the receiver: %q", s.Prompt)
	}
}

// Longer variable names bind first, so a short name is not a prefix-hazard.
func TestBindLongestKeyFirst(t *testing.T) {
	s := Spec{Prompt: "$subject vs $sub"}
	got := s.Bind(Binding{"sub": "SHORT", "subject": "LONG"})
	if got.Prompt != "LONG vs SHORT" {
		t.Fatalf("prefix collision: %q", got.Prompt)
	}
}

// A frame is a template like the task it frames: a charter that says "answer about
// $subject" needs the binding, and a frame that could not carry one would push every
// per-firing detail back into the task.
func TestBindExpandsFrame(t *testing.T) {
	s := Spec{Frame: "You review $subject. Answer with a severity and nothing else."}
	got := s.Bind(Binding{"subject": `demo.pr."1"`})
	if got.Frame != `You review demo.pr."1". Answer with a severity and nothing else.` {
		t.Fatalf("Frame = %q", got.Frame)
	}
}

// Tools are deliberately NOT expanded, and that asymmetry is the interesting one. Every
// other string here is a template because a charter has reason to build it out of the
// firing — but a tool name comes from a fixed set the driver defines, so a $var in one
// could only name a tool that does not exist, or name one chosen by whoever last wrote the
// entity being matched. The second widens authority by binding, one field away from the
// grant.
func TestBindDoesNotExpandTools(t *testing.T) {
	s := Spec{Tools: tl([]string{"read_file", "$sneaky"})}
	got := s.Bind(Binding{"sneaky": "run_bash"})
	if tools := task.ToolsOf(got.Tools); len(tools) != 2 || tools[1] != "$sneaky" {
		t.Fatalf("Tools = %v, want the authored list verbatim", tools)
	}
}

// A bound spec must not alias the charter's slice, or one firing could rewrite the tool
// list every later firing reads.
func TestBindCopiesTools(t *testing.T) {
	s := Spec{Tools: tl([]string{"read_file"})}
	got := s.Bind(Binding{})
	(*got.Tools)[0] = "run_bash"
	if (*s.Tools)[0] != "read_file" {
		t.Fatalf("Bind aliased the receiver's tools: %v", *s.Tools)
	}
}

// A spawn that names no tools and one that names `tools: []` are two different spawns, and
// stay so through a bind and through the wire.
func TestNoToolsIsNotUnnamed(t *testing.T) {
	none := Spec{Tools: tl([]string{})}.Bind(Binding{})
	if none.Tools == nil || len(*none.Tools) != 0 {
		t.Fatalf("tools: [] bound to %v", none.Tools)
	}
	if unnamed := (Spec{}).Bind(Binding{}); unnamed.Tools != nil {
		t.Fatalf("an unnamed tools bound to %v", *unnamed.Tools)
	}
	b, err := codec.Marshal(Spec{Tools: tl([]string{})}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var back Spec
	if err := codec.Unmarshal(b, &back, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if back.Tools == nil || len(*back.Tools) != 0 {
		t.Fatalf("tools: [] crossed as %v (wire %s)", back.Tools, b)
	}
}

// The answer contract is a DOCUMENT, and it keeps its operators. It could not be one until
// a rule could be stored as structure — a write is a tony patch, so `!or` in a payload used
// to be executed by the store rather than stored by it — and `!raw` (entity.Raw) is what
// settled that. As a document it is the same kind of thing as the `when` it sits beside.
func TestAnswerContractIsADocumentWithItsOperators(t *testing.T) {
	const src = "driver: llm\nanswer:\n  severity: !or [low, high]\n  note: !irtype string\n"
	var s Spec
	if err := codec.Unmarshal([]byte(src), &s, codec.Tony); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// the operators are still operators, which is the whole question: it MATCHES
	if !matches(t, s.Answer, `{severity: low, note: "x"}`) {
		t.Fatal("the contract does not accept a document it describes")
	}
	if matches(t, s.Answer, `{severity: nope, note: "x"}`) {
		t.Fatal("the contract accepts a severity outside its !or")
	}

	// and back out again, so `trigger show` renders the pattern the author wrote
	out, err := codec.Marshal(&s, codec.Tony)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var round Spec
	if err := codec.Unmarshal(out, &round, codec.Tony); err != nil {
		t.Fatalf("re-decode %q: %v", out, err)
	}
	if !matches(t, round.Answer, `{severity: high, note: "x"}`) || matches(t, round.Answer, `{severity: nope}`) {
		t.Fatalf("round trip lost the operator: %q", out)
	}
}

// Bind carries the contract across as written. It is a pattern, not text, so there is
// nothing to substitute into — $var expansion inside a match is the same future work
// Provider.Match is waiting on.
func TestBindCarriesTheAnswerContract(t *testing.T) {
	pat, _ := codec.ParseNode([]byte("severity: !or [low, high]"), codec.Tony)
	got := Spec{Answer: pat}.Bind(Binding{"subject": `demo.pr."1"`})
	if !matches(t, got.Answer, `{severity: low}`) || matches(t, got.Answer, `{severity: nope}`) {
		t.Fatalf("Answer = %v, want the pattern as written", got.Answer)
	}
}

// matches runs a document against a pattern the way the contract check will.
func matches(t *testing.T, pattern *ir.Node, doc string) bool {
	t.Helper()
	d, err := codec.ParseNode([]byte(doc), codec.Tony)
	if err != nil {
		t.Fatalf("parse %q: %v", doc, err)
	}
	ok, err := tony.Match(d, pattern)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	return ok
}
