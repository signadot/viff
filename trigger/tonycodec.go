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

	"github.com/signadot/verse/entity"
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

// address renders the match as the kinded path "system.kind.id" when it carries only those
// dimensions, with "*" for a gap. Reported false when a payload predicate or an op is present
// (no string can carry those), or when the match is empty.
//
// A COLON IS ORDINARY in any segment: the grammar reads dots, and a segment that would otherwise
// be mis-read is quoted rather than refused. So a match carrying a colon outside the id has a
// string form like any other, where a colon-delimited rendering would be ambiguous to ParseExpr
// and have none.
//
// A PATH IS NOT RENDERED AS ONE OF THESE, whatever its length. System/Kind/ID read a path's
// segments now (Match.seg), so the two spellings answer alike — but they do not MEAN alike, and
// the difference is what a stratum rests on: `{path: [verse-dev, drift, head]}` is a PLACE, and
// the dimension spelling's third position is an ID, which a stratum may not constrain. A path
// that came back as `{system, kind, id}` was refused on reinstall by the very check it passed
// going in.
func (m Match) address() (string, bool) {
	if m.Deep() {
		return "", false
	}
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
	segs := []string{sys, kind, id}
	last := 0
	for i, s := range segs {
		if s != "" {
			last = i
		}
	}
	out := make([]string, 0, last+1)
	for _, s := range segs[:last+1] {
		if s == "" {
			out = append(out, "*") // a gap is any segment, and a `*` is never quoted
			continue
		}
		out = append(out, entity.JoinSegs([]string{s}))
	}
	return strings.Join(out, "."), true
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

// ToTonyIR emits the match UNWRAPPED, so a `when` reads back the way it was written.
func (f Formula) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) {
	if f.Req == nil {
		return ir.Null(), nil
	}
	return f.Req.Facts.ToTonyIR(opts...)
}

// FromTonyIR reads a formula: a match, in any spelling a match takes — an address string, a
// steps list, an object of dimensions, a tagged operator such as `!and [...]` — and the
// explicit `!match` tag. The temporal grammar that used to stand here (`!until [...]`,
// `!quorum 2`, and the JSON spelling `{"quorum": 2}`) is refused by name: a gate's threshold
// and deadline are `need:` and `deadline:` on the gate, and waiting for a fact is a
// condition's own match.
func (f *Formula) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = unwrap(node)
	if node == nil || node.Type == ir.NullType {
		*f = Formula{}
		return nil
	}
	if tag := tagName(node); retiredFormulaTags[tag] {
		return retiredFormula("!" + tag)
	}
	// The JSON spelling of the same: a single-key object whose key is the tag. A match
	// dimension is never one of these (TestMatchKeysDisjointFromFormulaTags), so a
	// single-key match object is left to the match.
	if node.Type == ir.ObjectType && len(node.Fields) == 1 && retiredFormulaTags[node.Fields[0].String] && !matchKeys[node.Fields[0].String] {
		return retiredFormula(node.Fields[0].String)
	}
	var m Match
	if err := m.FromTonyIR(node); err != nil {
		return err
	}
	*f = *matchFormula(m)
	return nil
}

// retiredFormula is the refusal for the temporal grammar, naming what replaced it.
func retiredFormula(what string) error {
	return fmt.Errorf("%s is no longer a formula: nothing composes a condition over time. A "+
		"gate's threshold is `need: N` and its clock is `deadline: {within: \"24h\", default: "+
		"deny}` on the gate; waiting for a fact is the condition's own match", what)
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
