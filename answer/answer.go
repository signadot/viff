// Package answer is the contract a spawn puts on its own final message.
//
// An agent ends by talking: the loop stops when the model stops calling tools, and the
// result is whatever prose it stopped on (process/harness/llm.go). So "the model finished
// the job" and "the model stopped talking" are the same observation, and a charter that
// derefs `$data.class` finds out at PATCH time — after the work is done and the tokens are
// spent — that the agent narrated instead of answering. The failure lands on a hook nobody
// is watching, and the entity is stranded at a stage no rule matches.
//
// A contract moves that failure earlier and onto a path the charter already handles. The
// spawn declares the shape its answer must have; the driver validates the final message
// before it will call the run done; a failure is fed back to the model as a DIAGNOSIS it
// can act on, bounded at a couple of attempts; and a run that still cannot produce the
// shape FAILS, which is `onFailed`, which every rule already declares a branch for. If a
// process cannot succeed without satisfying its contract, the deref downstream of it
// cannot fail — the failure mode is deleted rather than made rarer.
//
// The contract is written as a tony schema's `accept` clause, which is the vocabulary
// verse already publishes for describing documents (see package schema, whose own comment
// holds a place for the first deliberately authored validating schema — this is it).
//
// It travels as SOURCE TEXT rather than as a parsed node, and that is not laziness. A
// contract is dense with tony's own operators (`!or`, `!irtype`), and the charter that
// declares one is itself persisted as an entity: a parsed pattern in a payload is a
// document full of operators being written into a store that interprets operators. Text
// crosses that seam intact, needs no escape, and is exactly how Frame — the other field a
// spawn authors for itself rather than names — already travels.
package answer

import (
	"fmt"
	"strings"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/tony-format/go-tony/mergeop"
	tschema "github.com/signadot/tony-format/go-tony/schema"

	"github.com/signadot/verse/codec"
)

// Contract is a parsed, ready-to-check answer contract.
type Contract struct {
	src      string
	accept   *ir.Node // the accept pattern, definition references already expanded
	ctx      *mergeop.OpContext
	maxTries int
}

// DefaultRepairs is how many times a driver re-asks after a failed check. Two, and bounded
// on purpose: if a model cannot produce the shape with the specific complaint in front of
// it, more turns will not teach it, and an unbounded repair loop is a token sink whose
// failure is invisible. The honest end of an unconvincible model is a failed process.
const DefaultRepairs = 2

// Parse reads a contract from tony source. Two forms are accepted, and the difference is
// only ceremony: a whole schema document carrying an `accept:` clause (plus any `define:`
// it needs), or a bare pattern, which is taken AS the accept clause.
//
// A malformed contract is an error here rather than a surprise at check time. This runs on
// the engine side before a spawn is issued (versed.launchOf) as well as in the driver, so
// a charter that wrote a contract it cannot mean fails the firing instead of paying for a
// model call and then failing the check it could never have passed.
func Parse(src string) (*Contract, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("empty answer contract")
	}
	doc, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		return nil, fmt.Errorf("parse answer contract: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("empty answer contract")
	}
	// Which of the two forms this is has to be decided on what the author WROTE, not on
	// whether the schema parser liked it. Falling back to the bare-pattern reading whenever
	// ParseSchema errors would take a schema with a real defect — an impossible definition
	// cycle, say, which ParseSchema does diagnose — and quietly reinterpret it as a demand
	// that the answer contain fields called `signature` and `define`. The author would get a
	// mismatch about their own schema keywords instead of the error they were handed.
	s := &tschema.Schema{Accept: doc}
	if hasField(doc, "accept") {
		parsed, err := tschema.ParseSchema(doc)
		if err != nil {
			return nil, fmt.Errorf("parse answer contract: %w", err)
		}
		if parsed.Accept == nil {
			return nil, fmt.Errorf("answer contract has no accept clause")
		}
		s = parsed
	}
	env := tschema.BuildDefEnv(s)
	opts := tschema.BuildEvalOptions(s)
	expanded, err := tschema.ExpandDefBody(s.Accept, env, opts)
	if err != nil {
		return nil, fmt.Errorf("expand answer contract: %w", err)
	}
	return &Contract{
		src:    src,
		accept: expanded,
		// The context is what lets `.[ref]` expand lazily inside operators during the
		// match, the same way schema.Validate does it.
		ctx:      &mergeop.OpContext{DefEnv: env, EvalOpts: opts},
		maxTries: DefaultRepairs,
	}, nil
}

// Source is the contract as written, for the prompt that asks a model to satisfy it: the
// author's own text is a better statement of intent than anything re-rendered from the IR.
func (c *Contract) Source() string { return c.src }

// Repairs is how many re-asks this contract allows before the run fails.
func (c *Contract) Repairs() int { return c.maxTries }

// Check validates an agent's final message against the contract, returning the document it
// was satisfied by.
//
// The message is EXTRACTED before it is validated (see Extract), so the contract judges the
// answer rather than the layout. A fenced answer is a formatting habit; spending one of two
// repair attempts teaching a model not to fence would be spending the budget on the wrong
// thing.
//
// A nil error means the answer is good and the returned node is what a charter's `$data`
// will deref — the same value mirror projects, because both go through Extract.
func (c *Contract) Check(final string) (*ir.Node, error) {
	doc := Extract(final)
	if doc == nil {
		return nil, &Failure{Kind: NoDocument}
	}
	ok, err := tony.MatchWith(doc, c.accept, c.ctx)
	if err != nil {
		// A malformed PATTERN, not a bad answer — an unknown operator tag, say. Nothing
		// the model can repair, so it is not a Failure: it is the spawn's own error.
		return nil, fmt.Errorf("answer contract is not usable: %w", err)
	}
	if ok {
		return doc, nil
	}
	return nil, &Failure{Kind: Mismatched, Mismatches: explain(doc, c.accept, c.ctx)}
}

// Kind distinguishes the two ways an answer fails its contract, because they need different
// things said back: one is "you did not answer", the other is "your answer is wrong here".
type Kind int

const (
	// NoDocument: the final message carries no tony object or list at all — the agent
	// narrated. This is the failure the contract exists for.
	NoDocument Kind = iota
	// Mismatched: a document, but not one the contract accepts.
	Mismatched
)

// Failure is a contract check that the model could plausibly fix. It is a distinct type
// from an ordinary error because the driver treats it differently: a Failure is fed back
// and retried, anything else fails the run at once.
type Failure struct {
	Kind       Kind
	Mismatches []Mismatch
}

func (f *Failure) Error() string {
	if f.Kind == NoDocument {
		return "the final message carries no answer document"
	}
	parts := make([]string, 0, len(f.Mismatches))
	for _, m := range f.Mismatches {
		parts = append(parts, m.String())
	}
	if len(parts) == 0 {
		// The explainer found nothing to say — possible when the whole pattern is one
		// operator whose internals it cannot see into. Better a bare truth than a
		// confident-sounding wrong path.
		return "the answer does not satisfy the contract"
	}
	return "the answer does not satisfy the contract: " + strings.Join(parts, "; ")
}

// Repair is what goes back to the model. It is a DIAGNOSIS, not a re-ask: the distinction
// is the whole design, since asking a model to try again teaches it nothing about what was
// wrong. It is the same move the loop already makes for a denied gate — an error specific
// enough to re-plan around — applied to the terminal message rather than to a tool result.
//
// The last line matters more than it looks. A repair loop pressures a model toward
// COMPLIANCE, and a model told twice that `severity` must be one of three values will pick
// one whether or not it knows. Naming the honest exit is what keeps the contract a
// guarantee about shape rather than an invitation to invent content.
func (f *Failure) Repair(c *Contract, failTool string) string {
	var b strings.Builder
	switch f.Kind {
	case NoDocument:
		b.WriteString("Your final message was not an answer: this spawn declares an answer " +
			"contract, and nothing in what you said parses as a document.\n")
	default:
		b.WriteString("Your final message did not satisfy this spawn's answer contract:\n")
		for _, m := range f.Mismatches {
			fmt.Fprintf(&b, "  %s\n", m)
		}
	}
	b.WriteString("\nThe contract is:\n\n")
	for _, line := range strings.Split(strings.TrimRight(c.Source(), "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	b.WriteString("\nReply with the corrected answer and nothing else — no preamble, no " +
		"commentary after it. Everything you want to say about the work has already been said.\n")
	if failTool != "" {
		fmt.Fprintf(&b, "If you cannot answer truthfully in this shape — the value is not "+
			"something you were able to determine — call %s with the reason instead. Do not "+
			"invent a value to satisfy the shape.\n", failTool)
	}
	return b.String()
}

// Extract pulls the answer document out of an agent's final message: the message parsed as
// tony when the whole of it is a document, else the last fenced block that is one.
//
// It accepts only an OBJECT or a LIST. The parser is strict enough that English fails it
// outright, so this gate is not about prose accidentally becoming a record — it is that a
// bare word parses fine as a tony string ("high", "yes" and "LGTM!" all decode to String),
// so without the gate every one-word answer would come back as structure and the
// distinction would signal nothing.
//
// Fences are accepted because they are the observed failure: an agent that has just read
// nine files narrates and fences before it answers. Nothing more heuristic than that —
// mining a trailing {…} out of a paragraph would turn a sentence that happens to contain
// braces into a record, and a wrong document is worse than no document.
//
// It is the ONE definition of "the data in an answer", used by the contract check and by
// the mirror's projection both, so what a contract validated and what `$data` derefs cannot
// be different documents.
func Extract(text string) *ir.Node {
	if n := parseComposite(text); n != nil {
		return n
	}
	blocks := fenced(text)
	for i := len(blocks) - 1; i >= 0; i-- { // last first: the answer follows the narration
		if n := parseComposite(blocks[i]); n != nil {
			return n
		}
	}
	return nil
}

// hasField reports whether an object document carries a top-level field — how Parse tells
// the schema form from the bare-pattern one.
func hasField(doc *ir.Node, name string) bool {
	if doc.Type != ir.ObjectType {
		return false
	}
	for _, f := range doc.Fields {
		if f.String == name {
			return true
		}
	}
	return false
}

func parseComposite(s string) *ir.Node {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	n, err := codec.ParseNode([]byte(s), codec.Tony)
	if err != nil || n == nil {
		return nil
	}
	if n.Type != ir.ObjectType && n.Type != ir.ArrayType {
		return nil
	}
	return n
}

// fenced returns the contents of each ``` block, ignoring any language tag. An unclosed
// fence is taken to run to the end of the message, since a truncated answer is still the
// answer the agent meant.
func fenced(text string) []string {
	const fence = "```"
	var out []string
	rest := text
	for {
		i := strings.Index(rest, fence)
		if i < 0 {
			return out
		}
		rest = rest[i+len(fence):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:] // drop the language tag line
		} else {
			return out // a fence marker with nothing after it
		}
		j := strings.Index(rest, fence)
		if j < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j+len(fence):]
	}
}
