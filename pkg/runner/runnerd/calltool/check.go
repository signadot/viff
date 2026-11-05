package calltool

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/signadot/verse/pkg/codec"
)

// Find is the tool named name, if the list has it.
func Find(tools []Tool, name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Names is the tools' names, sorted, for a message that lists them.
func Names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// Check is whether input is a call of t that the schema admits: every required parameter is
// present and not empty, no parameter is one the tool has not got, each value is of its
// parameter's type, and a value with an `enum` is one of it. A null is an absent parameter.
// The error names what is wrong in the parameter's own name, for either end to show: the
// binary checks before the call crosses, and the server checks what arrives, so a caller that
// is not the binary is told the same thing.
//
// It is a check of the SCHEMA, not of the call's sense: whether a branch is permitted or a
// question answerable is the tool's to say when it runs. t is as Describe reports it, with
// its params normalised; a schema read any other way is not what this checks.
func Check(t Tool, input map[string]any) error {
	var missing []string
	for _, name := range t.Required {
		if empty(input[name]) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s requires %s", t.Name, strings.Join(missing, ", "))
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw, ok := t.Params[k]
		if !ok {
			if len(t.Params) == 0 {
				return fmt.Errorf("%s takes no parameters", t.Name)
			}
			return fmt.Errorf("%s has no parameter %q (it has: %s)", t.Name, k, strings.Join(paramNames(t), ", "))
		}
		if input[k] == nil {
			continue // null: as good as unsaid, and the tool reads it so
		}
		schema, _ := raw.(map[string]any)
		if err := checkValue(schema, input[k]); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

// empty is a required value that is as good as absent: not there, null, or a blank string.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	}
	return false
}

func paramNames(t Tool) []string {
	out := make([]string, 0, len(t.Params))
	for k := range t.Params {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkValue is whether v is of the type schema names, and in its enum when it has one. A
// schema that names no type takes anything; a number is any numeric kind, since the codecs
// read one as int64 or float64 by its spelling.
func checkValue(schema map[string]any, v any) error {
	typ, _ := schema["type"].(string)
	switch typ {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("want a string, not %s", kind(v))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want a boolean, not %s", kind(v))
		}
	case "integer":
		if !isInteger(v) {
			return fmt.Errorf("want an integer, not %s", kind(v))
		}
	case "number":
		if !isNumber(v) {
			return fmt.Errorf("want a number, not %s", kind(v))
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("want a list, not %s", kind(v))
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i, it := range items {
			if err := checkValue(itemSchema, it); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
		return nil
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("want a document, not %s", kind(v))
		}
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		for _, e := range enum {
			if same(e, v) {
				return nil
			}
		}
		names := make([]string, 0, len(enum))
		for _, e := range enum {
			names = append(names, fmt.Sprint(e))
		}
		return fmt.Errorf("%q is not one of %s", fmt.Sprint(v), strings.Join(names, ", "))
	}
	return nil
}

// same is whether an enum's member and a value are the one thing: equal as values, or the same
// number in two numeric spellings (an int64 from one codec, a float64 from another), at any
// depth of a list or a document. A string is never the number it spells.
func same(e, v any) bool {
	switch x := e.(type) {
	case map[string]any:
		y, ok := v.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !same(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := v.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !same(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	if isNumber(e) && isNumber(v) {
		return fmt.Sprint(e) == fmt.Sprint(v)
	}
	// Scalars, and anything else: DeepEqual rather than ==, which panics on two values of one
	// uncomparable type.
	return reflect.DeepEqual(e, v)
}

// isInteger is whether v is a whole number, whatever numeric type the decoder chose for it.
func isInteger(v any) bool {
	_, ok := codec.AsInt64(v)
	return ok
}

func isNumber(v any) bool {
	switch v.(type) {
	case float32, float64:
		return true
	}
	return isInteger(v)
}

// kind is a value's type in the words a message uses.
func kind(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "a document"
	}
	if isNumber(v) {
		return "a number"
	}
	return fmt.Sprintf("%T", v)
}
