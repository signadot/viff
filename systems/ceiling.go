package systems

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// A CEILING IS A RUNNER'S BOUND ON ONE SYSTEM, in the environment's form: blocks of attributes
// and verbs, each verb with its shapes. It is positive and closed: what no block gives is beyond
// it, and there is no "all but" (a block gives what it lists). An environment's block is within
// it when one ceiling block holds it whole (Contains), and a step is within it when a block
// whose attributes it matches gives a shape it matches (Refuses).
//
// It differs from an environment in its attributes: each is a list of patterns a value matches,
// not the value (`repo: "github.com/signadot/**"`, `mode: [none, allow]`; one pattern may be
// written bare), and so a step may fall within more than one block. An attribute a ceiling block
// names bounds every environment block it holds: one that leaves the attribute unsaid stands for
// its document's default where there is one (the network's mode, none), and is beyond the block
// where there is none (git's `from`: a block bounding `from` holds only blocks that say it).
//
//	ceiling:
//	- repo: "github.com/signadot/**"
//	  fetch: {}
//	  push: [{ref: "refs/heads/verse/**"}, {ref: "refs/heads/release/*", force: "false"}]
type Ceiling []CeilingBlock

// Open is the ceiling that bounds nothing of a system: the empty block, every verb in any shape,
// for any value of its attributes. It is what a runner whose operator is its task's author writes
// (`verse up`'s), said rather than implied: a ceiling written nowhere is closed. It is for the
// systems a runner stops inside a run; for one with a unique attribute it would be any
// repository on any forge, which a `git` ceiling may not be (verserunner's forge check). It is
// not for the network either: the network is apart from the empty block (Block.Given), so Open
// allows it no connection, where an unwritten network ceiling bounds nothing.
func Open() Ceiling { return Ceiling{{}} }

// CeilingBlock is one block of a ceiling: the attribute values it is for, as patterns, and the
// verbs it gives with their shapes.
type CeilingBlock struct {
	Attributes map[string][]string
	Verbs      map[string][]Shape
}

// Check is what a ceiling for system has to satisfy: its attributes and verbs the document's,
// every pattern one its field takes, no shape bound at a firing, and no empty list.
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
			if !ok {
				if _, isVerb := d.Verb(name); isVerb {
					if len(b.Attributes[name]) == 0 {
						return at(fmt.Errorf("%s: an empty list allows nothing; leave the verb out instead", name))
					}
					return at(fmt.Errorf("%s: a verb takes a shape or a list of shapes (`%s: {}` for any)", name, name))
				}
				return at(fmt.Errorf("%s: %s has no attribute or verb %q (attributes: %s; verbs: %s)", name, system, name,
					strings.Join(attributeNames(d), ", "), strings.Join(verbNames(d), ", ")))
			}
			if len(b.Attributes[name]) == 0 {
				return at(fmt.Errorf("%s: an empty list allows nothing; leave the attribute out for any", name))
			}
			for _, p := range b.Attributes[name] {
				if strings.Contains(p, "$") {
					return at(fmt.Errorf("%s: %q: a ceiling is the runner's and binds nothing", name, p))
				}
				if err := a.Field().Accepts(Canonical(a.Form, p)); err != nil {
					return at(fmt.Errorf("%s: %w", name, err))
				}
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
// The block is bound, a launch's: a `$` in a value is part of it, as it is where the value is
// used, since a firing binds payload text as written. A charter's spawn, still to be bound, is
// held by ContainsAsWritten.
func (c Ceiling) Contains(system string, b Block) error { return c.contains(system, b, false) }

// ContainsAsWritten is Contains for a block as a charter writes it, at install: an attribute
// still to be bound at a firing (`$…`) could be many values, so each binding is read as `**`, and
// it is within where some value of it could be (Intersect). A charter binding its repository
// installs under a ceiling of an owner's, and one naming another owner before the `$` does not.
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
				return fmt.Sprintf("its %s is unsaid, and the ceiling's is %s", name, strings.Join(cb.Attributes[name], ", ")), true
			}
		}
		if !cb.admits(d, name, v, written) {
			return fmt.Sprintf("%s %s is beyond %s", name, v, strings.Join(cb.Attributes[name], ", ")), true
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

// admits is whether an attribute's value lies within the block's patterns for it: matched as a
// value, or, as written and still to be bound, met by one (ContainsAsWritten).
func (cb CeilingBlock) admits(d Document, name, v string, written bool) bool {
	a, _ := d.Attribute(name)
	v = Canonical(a.Form, v)
	for _, p := range cb.Attributes[name] {
		p = Canonical(a.Form, p)
		if written && strings.Contains(v, "$") {
			if Intersect(p, bound.ReplaceAllString(v, "**"), false) {
				return true
			}
		} else if Match(p, v, false) {
			return true
		}
	}
	return false
}

// bound is a binding a firing replaces, in an attribute as a charter writes it.
var bound = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_.]*`)

// ShapeWithin is whether every call inner allows, outer allows too: each field outer names, inner
// names, with a pattern within outer's (PatternContains). A field outer leaves out is any; one
// inner leaves out is any too, and so beyond an outer that names it.
func ShapeWithin(step string, outer, inner Shape) bool {
	for name, op := range outer {
		ip, named := inner[name]
		if !named || !PatternContains(op, ip, Crossing(step, name)) {
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
func Beyond(ceilings map[string]Ceiling, step string, fields map[string]string) (why string, ok bool) {
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
func (c Ceiling) Refuses(step string, fields map[string]string) (why string, ok bool) {
	system, verb, valid := Split(step)
	if !valid {
		return fmt.Sprintf("%s is no step of a system", step), true
	}
	d, _ := Get(system)
	var near []string
	for _, cb := range c {
		inside := true
		for name, patterns := range cb.Attributes {
			a, _ := d.Attribute(name)
			if !slices.ContainsFunc(patterns, func(p string) bool {
				return Match(Canonical(a.Form, p), Canonical(a.Form, fields[name]), false)
			}) {
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
		return fmt.Sprintf("the runner's ceiling gives no `%s` for it (%s)", verb, stepWords(d, fields)), true
	}
	return fmt.Sprintf("no shape of `%s` the runner's ceiling gives matches (%s): it gives %s",
		verb, stepWords(d, fields), strings.Join(near, "; ")), true
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
// key an attribute (a value, or a list of values) or a verb (a shape, or a list of shapes), told
// apart by what the key holds; whether each is its system's is Check's to say.
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
	b := CeilingBlock{Attributes: map[string][]string{}, Verbs: map[string][]Shape{}}
	for i, key := range node.Fields {
		key = ir.Uncomment(key)
		if key == nil || key.Type != ir.StringType {
			return CeilingBlock{}, errors.New("a key is an attribute's or a verb's name")
		}
		name := key.String
		val := ir.Uncomment(node.Values[i])
		var items []*ir.Node
		if val != nil && val.Type == ir.ArrayType {
			items = val.Values
		} else {
			items = []*ir.Node{val}
		}
		shapes, values := []Shape{}, []string{}
		for j, item := range items {
			item = ir.Uncomment(item)
			if item != nil && item.Type == ir.ObjectType {
				s, err := readShape(item)
				if err != nil {
					return CeilingBlock{}, fmt.Errorf("%s[%d]%w", name, j, err)
				}
				shapes = append(shapes, s)
				continue
			}
			v, err := scalar(item)
			if err != nil {
				return CeilingBlock{}, fmt.Errorf("%s: %w", name, err)
			}
			values = append(values, v)
		}
		switch {
		case len(shapes) > 0 && len(values) > 0:
			return CeilingBlock{}, fmt.Errorf("%s: a verb's shapes or an attribute's values, not both", name)
		case len(shapes) > 0:
			b.Verbs[name] = shapes
		default:
			b.Attributes[name] = values
		}
	}
	return b, nil
}

// Node writes the ceiling as a config writes it: one block bare, an attribute of one pattern
// bare.
func (c Ceiling) Node() *ir.Node {
	items := make([]*ir.Node, len(c))
	for i, b := range c {
		var kvs []ir.KeyVal
		for _, name := range slices.Sorted(maps.Keys(b.Attributes)) {
			ps := b.Attributes[name]
			val := ir.FromString(ps[0])
			if len(ps) != 1 {
				vs := make([]*ir.Node, len(ps))
				for j, p := range ps {
					vs[j] = ir.FromString(p)
				}
				val = ir.FromSlice(vs)
			}
			kvs = append(kvs, ir.KeyVal{Key: ir.FromString(name), Val: val})
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
