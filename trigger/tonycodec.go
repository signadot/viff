package trigger

// The charter's tony surface.
//
// The grammar this package implements is a TAGGED one — a formula's operator and a
// requirement's kind are tags, not field names:
//
//	when: !until [ !quorum 2, !fact motion:deadline ]
//	subject: demo:pr:*
//
// JSON has no tags, so the JSON codecs next door spell the same grammar with the tag as a
// single object key (`{"until": [...]}`). That was always an emulation of this; these
// methods are the thing itself. Both spellings decode here, so a charter written either way
// means the same, and codec.Detag renders a tagged document as JSON by producing exactly
// the key form the JSON codecs already accept.
//
// Hand-written rather than generated because a tag is not a field: no amount of struct
// tagging expresses "this value's TYPE is carried by its tag". tony-codegen is told so with
// codec=custom, so it resolves these types without generating over them.

import (
	"fmt"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// ── Match ────────────────────────────────────────────────────────────────────

// ToTonyIR emits the address string when the match names only system/kind/id, and the
// object form otherwise. `subject: demo:pr:*` is how a charter is written by hand, and a
// round trip should give it back rather than expanding it into three fields.
func (m Match) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if m == nil {
		return ir.Null(), nil
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if addr, ok := m.address(); ok {
		return ir.FromString(addr), nil
	}
	// Canonical dimension order, not map order: ir.FromMap sorts, and
	// {fields, id, kind, system} reads worse than the order people write.
	var kvs []ir.KeyVal
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID, mKeyOp} {
		if v := m.str(k); v != "" {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(v)})
		}
	}
	for _, k := range []string{mKeyFields, mKeyEntity} {
		f, ok := asFields(m[k])
		if !ok {
			continue
		}
		node, err := gomap.ToTonyIR(f)
		if err != nil {
			return nil, fmt.Errorf("match %q: %w", k, err)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: node})
	}
	if len(kvs) == 0 {
		return ir.FromKeyVals(nil), nil // {} — the empty match, which matches everything
	}
	return ir.FromKeyVals(kvs), nil
}

// address renders the match as "system:kind:id" when it carries only those dimensions,
// with "*" for a gap. Reported false when a payload predicate or an op is present (no
// string can carry those), when the match is empty, or when a segment contains a ":" that
// would make the result ambiguous to ParseExpr.
func (m Match) address() (string, bool) {
	if _, ok := asFields(m[mKeyFields]); ok {
		return "", false
	}
	if _, ok := asFields(m[mKeyEntity]); ok {
		return "", false
	}
	if m.str(mKeyOp) != "" {
		return "", false
	}
	sys, kind, id := m.System(), m.Kind(), m.ID()
	if sys == "" && kind == "" && id == "" {
		return "", false
	}
	// Only the id may contain ":" — ParseExpr splits into at most three segments, so a
	// colon anywhere else would re-parse as something different.
	if strings.ContainsRune(sys, ':') || strings.ContainsRune(kind, ':') {
		return "", false
	}
	segs := []string{sys, kind, id}
	last := 0
	for i, s := range segs {
		if s != "" {
			last = i
		}
	}
	for i := range segs[:last+1] {
		if segs[i] == "" {
			segs[i] = "*"
		}
	}
	return strings.Join(segs[:last+1], ":"), true
}

// FromTonyIR accepts the address string or the object form — the same two shapes the JSON
// codec takes, since this is one grammar in two syntaxes.
func (m *Match) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		*m = nil
		return nil
	}
	// A `!match` tag is accepted and never emitted: the match atom is written unwrapped,
	// but someone may spell it explicitly.
	if tag := tagName(node); tag != "" && tag != string(ReqMatch) {
		return fmt.Errorf("match: unexpected tag %q", node.Tag)
	}
	switch node.Type {
	case ir.StringType:
		parsed, err := ParseExpr(node.String)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	case ir.ObjectType:
		raw := map[string]any{}
		if err := gomap.FromTonyIR(node, &raw); err != nil {
			return fmt.Errorf("match: %w", err)
		}
		*m = Match(raw)
		return m.Validate()
	}
	return fmt.Errorf("match: want an object or a \"system:kind:id\" string, got %v", node.Type)
}

// ── Formula ──────────────────────────────────────────────────────────────────

// ToTonyIR emits the tagged form: an operator tags its operand list, a requirement tags its
// compact value, and a match atom is emitted UNWRAPPED so a `when` reads back the way it was
// written.
func (f Formula) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if f.Op == OpAtom {
		if f.Req == nil {
			return ir.Null(), nil
		}
		if f.Req.Kind == ReqMatch {
			return f.Req.Facts.ToTonyIR(opts...)
		}
		val, err := f.Req.valueNode(opts...)
		if err != nil {
			return nil, err
		}
		return val.WithTag("!" + string(f.Req.Kind)), nil
	}
	args := make([]*ir.Node, 0, len(f.Args))
	for i, a := range f.Args {
		n, err := a.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("formula %q arg %d: %w", f.Op, i, err)
		}
		args = append(args, n)
	}
	return ir.FromSlice(args).WithTag("!" + string(f.Op)), nil
}

// valueNode renders a requirement's compact value — the thing the kind tag is attached to.
// It mirrors compactValue (the JSON side) so the two spellings stay one grammar.
func (r *Requirement) valueNode(opts ...gomap.MapOption) (*ir.Node, error) {
	switch {
	case r.Kind == ReqDeadline:
		return ir.FromString(r.Within), nil
	case r.Kind == ReqMatch:
		return r.Facts.ToTonyIR(opts...)
	}
	factsSet := r.Facts.System() != "" || r.Facts.Kind() != "" || len(r.Facts.Fields()) > 0
	switch {
	case r.Choice == "" && !factsSet:
		return ir.FromInt(int64(r.N)), nil
	case r.N == 0 && r.Choice == "" && len(r.Facts.Fields()) == 0:
		if r.Facts.Kind() != "" {
			return ir.FromString(r.Facts.System() + ":" + r.Facts.Kind()), nil
		}
		return ir.FromString(r.Facts.System()), nil
	}
	var kvs []ir.KeyVal
	if r.N != 0 {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("n"), Val: ir.FromInt(int64(r.N))})
	}
	if r.Choice != "" {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("choice"), Val: ir.FromString(r.Choice)})
	}
	if factsSet {
		n, err := r.Facts.ToTonyIR(opts...)
		if err != nil {
			return nil, err
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("facts"), Val: n})
	}
	return ir.FromKeyVals(kvs), nil
}

// FromTonyIR reads the tagged form, and also the two untagged shapes that came from JSON:
// the single-key tag-as-key object, and the verbose {op, req, args} parse node.
//
// The tagged path needs no keyspace analysis at all — a tag lives in a different namespace
// from a field name, so `!quorum` cannot be confused with a match dimension. The ordered
// classification the JSON decoder needs (see hasMatchKey/isVerboseNode) applies only to the
// untagged shapes, which is why it stayed over there.
func (f *Formula) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		*f = Formula{}
		return nil
	}

	if tag := tagName(node); tag != "" {
		return f.fromTagged(tag, node)
	}

	switch node.Type {
	case ir.StringType: // address sugar: when: demo:pr:*
		m, err := ParseExpr(node.String)
		if err != nil {
			return err
		}
		*f = *matchFormula(m)
		return nil
	case ir.ObjectType:
		if verbose, err := f.fromVerbose(node); verbose {
			return err
		}
		if keyed, err := f.fromTagKeyed(node); keyed {
			return err
		}
		var m Match
		if err := m.FromTonyIR(node); err != nil {
			return err
		}
		*f = *matchFormula(m)
		return nil
	}
	return fmt.Errorf("formula: want a tagged value, a match object, or a ref string, got %v", node.Type)
}

// fromTagged reads `!until [...]` / `!quorum 2` — the native form.
func (f *Formula) fromTagged(tag string, node *ir.Node) error {
	switch op := FormulaOp(tag); op {
	case OpUntil, OpEventually, OpAlways, OpSequence:
		args, err := formulaList(node)
		if err != nil {
			return fmt.Errorf("formula %q: %w", tag, err)
		}
		*f = Formula{Op: op, Args: args}
		return nil
	case OpAtom: // an explicitly tagged atom carries the verbose node
		if verbose, err := f.fromVerbose(node); verbose {
			return err
		}
	}
	if tag == string(ReqMatch) {
		var m Match
		if err := m.FromTonyIR(node); err != nil {
			return err
		}
		*f = *matchFormula(m)
		return nil
	}
	if !isRequirementKind(tag) {
		return fmt.Errorf("formula: unknown tag %q (want an operator "+
			"(until/eventually/always/sequence) or a requirement "+
			"(match/quorum/fact/deadline/judgment/ask/default))", "!"+tag)
	}
	req, err := requirementFromNode(RequirementKind(tag), node)
	if err != nil {
		return err
	}
	*f = Formula{Op: OpAtom, Req: req}
	return nil
}

// fromTagKeyed reads the JSON spelling — a single-key object whose key is the tag. It is
// what makes a .json charter and a .tony charter the same grammar rather than two.
func (f *Formula) fromTagKeyed(node *ir.Node) (bool, error) {
	if len(node.Fields) != 1 || len(node.Values) != 1 {
		return false, nil
	}
	key := node.Fields[0].String
	if !isRequirementKind(key) {
		switch FormulaOp(key) {
		case OpUntil, OpEventually, OpAlways, OpSequence:
		default:
			return false, nil
		}
	}
	// A match dimension is never a formula tag — the disjointness the JSON grammar rests
	// on, asserted by TestMatchKeysDisjointFromFormulaTags.
	if matchKeys[key] {
		return false, nil
	}
	return true, f.fromTagged(key, unwrap(node.Values[0]))
}

// fromVerbose reads the internal {op, req, args} parse node, kept for compatibility.
func (f *Formula) fromVerbose(node *ir.Node) (bool, error) {
	opNode := unwrap(ir.Get(node, "op"))
	if opNode == nil || opNode.Type != ir.StringType {
		return false, nil
	}
	switch FormulaOp(opNode.String) {
	case OpAtom, OpUntil, OpEventually, OpAlways, OpSequence:
	default:
		return false, nil
	}
	for _, k := range node.Fields {
		if k.String != "op" && k.String != "req" && k.String != "args" {
			return false, nil
		}
	}
	out := Formula{Op: FormulaOp(opNode.String)}
	if reqNode := unwrap(ir.Get(node, "req")); reqNode != nil && reqNode.Type == ir.ObjectType {
		var r Requirement
		if err := r.fromNode(reqNode); err != nil {
			return true, err
		}
		out.Req = &r
	}
	if argsNode := unwrap(ir.Get(node, "args")); argsNode != nil && argsNode.Type == ir.ArrayType {
		args, err := formulaList(argsNode)
		if err != nil {
			return true, err
		}
		out.Args = args
	}
	*f = out
	return true, nil
}

// formulaList reads an operator's operands: a list, or a single sub-formula accepted as a
// one-element list.
func formulaList(node *ir.Node) ([]*Formula, error) {
	if node.Type == ir.ArrayType {
		out := make([]*Formula, 0, len(node.Values))
		for i, v := range node.Values {
			var sub Formula
			if err := sub.FromTonyIR(v); err != nil {
				return nil, fmt.Errorf("arg %d: %w", i, err)
			}
			out = append(out, &sub)
		}
		return out, nil
	}
	var one Formula
	if err := one.FromTonyIR(node); err != nil {
		return nil, err
	}
	return []*Formula{&one}, nil
}

// requirementFromNode reads the compact value after a kind tag: a number is a threshold, a
// string is a "system:kind" selector (or a duration for a deadline), an object carries the
// full fields. It switches on the node's TYPE rather than trying decodes in turn, which is
// what the JSON side has to do — and so it cannot swallow a decode error by accident.
func requirementFromNode(kind RequirementKind, node *ir.Node) (*Requirement, error) {
	node = unwrap(node)
	if node == nil {
		return &Requirement{Kind: kind}, nil
	}
	switch node.Type {
	case ir.NumberType:
		// A deadline has no threshold, so a number here is always a mistake — and almost
		// always the same one: an unquoted duration. A tony literal may not start with a
		// digit and commas are optional, so `!deadline 4h` reads as the number 4 followed
		// by a separate literal "h" — correctly, but not at all as meant, and the 4 lands
		// right here. Left to fall through it would set N and leave Within empty, which
		// is a deadline that silently isn't one. This is the only place it can be caught:
		// the document is well-formed, so nothing upstream of us has anything to report.
		if kind == ReqDeadline {
			return nil, fmt.Errorf("requirement %q: a deadline takes a duration, not a number "+
				"(quote it: !deadline \"4h\")", kind)
		}
		n, err := nodeInt(node)
		if err != nil {
			return nil, fmt.Errorf("requirement %q: %w", kind, err)
		}
		return &Requirement{Kind: kind, N: n}, nil
	case ir.StringType:
		if kind == ReqDeadline {
			return &Requirement{Kind: kind, Within: node.String}, nil
		}
		return &Requirement{Kind: kind, Facts: factSelFromRef(node.String)}, nil
	case ir.ObjectType:
		r := Requirement{Kind: kind}
		if err := r.fromNode(node); err != nil {
			return nil, err
		}
		r.Kind = kind // the tag names the kind; a `kind` field cannot override it
		return &r, nil
	}
	return nil, fmt.Errorf("requirement %q: want a threshold, a \"system:kind\", or an object, got %v",
		kind, node.Type)
}

// fromNode fills a Requirement from its object form, in either spelling: `facts` is the
// wire name, and a bare match object is accepted for the verbose {op,req,args} node.
func (r *Requirement) fromNode(node *ir.Node) error {
	if k := unwrap(ir.Get(node, "kind")); k != nil && k.Type == ir.StringType {
		r.Kind = RequirementKind(k.String)
	}
	if n := unwrap(ir.Get(node, "n")); n != nil && n.Type == ir.NumberType {
		v, err := nodeInt(n)
		if err != nil {
			return err
		}
		r.N = v
	}
	if c := unwrap(ir.Get(node, "choice")); c != nil && c.Type == ir.StringType {
		r.Choice = c.String
	}
	if w := unwrap(ir.Get(node, "within")); w != nil && w.Type == ir.StringType {
		r.Within = w.String
	}
	if fnode := unwrap(ir.Get(node, "facts")); fnode != nil {
		return r.Facts.FromTonyIR(fnode)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// unwrap steps past a comment wrapper. A charter file is exactly where a human writes
// comments, and a commented value is a CommentType node holding the real one.
func unwrap(n *ir.Node) *ir.Node {
	for n != nil && n.Type == ir.CommentType && len(n.Values) > 0 {
		n = n.Values[0]
	}
	return n
}

// tagName is a node's semantic tag without the "!", ignoring the layout hints a parser
// attaches to record how the source was written.
func tagName(n *ir.Node) string {
	tag := ir.TagRemove(ir.TagRemove(n.Tag, "!bracket"), "!literal")
	if tag == "" {
		return ""
	}
	head, _ := ir.HeadTag(tag)
	return strings.TrimPrefix(head, "!")
}

func nodeInt(n *ir.Node) (int, error) {
	if n.Int64 != nil {
		return int(*n.Int64), nil
	}
	return 0, fmt.Errorf("want a whole number, got %q", n.Number)
}

// ── Condition ────────────────────────────────────────────────────────────────

// Condition is hand-written for two reasons, both about its Subject.
//
// It is a Match — a named map with its own codec — and gomap's REFLECTION path does not
// dispatch to a value field's codec on the way IN (it does on the way out, and generated
// code does both; filed as tony-format#f69agjyeh12ksa25cnn0 item 15). Writing the decode
// explicitly makes Condition correct however it is reached.
//
// And `omitzero` is inert on a field whose type carries a custom codec, so a generated
// Condition emitted `subject: null` on every charter that does not name one — the same
// shape as item 12. Here, an absent Subject is simply absent.
func (c Condition) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	if c.When != nil {
		w, err := c.When.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("condition.when: %w", err)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("when"), Val: w})
	}
	if len(c.Subject) > 0 {
		s, err := c.Subject.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("condition.subject: %w", err)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("subject"), Val: s})
	}
	return ir.FromKeyVals(kvs), nil
}

func (c *Condition) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		*c = Condition{}
		return nil
	}
	if node.Type != ir.ObjectType {
		return fmt.Errorf("condition: want an object, got %v", node.Type)
	}
	if w := unwrap(ir.Get(node, "when")); w != nil && w.Type != ir.NullType {
		var f Formula
		if err := f.FromTonyIR(w, opts...); err != nil {
			return fmt.Errorf("condition.when: %w", err)
		}
		c.When = &f
	}
	if s := unwrap(ir.Get(node, "subject")); s != nil && s.Type != ir.NullType {
		if err := c.Subject.FromTonyIR(s, opts...); err != nil {
			return fmt.Errorf("condition.subject: %w", err)
		}
	}
	return nil
}
