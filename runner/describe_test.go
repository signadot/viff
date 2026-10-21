package runner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/systems"
)

// builds is a network System building the modes given.
func builds(modes ...string) System {
	return System{Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": modes}}}}
}

// A description reads as the Runners doc writes it, and survives the trip back.
func TestDescriptionRoundTrips(t *testing.T) {
	src := `version: 2
offers:
- agent:
    name: verse
    settings:
      model: {values: ["claude-*"], default: true}
      temperature: {form: number}
    annotations: {verse.portkey/virtual-key: string}
  converse: true
  systems:
    network: {builds: {mode: [none, open, allow]}, ceiling: {mode: [none, allow], tcp-client: {host: "*.github.com"}}}
    files: {ceiling: {write: {}}}
    commands: {ceiling: {run: [{command: "make *"}, {command: "go test ./..."}]}}
    git: {ceiling: [{repo: [acme/app, "github.com/acme/lib*"], fetch: {}}, {repo: acme/docs, push: {ref: "refs/heads/verse/**"}}]}
- script: true
  systems:
    network: {builds: {mode: [open]}}
`
	var d Description
	if err := codec.Unmarshal([]byte(src), &d, codec.Tony); err != nil {
		t.Fatal(err)
	}
	want := Description{Version: 2, Offers: []Offer{
		{
			Agent: &AgentOffer{
				Name: "verse",
				Settings: map[string]Setting{
					"model":       {Values: []string{"claude-*"}, Default: true},
					"temperature": {Form: FormNumber},
				},
				Annotations: map[string]string{"verse.portkey/virtual-key": "string"},
			},
			Converse: true,
			Systems: map[string]System{
				"network": {
					Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": {"none", "open", "allow"}}, Verbs: map[string][]systems.Shape{}}},
					Ceiling: systems.Ceiling{{Attributes: map[string][]string{"mode": {"none", "allow"}},
						Verbs: map[string][]systems.Shape{"tcp-client": {{"host": "*.github.com"}}}}},
				},
				"files": {Ceiling: systems.Ceiling{{Attributes: map[string][]string{},
					Verbs: map[string][]systems.Shape{"write": {{}}}}}},
				"commands": {Ceiling: systems.Ceiling{{Attributes: map[string][]string{},
					Verbs: map[string][]systems.Shape{"run": {{"command": "make *"}, {"command": "go test ./..."}}}}}},
				"git": {Ceiling: systems.Ceiling{
					{Attributes: map[string][]string{"repo": {"acme/app", "github.com/acme/lib*"}}, Verbs: map[string][]systems.Shape{"fetch": {{}}}},
					{Attributes: map[string][]string{"repo": {"acme/docs"}}, Verbs: map[string][]systems.Shape{"push": {{"ref": "refs/heads/verse/**"}}}},
				}},
			},
		},
		{Script: true, Systems: map[string]System{"network": {
			Builds: systems.Ceiling{{Attributes: map[string][]string{"mode": {"open"}}, Verbs: map[string][]systems.Shape{}}},
		}}},
	}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("read\n%+v\nwant\n%+v", d, want)
	}
	if err := d.Check(); err != nil {
		t.Fatalf("checked: %v", err)
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

// AN AGENT IS WRITTEN FLAT: its name, its settings beside it, a number or a boolean read as it is
// spelled, and its annotations nested; and it is written back so.
func TestAnAgentIsWrittenFlat(t *testing.T) {
	var a Agent
	if err := codec.Unmarshal([]byte(`{name: codex, model: gpt-5-codex, effort: high, temperature: 0.2, stream: true, annotations: {verse.portkey/virtual-key: vk}}`), &a, codec.Tony); err != nil {
		t.Fatal(err)
	}
	want := Agent{Name: "codex",
		Settings:    map[string]string{"model": "gpt-5-codex", "effort": "high", "temperature": "0.2", "stream": "true"},
		Annotations: map[string]string{"verse.portkey/virtual-key": "vk"}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("read %+v, want %+v", a, want)
	}
	if a.Model() != "gpt-5-codex" {
		t.Errorf("model = %q", a.Model())
	}
	out, err := codec.Marshal(a, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	var back Agent
	if err := codec.Unmarshal(out, &back, codec.Tony); err != nil || !reflect.DeepEqual(back, want) {
		t.Fatalf("after the trip %+v (%v), want %+v\nwire:\n%s", back, err, want, out)
	}
	for _, bad := range []string{`{name: x, model: {a: b}}`, `{name: x, annotations: [a]}`, `[x]`} {
		var a Agent
		if err := codec.Unmarshal([]byte(bad), &a, codec.Tony); err == nil {
			t.Errorf("%s was read: %+v", bad, a)
		}
	}
	if !ReservedSetting("name") || !ReservedSetting("annotations") || ReservedSetting("model") {
		t.Error("the reserved names")
	}
}

// A DESCRIPTION IS OF THIS VERSION, OFFERS SOMETHING, AND SAYS WHAT EACH OFFER BUILDS OF THE
// NETWORK, or verse does not register the runner (or read one it kept).
func TestADescriptionIsChecked(t *testing.T) {
	verse := func(settings map[string]Setting) Offer {
		return Offer{Agent: &AgentOffer{Name: "verse", Settings: settings}, Systems: map[string]System{"network": builds(NetworkOpen)}}
	}
	script := Offer{Script: true, Systems: map[string]System{"network": builds(NetworkNone, NetworkAllow)}}
	for _, c := range []struct {
		name string
		d    Description
		says string // "" = registered
	}{
		{"every offer builds its network", Description{Version: DescriptionVersion, Offers: []Offer{verse(nil), script}}, ""},
		{"an older version", Description{Version: 1, Offers: []Offer{verse(nil)}}, "version 1, not 2"},
		{"none", Description{Offers: []Offer{verse(nil)}}, "version 0"},
		{"no offers", Description{Version: DescriptionVersion}, "it offers nothing"},
		{"an offer builds no network", Description{Version: DescriptionVersion, Offers: []Offer{verse(nil), {Script: true}}}, "offer script: the network's `builds`"},
		{"a mode no network has", Description{Version: DescriptionVersion, Offers: []Offer{{Script: true, Systems: map[string]System{"network": builds("wide")}}}}, "builds"},
		{"neither agent nor script", Description{Version: DescriptionVersion, Offers: []Offer{{Systems: map[string]System{"network": builds(NetworkOpen)}}}}, "one of the two"},
		{"an agent with no name", Description{Version: DescriptionVersion, Offers: []Offer{{Agent: &AgentOffer{}, Systems: map[string]System{"network": builds(NetworkOpen)}}}}, "names its agent"},
		{"a setting called name", Description{Version: DescriptionVersion, Offers: []Offer{verse(map[string]Setting{"name": {}})}}, "may not be called name"},
		{"a setting called annotations", Description{Version: DescriptionVersion, Offers: []Offer{verse(map[string]Setting{"annotations": {}})}}, "may not be called annotations"},
		{"a form no setting has", Description{Version: DescriptionVersion, Offers: []Offer{verse(map[string]Setting{"x": {Form: "list"}})}}, "not string, number or bool"},
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

// A SETTING TAKES A VALUE OF ITS FORM, among its values where it lists them; one still to be
// bound is the launch's to say.
func TestASettingTakesItsValues(t *testing.T) {
	for _, c := range []struct {
		s     Setting
		v     string
		takes bool
	}{
		{Setting{}, "anything", true},
		{Setting{Values: []string{"claude-*"}}, "claude-sonnet-5", true},
		{Setting{Values: []string{"claude-*"}}, "gpt-5", false},
		{Setting{Form: FormNumber}, "0.2", true},
		{Setting{Form: FormNumber}, "warm", false},
		{Setting{Form: FormBool}, "true", true},
		{Setting{Form: FormBool}, "yes", false},
		{Setting{Values: []string{"low", "high"}}, "$subject.effort", false}, // bound, and none of them
	} {
		if err := c.s.Takes(c.v); (err == nil) != c.takes {
			t.Errorf("%+v takes %q: %v, want %v", c.s, c.v, err, c.takes)
		}
	}
}

// A SETTING'S VALUES ARE OF ITS FORM, or patterns that could be: a number setting listing a word
// would take nothing.
func TestASettingsValuesAreOfItsForm(t *testing.T) {
	for _, c := range []struct {
		s  Setting
		ok bool
	}{
		{Setting{Form: FormNumber, Values: []string{"0.2", "1"}}, true},
		{Setting{Form: FormNumber, Values: []string{"0.*"}}, true},
		{Setting{Form: FormNumber, Values: []string{"warm"}}, false},
		{Setting{Form: FormBool, Values: []string{"yes"}}, false},
		{Setting{Values: []string{"anything"}}, true},
	} {
		if err := c.s.check(); (err == nil) != c.ok {
			t.Errorf("%+v: %v, want ok %v", c.s, err, c.ok)
		}
	}
}

// AN OFFER REQUIRES ONLY WHAT IT COULD BE GIVEN: a step of a system, held, which its ceiling gives.
func TestAnOffersRequiresIsChecked(t *testing.T) {
	offer := func(requires string, sys map[string]System) Description {
		sys["network"] = builds(NetworkOpen)
		return Description{Version: DescriptionVersion, Offers: []Offer{{Agent: &AgentOffer{Name: "codex"}, Requires: []string{requires}, Systems: sys}}}
	}
	for says, d := range map[string]Description{
		"no step of a system":            offer("commands.fly", map[string]System{"commands": {Ceiling: systems.Open()}}),
		"of a system it does not hold":   offer("files.write", map[string]System{}),
		"which its ceiling for commands": offer("commands.run", map[string]System{"commands": {}}),
	} {
		if err := d.Check(); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%v: %v, want it refused saying %q", d.Offers[0].Requires, err, says)
		}
	}
	if err := offer("commands.run", map[string]System{"commands": {Ceiling: systems.Open()}}).Check(); err != nil {
		t.Errorf("a required step the ceiling gives: %v", err)
	}
}
