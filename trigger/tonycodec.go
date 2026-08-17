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
	"github.com/signadot/tony-format/go-tony/mergeop"
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
	// A PATH GOES BACK OUT AS `path:`, the OBJECT form, because that is the spelling that means
	// a path at every length. `!verse-path [a b c]` does not: three steps FOLD into the three
	// dimensions, deliberately, so that the vector spelling names the entity `a:b:c` the way
	// every charter written before paths does. Rendering a path with it turns a PLACE into an
	// address with an id.
	//
	// Which is a restart's worth of damage. A stratum is a place — `{path: [verse-dev, drift,
	// head]}` — and written down as `!verse-path` it came back constraining `id`, so restore
	// refused it, and then refused every rule whose derive needed the level it named. The
	// charter shrank across a restart, in the store's own copy of itself.
	//
	// Without SOME path spelling here a deep rule rendered as `{}`, which was worse again: it
	// installed, was written down as a match on everything, and came back with no subject.
	if m.Deep() {
		segs := m.SegNodes()
		vals := make([]*ir.Node, 0, len(segs))
		vals = append(vals, segs...)
		kvs := []ir.KeyVal{{Key: ir.FromString(mKeyPath), Val: ir.FromSlice(vals)}}
		if node, ok := predicateNode(m[mKeyValue]); ok {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(mKeyValue), Val: node})
		}
		return ir.FromKeyVals(kvs), nil
	}
	// Canonical dimension order, not map order: ir.FromMap sorts, and
	// {fields, id, kind, system} reads worse than the order people write.
	var kvs []ir.KeyVal
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID} {
		// An operator goes back out as a tag, which is how it was written in tony and the
		// only faithful spelling — the tag-keyed input form is sugar, not a rendering.
		if n := m.dim(k); n != nil {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: n})
			continue
		}
		if v := m.str(k); v != "" {
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(k), Val: ir.FromString(v)})
		}
	}
	// The predicate goes back out as the node it is, operators and all — the same
	// faithful-rendering rule the id/op dimensions follow above.
	if node, ok := predicateNode(m[mKeyValue]); ok {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(mKeyValue), Val: node})
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
	if m.HasPredicate() {
		return "", false // no address spelling carries a payload predicate
	}
	// An operator has no address spelling. Rendering one as the string form would drop it
	// — `{id: !glob "a/*"}` becoming "demo:pr" — which is the widest possible match wearing
	// the narrowest one's clothes.
	for _, k := range []string{mKeySystem, mKeyKind, mKeyID} {
		if m.dim(k) != nil {
			return "", false
		}
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
	// `!verse-path [demo pr 1]` — an address as STEPS, checked before the guard below because
	// it is a tag this type accepts rather than one it refuses.
	if tagName(node) == VersePathTag {
		parsed, err := stepsMatch(node)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	}
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
	case ir.ArrayType:
		// An untagged list is not an address — stepsMatch is what says so, and why.
		parsed, err := stepsMatch(node)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	case ir.ObjectType:
		parsed, err := matchFromObject(node)
		if err != nil {
			return err
		}
		*m = parsed
		return m.Validate()
	}
	return fmt.Errorf("match: want an object or a \"system:kind:id\" string, got %v", node.Type)
}

// matchFromObject reads the object form dimension by dimension, rather than flattening the
// whole node with gomap.
//
// The difference is TAGS. gomap decodes into map[string]any, which has nowhere to put one,
// so `{stage: !or [open, merged]}` used to arrive as the list ["open","merged"] — silently a
// different predicate. Reading each dimension in its own terms means a tag either lands
// somewhere that can hold it or is reported, never dropped.
func matchFromObject(node *ir.Node) (Match, error) {
	out := Match{}
	var at Match // the `at:` spelling, merged after the loop so a conflict is caught either way
	for i, key := range node.Fields {
		k := key.String
		val := unwrap(node.Values[i])
		switch k {
		case mKeyPath:
			// `path:` is the object spelling of `!verse-path`, and the two are one dimension:
			// a list of segments, each a match. It is what an address deeper than the three
			// dimensions is carried as, and what a rendering of one comes back in.
			if val == nil || val.Type != ir.ArrayType {
				return nil, fmt.Errorf("match: `%s` is a list of segments (`[demo, pr, \"*\"]`); "+
					"got %v", mKeyPath, valType(val))
			}
			got, err := PathMatch(val.Values)
			if err != nil {
				return nil, err
			}
			at = got

		case mKeyAt: //nolint:dupl // the conflict is checked once, after the loop
			// `at:` is the whole address prefix as ONE covering path — `at: demo.pr` for
			// `{system: demo, kind: pr}`, `at: verse.gate.question` for the same three in
			// verse's own half. It EXPANDS here rather than being carried as a dimension of
			// its own: what a match means is the dimensions, and one address written two ways
			// in one map would be two answers to ask a firing about.
			//
			// It takes the vector spelling too, so `at: !verse-path [demo pr]` reads.
			got, err := atMatch(val)
			if err != nil {
				return nil, err
			}
			at = got

		case mKeySystem, mKeyKind, mKeyID, mKeyRoot:
			// A dimension is a closed grammar — a string, or an operator over one — so an
			// object here is unambiguously the tag-keyed spelling of an operator and can
			// be desugared without asking what the surrounding keys mean.
			n, err := opNode(val)
			if err != nil {
				return nil, fmt.Errorf("match %q: %w", k, err)
			}
			if n != nil {
				out[k] = n
				continue
			}
			if val.Type != ir.StringType {
				return nil, fmt.Errorf("match: %q must be a string, got %v", k, val.Type)
			}
			out[k] = val.String

		case mKeyValue:
			// Kept as the NODE, not decoded to a map: a payload predicate is a tony match
			// evaluated at the payload root, so any operator it carries has to survive to
			// tony.Match. Decoding to map[string]any would drop the tag and silently turn
			// `{stage: !glob "review*"}` into a predicate that matches nothing.
			//
			// Tags used to be rejected here, with an error saying a payload predicate is
			// equality-only. That is no longer true, which is the point of the change.
			out[k] = val

		default:
			// Unknown: decode it so Validate can name it, rather than dropping it here
			// and reporting a match that constrains less than it says.
			var v any
			if err := gomap.FromTonyIR(val, &v); err != nil {
				return nil, fmt.Errorf("match %q: %w", k, err)
			}
			out[k] = v
		}
	}
	// `at:` merges LAST and refuses to overwrite: the two are one address written two ways, and
	// a map holding both has said it twice. Checked here rather than in the loop, so the
	// refusal does not depend on which key the author wrote first.
	for dim, v := range at {
		if _, taken := out[dim]; taken {
			return nil, fmt.Errorf("match: `at` names %s, and so does the match itself — an "+
				"address is written one way or the other", dim)
		}
		out[dim] = v
	}
	return out, nil
}

// opNode converts a dimension's value into a tony match if it carries an operator, in
// either spelling — `!glob "hotfix-*"` or `{glob: "hotfix-*"}` — and returns nil for a
// plain value. The two are one grammar: JSON has no tag syntax, so the tag-keyed object is
// the only way a JSON-shaped charter reaches an operator, exactly as it is for a formula
// (Formula.fromTagKeyed).
//
// Recursive, so operands are desugared too: {"or": ["a", {"glob": "b-*"}]} becomes
// !or [a, !glob "b-*"]. Inside a dimension there are no object VALUES to be confused with,
// which is what makes this unambiguous here and impossible inside `fields`.
func opNode(n *ir.Node) (*ir.Node, error) {
	if n == nil {
		return nil, nil
	}
	// A LAYOUT tag is not an operator. The parser attaches !bracket to anything written in
	// flow style, which is every JSON object — so treating a tag as an operator without
	// looking would make `{"glob": …}` a tagged node that no mergeop ever sees, and the
	// sugar would decode to itself.
	if !isLayoutTag(n.Tag) {
		return desugar(n), nil
	}
	if n.Type != ir.ObjectType {
		return nil, nil
	}
	if len(n.Fields) != 1 {
		return nil, fmt.Errorf("want a string or a single-key operator object, got %d keys",
			len(n.Fields))
	}
	name := n.Fields[0].String
	if mergeop.Lookup(name) == nil {
		return nil, fmt.Errorf("unknown operator %q (write %q as a plain string, or use a "+
			"known operator such as glob/or/not)", name, name)
	}
	return desugar(unwrap(n.Values[0])).WithTag("!" + name), nil
}

// desugar rewrites the tag-keyed spelling into tags throughout a subtree.
func desugar(n *ir.Node) *ir.Node {
	if n == nil {
		return nil
	}
	if n.Type == ir.ObjectType && len(n.Fields) == 1 && isLayoutTag(n.Tag) {
		if name := n.Fields[0].String; mergeop.Lookup(name) != nil {
			return desugar(unwrap(n.Values[0])).WithTag("!" + name)
		}
	}
	out := n.Clone()
	for i, v := range out.Values {
		out.Values[i] = desugar(unwrap(v))
	}
	return out
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
	factsSet := r.Facts.System() != "" || r.Facts.Kind() != "" || r.Facts.HasPredicate()
	switch {
	case r.Choice == "" && !factsSet:
		return ir.FromInt(int64(r.N)), nil
	case r.N == 0 && r.Choice == "" && !r.Facts.HasPredicate():
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

// FromTonyIR reads the tagged form, and also the two untagged shapes a JSON-shaped charter
// writes: the single-key tag-as-key object, and the verbose {op, req, args} parse node.
//
// The tagged path needs no keyspace analysis at all — a tag lives in a different namespace
// from a field name, so `!quorum` cannot be confused with a match dimension. The untagged
// shapes have no such separation and are classified by their KEYS instead (fromVerbose,
// then fromTagKeyed, then a match), which is why the disjointness asserted by
// TestMatchKeysDisjointFromFormulaTags is load-bearing rather than tidy.
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
// headLines is the comment written above a value, or nil — the half of unwrap that unwrap
// throws away. A head comment WRAPS its value, so the lines live on the wrapper.
func headLines(n *ir.Node) []string {
	if n == nil || n.Type != ir.CommentType {
		return nil
	}
	return n.Lines
}

// lineLines is the comment written BESIDE a value — `subject: demo:pr:* # only PRs`. It rides
// the value itself rather than wrapping it, so it is read from INSIDE any head wrapper, which
// is the one asymmetry between the two positions.
func lineLines(n *ir.Node) []string {
	if v := unwrap(n); v != nil && v.Comment != nil {
		return v.Comment.Lines
	}
	return nil
}

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

// Condition is hand-written for two reasons, both about its custom-codec fields.
//
// Subject is a Match — a named map with its own codec — and gomap's REFLECTION path does not
// dispatch to a value field's codec on the way IN (it does on the way out, and generated
// code does both; filed as tony-format#f69agjyeh12ksa25cnn0 item 15). Writing the decode
// explicitly makes Condition correct however it is reached.
//
// And `omitzero` is inert on a field whose type carries a custom codec, so a generated
// Condition emitted `when: null` on every charter that names no when — the same shape as item
// 12. Here, an absent When (or On) is simply absent. The Subject is emitted unconditionally, because a
// condition without one is not installable (Condition.validate) and hiding that in the
// rendering would turn an install error into a document that reads fine.
func (c Condition) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	var kvs []ir.KeyVal
	s, err := c.Subject.ToTonyIR(opts...)
	if err != nil {
		return nil, fmt.Errorf("condition.subject: %w", err)
	}
	kvs = append(kvs, ir.KeyVal{Key: ir.FromString("subject"),
		Val: gomap.ApplyComments(s, c.SubjectDoc, c.SubjectNote)})
	// `on` before `when`: cause then condition, which is the order they are read in.
	if c.On != nil {
		o, err := c.On.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("condition.on: %w", err)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("on"),
			Val: gomap.ApplyComments(o, c.OnDoc, c.OnNote)})
	}
	if c.When != nil {
		w, err := c.When.ToTonyIR(opts...)
		if err != nil {
			return nil, fmt.Errorf("condition.when: %w", err)
		}
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString("when"),
			Val: gomap.ApplyComments(w, c.WhenDoc, c.WhenNote)})
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
	// The comment BEFORE unwrapping, since unwrapping is what discards it. A field absent from
	// the document has no node and so no comment, which is why each read is guarded the same
	// way the value's is.
	c.SubjectDoc, c.OnDoc, c.WhenDoc = headLines(ir.Get(node, "subject")),
		headLines(ir.Get(node, "on")), headLines(ir.Get(node, "when"))
	c.SubjectNote, c.OnNote, c.WhenNote = lineLines(ir.Get(node, "subject")),
		lineLines(ir.Get(node, "on")), lineLines(ir.Get(node, "when"))
	if o := unwrap(ir.Get(node, "on")); o != nil && o.Type != ir.NullType {
		var f Formula
		if err := f.FromTonyIR(o, opts...); err != nil {
			return fmt.Errorf("condition.on: %w", err)
		}
		c.On = &f
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

// VersePathTag marks an address written as STEPS: `!verse-path [demo pr 1]`.
//
// The tag is required rather than inferred from the array. A bare list where an address belongs
// would be a second thing a bare list can mean — the union form a stratum's `match` takes is
// already one — and an address is exactly the place where guessing wrong is expensive.
const VersePathTag = "verse-path"

// stepsMatch reads the tagged-vector spelling of an address into the same dimensions the string
// spelling produces.
func stepsMatch(node *ir.Node) (Match, error) {
	if tagName(node) != VersePathTag {
		return nil, fmt.Errorf("match: an address written as a list needs the !%s tag "+
			"(`!%s [demo pr 1]`), so a list of steps cannot be read as anything else",
			VersePathTag, VersePathTag)
	}
	// The NODES, not their text: a step may carry an operator (`!glob "hot-*"`), and the tag is
	// what says so. ParseStepNodes keeps it.
	if hasTaggedStep(node) {
		return ParseStepNodes(node.Values)
	}
	steps := make([]string, 0, len(node.Values))
	for _, v := range node.Values {
		// ANY SCALAR, not just a string: an id is a name whatever it looks like, and `1` is
		// the id of half the entities anybody writes down. Refusing a number here would make
		// the vector spelling the one form that needs quoting, which is the opposite of what
		// it is for.
		step, ok := stepText(unwrap(v))
		if !ok {
			return nil, fmt.Errorf("match: a step of !%s is a name; got %v", VersePathTag, v.Type)
		}
		steps = append(steps, step)
	}
	return ParseSteps(steps)
}

// atMatch reads the `at:` dimension: an address prefix as a kinded path (`demo.pr`) or as the
// tagged vector (`!verse-path [demo pr]`), into the dimensions it names.
func atMatch(val *ir.Node) (Match, error) {
	switch {
	case val == nil:
		return nil, fmt.Errorf("match: `at` is empty")
	case val.Type == ir.ArrayType:
		return stepsMatch(val)
	case val.Type == ir.StringType:
		return ParseSteps(strings.Split(val.String, "."))
	}
	return nil, fmt.Errorf("match: `at` is an address as a path (`demo.pr`) or as steps "+
		"(`!%s [demo pr]`); got %v", VersePathTag, val.Type)
}

// hasTaggedStep reports whether any step of an address carries an operator.
func hasTaggedStep(node *ir.Node) bool {
	for _, v := range node.Values {
		if u := unwrap(v); u != nil && u.Tag != "" {
			return true
		}
	}
	return false
}

// stepText is a step's text, whatever scalar it was written as. It is scalarText's question
// asked of an ADDRESS rather than of an interpolation, and it answers no for a composite for
// the same reason: a step is a name, and a list is not one.
func stepText(n *ir.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	return scalarText(n)
}

// valType names a node's type for a message, and says "nothing" for an absent one.
func valType(n *ir.Node) string {
	if n == nil {
		return "nothing"
	}
	return n.Type.String()
}
