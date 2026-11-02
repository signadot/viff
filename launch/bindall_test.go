package launch

import (
	"reflect"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// Bind carries EVERY field of a Spec.
//
// It is a field-by-field copy, which means a field added to Spec and not added to Bind is
// dropped on every charter spawn while every direct construction of a Spec keeps working —
// so the omission shows up as a charter setting being quietly ignored, with nothing failing.
// That is exactly how `retain` was lost: decoded correctly, carried correctly across the
// runner seam, and zeroed in between.
//
// This fills a Spec by reflection, so the guard covers fields nobody has written yet.
func TestBindCarriesEveryField(t *testing.T) {
	var spec Spec
	v := reflect.ValueOf(&spec).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Int, reflect.Int64:
			f.SetInt(7)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Struct:
			// The environment: filled to its leaves, which carried below checks one level down.
			fill(t, f, v.Type().Field(i).Name)
		default:
			t.Fatalf("Spec.%s is a %s and this test does not know how to fill one; "+
				"teach it rather than skipping, or the guard has a hole",
				v.Type().Field(i).Name, f.Kind())
		}
	}

	// Fields a bound spec must NOT carry, with the reason. The engine assigns these AFTER
	// binding — they are answers rather than requests — so a charter that wrote one is
	// overwritten, and carrying it here would let it survive to be overwritten later, which
	// is a longer way to the same place with a window in the middle.
	engineOwned := map[string]string{
		"Chain":      "the kpath of the spawning action, which only the engine knows",
		"Occurrence": "the commit that caused this firing, which is a fact about the cause and not a request",
		"ActsFor":    "who `as: subject` resolved to at the firing; a charter naming it would act as anybody",
		"Passed":     "the gates the firing passed, which a credential vouches for; a charter naming them would vouch for an answer nobody gave",
		// A comment describes the spawn AS WRITTEN, so it belongs to the charter and not to the
		// run: the persisted rule is the un-bound spec, which keeps them, and binding one into a
		// running task would carry an author's note somewhere nobody reads it. Substituting
		// `$subject` inside a comment would be worse still.
		"Doc":  "a comment on the rule, which describes what was written rather than what runs",
		"Note": "a comment on the rule, which describes what was written rather than what runs",
	}

	out := reflect.ValueOf(spec.Bind(Binding{}))
	for i := range out.NumField() {
		name := out.Type().Field(i).Name
		zero := out.Field(i).IsZero()
		if why, owned := engineOwned[name]; owned {
			if !zero {
				t.Errorf("Bind carried Spec.%s, which is engine-owned (%s)", name, why)
			}
			continue
		}
		if zero {
			t.Errorf("Bind dropped Spec.%s — add it to Bind, or add it to engineOwned with "+
				"the reason a spawn must not carry it", name)
		}
	}
}

// Bind carries EVERY field of the agent and of the environment to the leaf: a field added and
// not copied is a fact of the charter that every firing quietly drops, as the network was.
func TestBindCarriesEveryAgentAndEnvironmentField(t *testing.T) {
	var spec Spec
	fill(t, reflect.ValueOf(&spec.Agent).Elem(), "Agent")
	fill(t, reflect.ValueOf(&spec.Environment).Elem(), "Environment")
	got := spec.Bind(Binding{})
	carried(t, reflect.ValueOf(got.Agent), "Agent")
	carried(t, reflect.ValueOf(got.Environment), "Environment")
}

// nodeType is a pattern's, which fill and carried take as a leaf.
var nodeType = reflect.TypeOf((*ir.Node)(nil))

// fill sets every leaf of v to something other than its zero value.
func fill(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Type() == nodeType { // a pattern is a leaf: its parent links lead back up the tree
		v.Set(reflect.ValueOf(ir.FromString("x")))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Struct:
		for i := range v.NumField() {
			fill(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(t, v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(t, v.Index(0), path+"[0]")
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(t, k, path+"<key>")
		fill(t, e, path+"[k]")
		v.SetMapIndex(k, e)
	default:
		t.Fatalf("%s is a %s and this test does not know how to fill one; teach it rather than "+
			"skipping, or the guard has a hole", path, v.Kind())
	}
}

// carried fails for each leaf of v that is zero: a field fill set and Bind did not copy.
func carried(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Type() == nodeType {
		if v.IsNil() {
			t.Errorf("Bind dropped %s", path)
		}
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			carried(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Pointer:
		if v.IsNil() {
			t.Errorf("Bind dropped %s", path)
			return
		}
		carried(t, v.Elem(), path)
	case reflect.Slice:
		if v.Len() == 0 {
			t.Errorf("Bind dropped %s", path)
			return
		}
		carried(t, v.Index(0), path+"[0]")
	case reflect.Map:
		if v.Len() == 0 {
			t.Errorf("Bind dropped %s", path)
			return
		}
		for _, k := range v.MapKeys() {
			carried(t, v.MapIndex(k), path+"[k]")
		}
	default:
		if v.IsZero() {
			t.Errorf("Bind dropped %s", path)
		}
	}
}
