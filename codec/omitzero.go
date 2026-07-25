package codec

import (
	"reflect"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"
)

// omitzero support, implemented once over gomap's reflection path.
//
// `tony:"field=x,omitzero"` is honored ONLY by tony-codegen; the reflection path reads
// `field=` and ignores everything else. Without it every optional field of every document
// serializes explicitly — `task: null`, `driver: null`, `cursor: 0` — which defeats much
// of the point of moving to a format people are meant to read.
//
// The obvious answer is to run tony-codegen. verse cannot: a single `map[string]any` or
// `[]NamedString` field takes a whole package out of codegen, and there is no way to tell
// the generator "this type has its own codec, just call it", so one unsupported field
// propagates to every type that references it. Details and repros are in
// tony-format#f69agjyeh12ksa25cnn0.
//
// So: gomap produces the node, and this prunes it. About a hundred lines that work for
// every type, against roughly seven hundred lines of hand-written ToTonyIR/FromTonyIR
// boilerplate that would otherwise be needed — and the tags stay exactly the ones
// tony-codegen reads, so if those upstream gaps close, generated codecs can take over
// per package with no change to any type declaration.
//
// Only the encode direction needs this. Decoding already tolerates an absent field: it
// leaves the Go zero value, which is what the field was omitted for.

// pruneZero removes the keys of omitzero fields whose values are empty, walking the Go
// value and the node it produced in parallel. n is modified in place.
func pruneZero(val reflect.Value, n *ir.Node) {
	if n == nil {
		return
	}
	for val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface {
		if val.IsNil() {
			return
		}
		val = val.Elem()
	}

	switch val.Kind() {
	case reflect.Struct:
		pruneStruct(val, n)
	case reflect.Slice, reflect.Array:
		if n.Type != ir.ArrayType {
			return
		}
		for i := 0; i < val.Len() && i < len(n.Values); i++ {
			pruneZero(val.Index(i), n.Values[i])
		}
	case reflect.Map:
		if n.Type != ir.ObjectType {
			return
		}
		for _, k := range val.MapKeys() {
			// Only string-keyed maps line up with object field names; anything else
			// (gomap's int-keyed sparse arrays) is left alone.
			if k.Kind() != reflect.String {
				continue
			}
			if child := fieldValue(n, k.String()); child != nil {
				pruneZero(val.MapIndex(k), child)
			}
		}
	}
}

func pruneStruct(val reflect.Value, n *ir.Node) {
	if n.Type != ir.ObjectType {
		return
	}
	// A type with its own ToTonyIR produced this node itself, so its shape need not
	// correspond to the struct's fields at all — pruning it by field name could delete
	// something that merely shares a name. Its codec owns its own omission.
	if hasOwnCodec(val.Type()) {
		return
	}

	typ := val.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := val.Field(i)

		// gomap flattens an embedded struct's fields into the parent object, so the
		// embedded value is pruned against the SAME node.
		if field.Anonymous && fv.Kind() == reflect.Struct {
			pruneZero(fv, n)
			continue
		}

		tag, err := gomap.ParseStructTag(field.Tag.Get("tony"))
		if err != nil {
			continue
		}
		name := field.Name
		if renamed, ok := tag["field"]; ok && renamed != "" && renamed != "-" {
			name = renamed
		}
		if _, omitzero := tag["omitzero"]; omitzero && isEmpty(fv) {
			dropField(n, name)
			continue
		}
		if child := fieldValue(n, name); child != nil {
			pruneZero(fv, child)
		}
	}
}

// hasOwnCodec reports whether a type carries a hand-written or generated ToTonyIR.
func hasOwnCodec(t reflect.Type) bool {
	if _, ok := t.MethodByName("ToTonyIR"); ok {
		return true
	}
	_, ok := reflect.PointerTo(t).MethodByName("ToTonyIR")
	return ok
}

// isEmpty reports whether a value should be omitted. It follows tony-codegen's reading of
// omitzero rather than Go's reflect.Value.IsZero: an empty-but-non-nil slice or map is
// omitted too, because generated code guards those with `if len(...) > 0`. Matching the
// generator matters — these tags are its tags, and the two must not disagree about what a
// document looks like.
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array, reflect.String:
		return v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	default:
		return v.IsZero()
	}
}

// fieldValue returns the value node stored under name, or nil.
func fieldValue(n *ir.Node, name string) *ir.Node {
	for i, f := range n.Fields {
		if f != nil && f.String == name && i < len(n.Values) {
			return Unwrap(n.Values[i])
		}
	}
	return nil
}

// dropField removes name from an object node, keeping the parallel Fields/Values slices
// in step.
func dropField(n *ir.Node, name string) {
	for i, f := range n.Fields {
		if f == nil || f.String != name {
			continue
		}
		n.Fields = append(n.Fields[:i], n.Fields[i+1:]...)
		if i < len(n.Values) {
			n.Values = append(n.Values[:i], n.Values[i+1:]...)
		}
		return
	}
}
