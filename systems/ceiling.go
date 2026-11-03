package systems

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// A CEILING IS A RUNNER'S BOUND ON ONE SYSTEM, in the environment's form: blocks of attributes
// and verbs, each verb with its shapes. It is positive and closed: what no block gives is beyond
// it, and there is no "all but" (a block gives what it lists). An environment's block is within
// it when one ceiling block holds it whole (Contains), and a step is within it when a block
// whose attributes it matches gives a shape it matches (Refuses).
//
// It differs from an environment in its attributes: each is a pattern a value matches, not the
// value (`repo: !regexp "^github\\.com/signadot/"`, `mode: !or [none, allow]`), a tony match
// as a shape's fields are, and so a step may fall within more than one block. An attribute a ceiling block
// names bounds every environment block it holds: one that leaves the attribute unsaid stands for
// its document's default where there is one (the network's mode, none), and is beyond the block
// where there is none (git's `from`: a block bounding `from` holds only blocks that say it).
//
//	ceiling:
//	- repo: !glob "github.com/signadot/*"
//	  fetch: {}
//	  push: [{ref: !regexp "^refs/heads/verse/"}, {ref: !glob "refs/heads/release/*", force: false}]
type Ceiling []CeilingBlock

// Open is the ceiling that bounds nothing of a system: the empty block, every verb in any shape,
// for any value of its attributes. It is what a runner whose operator is its task's author writes
// (`verse up`'s), said rather than implied: a ceiling written nowhere is closed. It is for the
// systems a runner stops inside a run; for one with a unique attribute it would be any
// repository on any forge, which a `git` ceiling may not be (the stock runner's forge check). It is
// not for the network either: the network is apart from the empty block (Block.Given), so Open
// allows it no connection, where an unwritten network ceiling bounds nothing.
func Open() Ceiling { return Ceiling{{}} }

// CeilingBlock is one block of a ceiling: the attribute values it is for, each as a pattern, and
// the verbs it gives with their shapes.
type CeilingBlock struct {
	Attributes map[string]*ir.Node
	Verbs      map[string][]Shape
}

// Check is what a ceiling for system has to satisfy: its attributes and verbs the document's,
// every pattern one its field takes (Field.Accepts), nothing bound at a firing, and no empty list.
func (c Ceiling) Check(system string) error {
	d, ok := Get(system)
	if !ok {
		return fmt.Errorf("%s: not a system", system)
	}
	for i, b := range c {
		at := func(err error) error {
			if len(c) > 1 {
				return fmt.Errorf("[%d].%w", i, err)
			}
			return fmt.Errorf(".%w", err)
		}
		for _, name := range slices.Sorted(maps.Keys(b.Attributes)) {
			a, ok := d.Attribute(name)
			p := b.Attributes[name]
			empty := p != nil && p.Tag == "" && p.Type == ir.ArrayType && len(p.Values) == 0
			if !ok {
				if _, isVerb := d.Verb(name); isVerb {
					if empty {
						return at(fmt.Errorf("%s: an empty list allows nothing; leave the verb out instead", name))
					}
					return at(fmt.Errorf("%s: a verb takes a shape or a list of shapes (`%s: {}` for any)", name, name))
				}
				return at(fmt.Errorf("%s: %s has no attribute or verb %q (attributes: %s; verbs: %s)", name, system, name,
					strings.Join(attributeNames(d), ", "), strings.Join(verbNames(d), ", ")))
			}
			if empty {
				return at(fmt.Errorf("%s: an empty list allows nothing; leave the attribute out for any", name))
			}
			if Binds(p) {
				return at(fmt.Errorf("%s: %s: a ceiling is the runner's and binds nothing", name, Words(p)))
			}
			if err := a.Field().Accepts(canon(a.Form, p)); err != nil {
				return at(fmt.Errorf("%s: %w", name, err))
			}
		}
		for _, verb := range slices.Sorted(maps.Keys(b.Verbs)) {
			v, ok := d.Verb(verb)
			if !ok {
				if _, isAttr := d.Attribute(verb); isAttr {
					return at(fmt.Errorf("%s: an attribute takes a value or a list of values, not a shape", verb))
				}
				return at(fmt.Errorf("%s: %s has no attribute or verb %q (attributes: %s; verbs: %s)", verb, system, verb,
					strings.Join(attributeNames(d), ", "), strings.Join(verbNames(d), ", ")))
			}
			if len(b.Verbs[verb]) == 0 {
				return at(fmt.Errorf("%s: an empty list allows nothing; leave the verb out instead", verb))
			}
			for j, s := range b.Verbs[verb] {
				if err := s.check(v); err != nil {
					return at(fmt.Errorf("%s[%d]%w", verb, j, err))
				}
			}
		}
	}
	return nil
}

// Contains is why an environment's block of system lies beyond the ceiling, or nil: some one
// ceiling block holds it whole. Its attributes are within the ceiling block's patterns (an
// attribute the block leaves out is any; one it names that the environment's block does not give
// is beyond, but for its document's default, which is what the block means), and each verb it
// gives is the ceiling block's, each shape within one of its shapes (ShapeWithin).
//
// The block is bound, a launch's: a `$[` in a value is part of it, as it is where the value is
// used, since what a firing substitutes is not expanded again. A charter's spawn, still to be bound, is
// held by ContainsAsWritten.
func (c Ceiling) Contains(system string, b Block) error { return c.contains(system, b, false) }

// ContainsAsWritten is Contains for a block as a charter writes it, at install: an attribute
// still to be bound at a firing (`$[…]`) could be many values, so each binding is read as any run
// (meetsWritten), and it is within where some value of it could be. A charter binding its repository
// installs under a ceiling of an owner's, and one naming another owner before the expression does not.
// The launch, bound, is held to the ceiling again, by Contains.
func (c Ceiling) ContainsAsWritten(system string, b Block) error { return c.contains(system, b, true) }

func (c Ceiling) contains(system string, b Block, written bool) error {
	d, _ := Get(system)
	// A refusal names the blocks the environment's is for, its attributes within theirs, where
	// any is: why the nearest did not hold it, not why every other is about something else.
	var near, far []string
	for i, cb := range c {
		why, attr := cb.contains(d, b, written)
		if why == "" {
			return nil
		}
		if len(c) > 1 {
			why = fmt.Sprintf("[%d]: %s", i, why)
		}
		if attr {
			far = append(far, why)
		} else {
			near = append(near, why)
		}
	}
	switch {
	case len(near) > 0:
		return errors.New(strings.Join(near, "; "))
	case len(far) > 0:
		return errors.New(strings.Join(far, "; "))
	}
	return errors.New("the ceiling gives nothing of it")
}

// contains is why cb does not hold b, or ""; attr is whether it is b's attributes cb is not for.
func (cb CeilingBlock) contains(d Document, b Block, written bool) (why string, attr bool) {
	for _, name := range slices.Sorted(maps.Keys(cb.Attributes)) {
		v, given := b.Attributes[name]
		if !given {
			if a, ok := d.Attribute(name); ok && a.Default != "" {
				v = a.Default
			} else {
				return fmt.Sprintf("its %s is unsaid, and the ceiling's is %s", name, Words(cb.Attributes[name])), true
			}
		}
		if !cb.admits(d, name, v, written) {
			return fmt.Sprintf("%s %s is beyond %s", name, v, Words(cb.Attributes[name])), true
		}
	}
	allows, gives := given(d.Name, cb.Verbs), b.Given(d.Name)
	for _, verb := range slices.Sorted(maps.Keys(gives)) {
		shapes, ok := allows[verb]
		if !ok {
			return fmt.Sprintf("it gives no `%s` (it gives: %s)", verb, verbList(slices.Sorted(maps.Keys(allows)))), false
		}
		step := StepName(d.Name, verb)
		for _, s := range gives[verb] {
			if !slices.ContainsFunc(shapes, func(outer Shape) bool { return ShapeWithin(step, outer, s) }) {
				return fmt.Sprintf("%s %s is within no shape of its: %s", verb, s, shapeList(shapes)), false
			}
		}
	}
	return "", false
}

// admits is whether an attribute's value lies within the block's pattern for it: matched as a
// value, or, as written and still to be bound, met by one (ContainsAsWritten).
func (cb CeilingBlock) admits(d Document, name, v string, written bool) bool {
	a, _ := d.Attribute(name)
	v = Canonical(a.Form, v)
	if written && Templated(v) {
		return meetsWritten(a.Form, cb.Attributes[name], v)
	}
	return MatchField(a.Form, cb.Attributes[name], a.Field().Value(v))
}

// Templated is whether v holds an expression a firing replaces: a `$[…]`, or a v that is one
// `.[…]` and nothing else (Whole). A `$` that opens none is text, as it is where the value is
// used: `$HOME`, `$5`; and so is a `.[` in a value it is not the whole of.
func Templated(v string) bool { return strings.Contains(v, "$[") || Whole(v) }

// Whole is whether v is one `.[…]` and nothing else, which a firing replaces whole: the test the
// expansion makes (eval.GetRaw).
func Whole(v string) bool { return tonyeval.GetRaw(v) != "" }

// bound is an expression a firing replaces, in an attribute as a charter writes it: `$[…]`, an
// index's brackets inside it included.
var bound = regexp.MustCompile(`\$\[(?:[^\[\]]|\[[^\[\]]*\])*\]`)

// ShapeWithin is whether every call inner allows, outer allows too: each field outer names, inner
// names, with a pattern within outer's (FieldContains). A field outer leaves out is any; one
// inner leaves out is any too, and so beyond an outer that names it.
func ShapeWithin(step string, outer, inner Shape) bool {
	for name, op := range outer {
		ip, named := inner[name]
		if !named || !FieldContains(FormOf(step, name), op, ip) {
			return false
		}
	}
	return true
}

// Gives is whether some block of the ceiling gives a step's verb, the empty block among them:
// whether any call of it could lie within. A step of another system is none of its.
func (c Ceiling) Gives(step string) bool {
	system, verb, ok := Split(step)
	if !ok {
		return false
	}
	for _, b := range c {
		if _, gives := given(system, b.Verbs)[verb]; gives {
			return true
		}
	}
	return false
}

// Beyond is why a step lies beyond a runner's ceilings, by system, or ok false: beyond its
// system's (Ceiling.Refuses), or of a system they name no ceiling for, which is closed, as a
// ceiling written nowhere is. Ceilings that are nil name none, and close every system.
func Beyond(ceilings map[string]Ceiling, step string, fields *ir.Node) (why string, ok bool) {
	system, _, valid := Split(step)
	if !valid {
		return fmt.Sprintf("%s is no step of a system", step), true
	}
	c, has := ceilings[system]
	if !has {
		return fmt.Sprintf("the runner states no ceiling for %s, so none of it is any task's", system), true
	}
	return c.Refuses(step, fields)
}

// Refuses is why a step lies beyond the ceiling, or ok false: no block whose attributes its
// fields match gives a shape matching it. Said as Environment.Refuses says it, in what the ceiling
// gives, so the line it crossed can be read.
func (c Ceiling) Refuses(step string, fields *ir.Node) (why string, ok bool) {
	system, verb, valid := Split(step)
	if !valid {
		return fmt.Sprintf("%s is no step of a system", step), true
	}
	d, _ := Get(system)
	var near []string
	for _, cb := range c {
		inside := true
		for name, pattern := range cb.Attributes {
			a, _ := d.Attribute(name)
			if !MatchField(a.Form, pattern, stepValue(fields, name)) {
				inside = false
			}
		}
		if !inside {
			continue
		}
		shapes := given(system, cb.Verbs)[verb]
		for _, s := range shapes {
			if s.Matches(step, fields) {
				return "", false
			}
		}
		if len(shapes) > 0 {
			near = append(near, shapeList(shapes))
		}
	}
	if len(near) == 0 {
		return fmt.Sprintf("the runner's ceiling gives no `%s` for it (%s)", verb, stepWords(d, verb, fields)), true
	}
	return fmt.Sprintf("no shape of `%s` the runner's ceiling gives matches (%s): it gives %s",
		verb, stepWords(d, verb, fields), strings.Join(near, "; ")), true
}

func shapeList(shapes []Shape) string {
	words := make([]string, len(shapes))
	for i, s := range shapes {
		words[i] = s.String()
	}
	return strings.Join(words, ", ")
}

func verbList(names []string) string {
	if len(names) == 0 {
		return "no verb"
	}
	return strings.Join(names, ", ")
}

// FromTonyIR reads a ceiling as a runner's config writes it: a block, or a list of blocks, each
// key an attribute (a pattern) or a verb (a shape, or a list of shapes), told apart by what the
// key holds; whether each is its system's is Check's to say.
func (c *Ceiling) FromTonyIR(node *ir.Node, opts ...gomap.UnmapOption) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type == ir.NullType {
		*c = nil
		return nil
	}
	var items []*ir.Node
	switch node.Type {
	case ir.ArrayType:
		items = node.Values
	case ir.ObjectType:
		items = []*ir.Node{node}
	default:
		return errors.New("a ceiling is a block, `{…}`, or a list of blocks")
	}
	out := Ceiling{}
	for i, item := range items {
		b, err := readCeilingBlock(item)
		if err != nil {
			if len(items) > 1 {
				return fmt.Errorf("[%d]: %w", i, err)
			}
			return err
		}
		out = append(out, b)
	}
	*c = out
	return nil
}

// ToTonyIR writes the ceiling as a config writes it (Node).
func (c Ceiling) ToTonyIR(opts ...gomap.MapOption) (*ir.Node, error) { return c.Node(), nil }

func readCeilingBlock(node *ir.Node) (CeilingBlock, error) {
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return CeilingBlock{}, errors.New("a ceiling block is an object of attributes and verbs")
	}
	b := CeilingBlock{Attributes: map[string]*ir.Node{}, Verbs: map[string][]Shape{}}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return CeilingBlock{}, errors.New("a key is an attribute's or a verb's name")
		}
		name := key.String
		val := Plain(ir.Uncomment(node.Values[i]))
		switch {
		case val != nil && val.Tag == "" && val.Type == ir.ObjectType:
			s, err := readShape(val)
			if err != nil {
				return CeilingBlock{}, fmt.Errorf("%s%w", name, err)
			}
			b.Verbs[name] = []Shape{s}
		case val != nil && val.Tag == "" && val.Type == ir.ArrayType && len(val.Values) > 0 &&
			!slices.ContainsFunc(val.Values, func(v *ir.Node) bool { v = ir.Uncomment(v); return v == nil || v.Type != ir.ObjectType }):
			shapes := []Shape{}
			for j, item := range val.Values {
				s, err := readShape(ir.Uncomment(item))
				if err != nil {
					return CeilingBlock{}, fmt.Errorf("%s[%d]%w", name, j, err)
				}
				shapes = append(shapes, s)
			}
			b.Verbs[name] = shapes
		case val != nil && val.Tag == "" && val.Type == ir.ArrayType &&
			slices.ContainsFunc(val.Values, func(v *ir.Node) bool { v = ir.Uncomment(v); return v != nil && v.Type == ir.ObjectType }):
			return CeilingBlock{}, fmt.Errorf("%s: a verb's shapes or an attribute's pattern, not both", name)
		default:
			if val == nil {
				val = ir.Null()
			}
			b.Attributes[name] = val
		}
	}
	return b, nil
}

// Node writes the ceiling as a config writes it: one block bare.
func (c Ceiling) Node() *ir.Node {
	items := make([]*ir.Node, len(c))
	for i, b := range c {
		var kvs []ir.KeyVal
		for _, name := range slices.Sorted(maps.Keys(b.Attributes)) {
			v := b.Attributes[name]
			if v == nil {
				v = ir.Null()
			}
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: v.Clone()})
		}
		for _, verb := range slices.Sorted(maps.Keys(b.Verbs)) {
			shapes := b.Verbs[verb]
			val := shapes[0].node()
			if len(shapes) != 1 {
				vs := make([]*ir.Node, len(shapes))
				for j, s := range shapes {
					vs[j] = s.node()
				}
				val = ir.FromSlice(vs)
			}
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(verb), Val: val})
		}
		items[i] = ir.FromKeyVals(kvs)
	}
	if len(items) == 1 {
		return items[0]
	}
	return ir.FromSlice(items)
}
