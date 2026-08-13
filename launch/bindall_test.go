package launch

import (
	"reflect"
	"testing"
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
		"Doer":       "who the selection picked, which is an answer and not a request",
		"Occurrence": "the commit that caused this firing, which is a fact about the cause and not a request",
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
