package entity

import (
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// Payload is what an entity carries: ANY tony value, not only an object.
//
// It used to be `map[string]any`, which forced every entity to be a little record. Nothing
// ever required that. An entity whose payload is `42`, `true`, `"open"` or `[a, b]` is
// perfectly meaningful — a counter, a flag, a state name, a list — and was simply
// inexpressible. A node is the tony type for "any value", so that constraint is gone.
//
// It is an ALIAS rather than a defined type on purpose: tony-codegen resolves `*ir.Node` to
// the `tony-base:ir` schema, and a defined type over it would resolve to something else (a
// named type is the shape item 8 of tony-format#f69agjyeh12ksa25cnn0 is about). Since an
// alias cannot carry methods, the accessors below are functions.
//
// A schemagen'd struct field may spell it `Payload` as of go-tony v0.0.95, which taught the
// resolver to see through an alias (item 18). Before that those fields had to spell
// `*ir.Node` directly, so the vocabulary was absent from exactly the declarations a reader
// of the schema looks at.
//
// Nothing here is an *object* predicate. Field-shaped features — a trigger's `fields`
// match, `$var` bindings — read fields through Get/Str, which answer empty for a payload
// that has no fields, so a scalar payload simply does not match rather than erroring.
type Payload = *ir.Node

// Obj builds an object payload, and is the literal-friendly spelling of the common case:
//
//	entity.Obj{"stage": "open"}.Payload()
//
// It exists so the many object payloads across the codebase stay readable instead of
// becoming ir.FromMap(map[string]*ir.Node{...}) by hand.
type Obj map[string]any

// Payload converts the object to a node. Values are converted by gomap, so they may be any
// Go value it can map — scalars, slices, maps, structs with tony tags.
func (o Obj) Payload() Payload {
	if o == nil {
		return nil
	}
	fields := make(map[string]*ir.Node, len(o))
	for k, v := range o {
		fields[k] = Val(v)
	}
	return ir.FromMap(fields)
}

// Val converts any Go value to a payload. An unconvertible value becomes null rather than
// an error: this sits on write paths that have no error to return, and a null payload is
// visible and harmless where a panic would not be.
func Val(v any) Payload {
	switch t := v.(type) {
	case nil:
		return ir.Null()
	case *ir.Node:
		return t
	case string:
		return ir.FromString(t)
	case bool:
		return ir.FromBool(t)
	case int:
		return ir.FromInt(int64(t))
	case int64:
		return ir.FromInt(t)
	case float64:
		return ir.FromFloat(t)
	case Obj:
		return t.Payload()
	case map[string]any:
		return Obj(t).Payload()
	}
	n, err := gomap.ToTonyIR(v)
	if err != nil {
		return ir.Null()
	}
	return n
}

// Get returns the named field of an object payload, or nil — for a scalar, a list, or an
// absent field alike. Callers distinguish "no such field" from "field is null" by checking
// the node, not by a second return: a payload that cannot have fields and one that lacks
// this one are the same answer to the same question.
func Get(p Payload, field string) Payload {
	if p == nil || p.Type != ir.ObjectType {
		return nil
	}
	return ir.Get(p, field)
}

// Str reads a string field, empty when absent or not a string. It replaces the
// `payload["flow"].(string)` idiom, which had the same tolerance.
func Str(p Payload, field string) string {
	f := Get(p, field)
	if f == nil || f.Type != ir.StringType {
		return ""
	}
	return f.String
}

// Strs reads a list-valued field as strings, nil when absent or not a list; non-string
// elements are skipped.
//
// It exists because a payload no longer remembers Go types. The map-backed payload held
// whatever the writer put in it, so a `[]string` written came back a `[]string` — but only
// in memory: the docd-backed store always round-tripped through the document and returned a
// list of anys. Now both stores agree, and reading a list means asking for the element type
// you want rather than type-asserting the slice the writer happened to use.
func Strs(p Payload, field string) []string {
	f := Get(p, field)
	if f == nil || f.Type != ir.ArrayType {
		return nil
	}
	out := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		if v != nil && v.Type == ir.StringType {
			out = append(out, v.String)
		}
	}
	return out
}

// Fields returns an object payload's fields, nil for anything else — the iteration face,
// replacing `for k, v := range payload`.
func Fields(p Payload) map[string]*ir.Node {
	if p == nil || p.Type != ir.ObjectType {
		return nil
	}
	return ir.ToMap(p)
}

// IsObj reports whether the payload is an object, i.e. whether field-shaped questions can
// mean anything about it.
func IsObj(p Payload) bool { return p != nil && p.Type == ir.ObjectType }

// ToObj reads a whole object payload back as Go values — the inverse of Obj.Payload, and
// nil for anything that is not an object. It is for code that builds a payload
// incrementally (reconcilers comparing desired against current, mirrors accumulating
// fields): those keep working in maps and convert once, at the store boundary, rather than
// mutating a node in place.
func ToObj(p Payload) Obj {
	if !IsObj(p) {
		return nil
	}
	var m map[string]any
	if err := gomap.FromTonyIR(p, &m); err != nil {
		return nil
	}
	return Obj(m)
}

// clone deep-copies a payload. ir.Node carries a Parent pointer, so a node embedded in one
// document must not be shared into another; the store clones on write for the same reason
// the map-backed payload did.
func clone(p Payload) Payload {
	if p == nil {
		return nil
	}
	return p.DeepCopy()
}

// Equal reports whether two payloads are structurally the same — used where the old map
// payload was compared with reflect.DeepEqual.
func Equal(a, b Payload) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.DeepEqual(b)
}
