package runner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/task"
)

// A description reads as the Runners doc writes it, and survives the trip back.
func TestDescriptionRoundTrips(t *testing.T) {
	src := `offers:
- agent: verse
  with: {model: any, effort: [low, medium, high]}
  systems:
    files: {ceiling: [{then: allow}]}
    commands: {ceiling: [{then: allow}]}
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
			Agent: "verse",
			With:  map[string]Accepts{"model": AcceptsAny, "effort": AcceptsOneOf("low", "medium", "high")},
			Systems: map[string]System{
				"files":    {Ceiling: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}},
				"commands": {Ceiling: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}},
			},
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

// A DESCRIPTION OFFERS SOMETHING AND SAYS EACH OFFER'S NETWORKS, by mode, or verse does not
// register the runner.
func TestADescriptionNamesEachOffersNetworks(t *testing.T) {
	nets := func(a Accepts) map[string]Accepts { return map[string]Accepts{"network": a} }
	for _, c := range []struct {
		name string
		d    Description
		says string // "" = registered
	}{
		{"every offer names its networks", Description{Offers: []Offer{
			{Agent: "verse", With: nets(AcceptsOneOf(NetworkOpen))}, {Script: true, With: nets(AcceptsOneOf(NetworkNone, NetworkAllow))}}}, ""},
		{"no offers", Description{}, "it offers nothing"},
		{"an offer names none", Description{Offers: []Offer{{Agent: "verse", With: nets(AcceptsOneOf(NetworkOpen))}, {Script: true}}}, "offer script: with.network names no network"},
		{"an offer takes any", Description{Offers: []Offer{{Agent: "verse", With: nets(AcceptsAny)}}}, "offer verse: with.network names no network"},
		{"an offer names no mode", Description{Offers: []Offer{{Agent: "verse", With: nets(AcceptsOneOf("wide"))}}}, `"wide" is not a network mode`},
	} {
		err := c.d.Check()
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want it refused saying %q", c.name, err, c.says)
		}
	}
}
