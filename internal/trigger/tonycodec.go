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

	"go.viff.dev/viff/pkg/entity"
)

// ── Match ────────────────────────────────────────────────────────────────────

// ToTonyIR emits the kinded path when the match is an address and nothing else — every segment a
// name or `*`, no predicate — and the object form `{path, value}` otherwise. `subject: demo.pr.*`
// is how a charter is written by hand, and a round trip gives it back.
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
	var kvs []ir.KeyVal
	if addr, ok := m.pathString(); ok {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(mKeyPath), Val: ir.FromString(addr)})
	}
	// The predicate goes back out as the node it is, operators and all.
	if node, ok := predicateNode(m[mKeyValue]); ok {
		kvs = append(kvs, ir.KeyVal{Key: ir.FromString(mKeyValue), Val: node})
	}
	return ir.FromKeyVals(kvs), nil // {} for the empty match, which matches everything
}

// address renders the match as the kinded path alone, when it is an address and nothing else: a
// path, with no predicate beside it. With one, the path is a key of the object form and this
// answers no.
func (m Match) address() (string, bool) {
	if m.HasPredicate() {
		return "", false
	}
	return m.pathString()
}

// pathString renders the match's path as the kinded path it is — the one spelling an address
// has. A `*` is bare, a `..` is the descent, and a name that would read as either carries the
// quotes that make it a name (`demo.pr."*"`), which is how it was written and how it is read
// back.
func (m Match) pathString() (string, bool) {
	segs := m.SegNodes()
	if len(segs) == 0 {
		return "", false
	}
	steps := make([]string, 0, len(segs))
	for _, n := range segs {
		switch {
		case n.Tag != "":
			// The one match a path segment can hold: the name a quoted `"*"` or `".."` spells,
			// which PatternNodes compiles to `!or [name]` because a bare string of those
			// characters is the query segment.
			lit, ok := quotedName(n)
			if !ok {
				return "", false
			}
			steps = append(steps, `"`+lit+`"`)
		default:
			t, ok := stepText(n)
			if !ok {
				return "", false
			}
			steps = append(steps, t)
		}
	}
	out, err := entity.KPathOf(steps)
	if err != nil {
		return "", false
	}
	return out, true
}

// quotedName reads the one-name match a quoted segment compiles to, and answers false for any
// other operator — nothing else can be written in a path now that it is a kinded path.
func quotedName(n *ir.Node) (string, bool) {
	if tagName(n) != "or" || n.Type != ir.ArrayType || len(n.Values) != 1 {
		return "", false
	}
	v := ir.Uncomment(n.Values[0])
	if v == nil || v.Type != ir.StringType {
		return "", false
	}
	return v.String, true
}

// FromTonyIR accepts the address string or the object form — the same two shapes the JSON
// codec takes, since this is one grammar in two syntaxes.
func (m *Match) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*m = nil
		return nil
	}
	// A `!match` tag is accepted and never emitted: the match atom is written unwrapped, but
	// someone may spell it explicitly.
	if tagName(node) == ViffPathTag {
		return fmt.Errorf("match: !%s is not a spelling of an address — write the kinded path "+
			"(`demo.pr.*`) or `path: \"demo.pr.*\"`", ViffPathTag)
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
		return fmt.Errorf("match: a list is not an address — write the kinded path (`demo.pr.*`) " +
			"or `path: \"demo.pr.*\"`")
	case ir.ObjectType:
		parsed, err := matchFromObject(node)
		if err != nil {
			return err
		}
		*m = parsed
		return m.Validate()
	}
	return fmt.Errorf("match: want an object or a kinded path string, got %v", node.Type)
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
	for i, key := range node.Fields {
		k := key.String
		val := ir.Uncomment(node.Values[i])
		switch k {
		case mKeyPath:
			// A KINDED PATH, which is the one spelling an address has: `demo.pr.*`, the same
			// grammar `viff data -at` takes. A list of segments was the other one, and it
			// existed to carry a match on a segment; a predicate on the payload says what those
			// said, and says it where a reader looks for it.
			if val == nil || val.Type != ir.StringType {
				return nil, fmt.Errorf("match: `%s` is a kinded path (`\"demo.pr.*\"`); got %v",
					mKeyPath, valType(val))
			}
			got, err := ParseExpr(val.String)
			if err != nil {
				return nil, err
			}
			out[mKeyPath] = got[mKeyPath]

		case mKeyValue:
			// Kept as the NODE, not decoded to a map: a payload predicate is a tony match
			// evaluated at the payload root, so any operator it carries has to survive to
			// tony.Match.
			out[k] = val

		default:
			// Unknown or removed: decode it so Validate can name it, rather than dropping it
			// here and reporting a match that constrains less than it says.
			var v any
			if val != nil {
				if err := gomap.FromTonyIR(val, &v); err != nil {
					return nil, fmt.Errorf("match %q: %w", k, err)
				}
			}
			out[k] = v
		}
	}
	return out, nil
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
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*f = Formula{}
		return nil
	}
	if tag := tagName(node); retiredFormulaTags[tag] {
		return retiredFormula("!" + tag)
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
	if v := ir.Uncomment(n); v != nil && v.Comment != nil {
		return v.Comment.Lines
	}
	return nil
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
	node = ir.Uncomment(node)
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
	if o := ir.Uncomment(ir.Get(node, "on")); o != nil && o.Type != ir.NullType {
		var f Formula
		if err := f.FromTonyIR(o, opts...); err != nil {
			return fmt.Errorf("condition.on: %w", err)
		}
		c.On = &f
	}
	if w := ir.Uncomment(ir.Get(node, "when")); w != nil && w.Type != ir.NullType {
		var f Formula
		if err := f.FromTonyIR(w, opts...); err != nil {
			return fmt.Errorf("condition.when: %w", err)
		}
		c.When = &f
	}
	if s := ir.Uncomment(ir.Get(node, "subject")); s != nil && s.Type != ir.NullType {
		if err := c.Subject.FromTonyIR(s, opts...); err != nil {
			return fmt.Errorf("condition.subject: %w", err)
		}
	}
	return nil
}

// ViffPathTag marks an address written as STEPS: `!viff-path [demo pr 1]`.
//
// The tag is required rather than inferred from the array. A bare list where an address belongs
// would be a second thing a bare list can mean — the union form a stratum's `match` takes is
// already one — and an address is exactly the place where guessing wrong is expensive.
const ViffPathTag = "viff-path"

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
