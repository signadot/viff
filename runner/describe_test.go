package runner

import (
	"reflect"
	"testing"

	"github.com/signadot/verse/codec"
)

// A description reads as the Runners doc writes it, and survives the trip back.
func TestDescriptionRoundTrips(t *testing.T) {
	src := `offers:
- agent: verse
  with: {model: any, effort: [low, medium, high]}
  intercepts: [files.write, commands.run]
  annotations: {verse.portkey/virtual-key: string}
- script: true
  with: {interpreter: [bash, sh]}
`
	var d Description
	if err := codec.Unmarshal([]byte(src), &d, codec.Tony); err != nil {
		t.Fatal(err)
	}
	want := Description{Offers: []Offer{
		{
			Agent:       "verse",
			With:        map[string]Accepts{"model": AcceptsAny, "effort": AcceptsOneOf("low", "medium", "high")},
			Intercepts:  []string{"files.write", "commands.run"},
			Annotations: map[string]string{"verse.portkey/virtual-key": "string"},
		},
		{Script: true, With: map[string]Accepts{"interpreter": AcceptsOneOf("bash", "sh")}},
	}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("read\n%+v\nwant\n%+v", d, want)
	}
	out, err := codec.Marshal(d, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var back Description
	if err := codec.Unmarshal(out, &back, codec.Tony); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("after the trip\n%+v\nwant\n%+v\nwire:\n%s", back, want, out)
	}
}

func TestAcceptsRefusesWhatItCannotRead(t *testing.T) {
	var d Description
	if err := codec.Unmarshal([]byte(`offers: [{agent: x, with: {model: everything}}]`), &d, codec.Tony); err == nil {
		t.Fatal("a with value that is neither any nor a list was read")
	}
}
