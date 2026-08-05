// Package answer is the contract a spawn puts on its own final message.
//
// An agent ends by talking: the loop stops when the model stops calling tools, and the
// result is whatever prose it stopped on (task/harness/llm.go). So "the model finished
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
// task cannot succeed without satisfying its contract, the deref downstream of it
// cannot fail — the failure mode is deleted rather than made rarer.
//
// The contract is written as a tony schema's `accept` clause, which is the vocabulary
// verse already publishes for describing documents (see package schema, whose own comment
// holds a place for the first deliberately authored validating schema — this is it).
//
// WHAT GOES BACK TO THE MODEL is the load-bearing part, and it is not something a bare
// match could give: `accept` is a match, and a match answers yes or no. tony.Explaining
// (go-tony v0.0.102, filed from here as axp74f1wh12krznjcxn0) is what turns the verdict
// into a diagnosis — the path of each failing node, what was expected there, what was
// found, absent kept apart from present-but-wrong, and every failing field rather than the
// first, so one repair round can fix them all.
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
// failure is invisible. The honest end of an unconvincible model is a failed task.
const DefaultRepairs = 2

// ParseSource reads a contract from tony source — the hand-written form, for a test or a
// caller holding text rather than a document. A charter carries the document itself.
func ParseSource(src string) (*Contract, error) {
	doc, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		return nil, fmt.Errorf("parse answer contract: %w", err)
	}
	return Parse(doc)
}

// Parse reads a contract from the document a spawn declared. Two forms are accepted, and
// the difference is only ceremony: a whole schema document carrying an `accept:` clause
// (plus any `define:` it needs), or a bare pattern, which is taken AS the accept clause.
//
// A malformed contract is an error here rather than a surprise at check time. This runs on
// the engine side before a spawn is issued (versed.launchOf) as well as in the driver, so
// a charter that wrote a contract it cannot mean fails the firing instead of paying for a
// model call and then failing the check it could never have passed.
func Parse(doc *ir.Node) (*Contract, error) {
	if doc == nil || doc.Type == ir.NullType {
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
	// The source is rendered once, here, and it is what the agent is shown — in the frame
	// up front and in a repair afterwards. Rendered rather than carried as the author's own
	// keystrokes because the author no longer writes text: a contract arrives as the
	// document it is, so this is the only text there is, and it is the same text `verse
	// entity get` prints for the rule.
	src, err := render(s.Accept)
	if err != nil {
		return nil, fmt.Errorf("render answer contract: %w", err)
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

// Source is the contract rendered as tony, for the prompt that asks a model to satisfy it.
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
	doc, parseErr := extract(final)
	if doc == nil {
		return nil, &Failure{Kind: NoDocument, ParseErr: parseErr}
	}
	var why tony.Explanation
	ok, err := tony.MatchWith(doc, c.accept, c.ctx, tony.Explaining(&why))
	if err != nil {
		// A malformed PATTERN, not a bad answer — an unknown operator tag, say. Nothing
		// the model can repair, so it is not a Failure: it is the spawn's own error.
		return nil, fmt.Errorf("answer contract is not usable: %w", err)
	}
	if ok {
		return doc, nil
	}
	return nil, &Failure{Kind: Mismatched, Why: &why}
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
	Kind Kind
	// Why is the match's own account of itself: a frame per failing node, each carrying a
	// kpath into the ANSWER, the pattern that judged it, the value found, and which kind of
	// wrong it is (tony.ReasonAbsent, ReasonValue, ReasonOp, …). nil when Kind is
	// NoDocument, since nothing was matched.
	Why *tony.Explanation
	// ParseErr is why the text that was closest to a document is not one: the parser's own
	// complaint, naming the line. Set when Kind is NoDocument and something was tried and
	// broke; nil when there was nothing document-shaped to break — prose, or a bare word.
	//
	// It is kept because "no document" and "a document with a syntax error in it" are the
	// same nil node to a caller holding only the node, and the difference is the entire
	// content of the diagnosis. A model told only that it "carries no answer document" when
	// it DID write one has nothing to act on, and spends the repair budget on the container
	// while the broken line goes unmentioned (friction:entry:f024).
	ParseErr error
}

func (f *Failure) Error() string {
	if f.Kind == NoDocument {
		if f.ParseErr != nil {
			return "the final message carries no answer document: " + f.ParseErr.Error()
		}
		return "the final message carries no answer document"
	}
	lines := f.lines()
	if len(lines) == 0 {
		return "the answer does not satisfy the contract"
	}
	return "the answer does not satisfy the contract: " + strings.Join(lines, "; ")
}

// lines is the explanation one failure per line. It is tony's own rendering — the paths and
// the bounded node printing are the match's to describe, not this package's to re-derive.
func (f *Failure) lines() []string {
	if f.Why == nil {
		return nil
	}
	s := strings.TrimSpace(f.Why.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
		// Said as the parser's complaint rather than as "your document is wrong here",
		// because this branch is also reached by pure narration, where there is no document
		// to be wrong and the complaint is about a sentence. Phrased that way it is
		// actionable when a document was meant and inert when one was not.
		if f.ParseErr != nil {
			fmt.Fprintf(&b, "What the parser said about the closest thing to one: %v\n", f.ParseErr)
		}
	default:
		b.WriteString("Your final message did not satisfy this spawn's answer contract:\n")
		for _, line := range f.lines() {
			fmt.Fprintf(&b, "  %s\n", line)
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
	n, _ := extract(text)
	return n
}

// extract is Extract with the reason kept: nil node and nil error is "there was no document
// here", nil node and an error is "there was one and it is broken at this line". Only the
// contract check has any use for the difference — a repair is written from it — so the
// exported form stays the plain question the mirror asks.
//
// WHICH error, when the text offers several candidates, is the whole subtlety. A message that
// narrates and then fences fails the whole-message parse on its own prose, at line 0, every
// time: that error is always available and never the one that matters. So a fenced block's
// error wins over the whole message's, and the whole message's is reported only when there
// was no fence to have a better one from — which is also the case that motivated this, an
// agent whose entire message was an unquoted, unfenced document.
func extract(text string) (*ir.Node, error) {
	n, wholeErr := parseComposite(text)
	if n != nil {
		return n, nil
	}
	blocks := fenced(text)
	var blockErr error
	for i := len(blocks) - 1; i >= 0; i-- { // last first: the answer follows the narration
		bn, err := parseComposite(blocks[i])
		if bn != nil {
			return bn, nil
		}
		if blockErr == nil {
			blockErr = err
		}
	}
	if blockErr != nil {
		return nil, blockErr
	}
	return nil, wholeErr
}

// render prints the accept pattern for a model to read: one field per line, each value in
// tony's compact wire form. The pretty encoder breaks a tagged list across four lines
// (`class: !or` / `[` / `bug` / …), which is correct tony and unreadable as a statement of
// what an answer must look like — and this text is the whole of what the agent is told.
func render(accept *ir.Node) (string, error) {
	if accept == nil || accept.Type != ir.ObjectType || accept.Tag != "" {
		b, err := codec.NodeBytesWire(accept, codec.Tony)
		return strings.TrimSpace(string(b)), err
	}
	var b strings.Builder
	for i, f := range accept.Fields {
		v, err := codec.NodeBytesWire(accept.Values[i], codec.Tony)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s: %s\n", f.String, strings.TrimSpace(string(v)))
	}
	return strings.TrimRight(b.String(), "\n"), nil
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

// parseComposite is "this text as an object or a list, or why it is not one". A parse that
// failed reports its error; a parse that SUCCEEDED into a scalar does not, because a bare
// word is not a broken document — nothing there is wrong for a model to fix.
func parseComposite(s string) (*ir.Node, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	n, err := codec.ParseNode([]byte(s), codec.Tony)
	if err != nil {
		return nil, err
	}
	if n == nil || (n.Type != ir.ObjectType && n.Type != ir.ArrayType) {
		return nil, nil
	}
	return n, nil
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
