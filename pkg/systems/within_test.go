package systems

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
)

// AN ENVIRONMENT WITHIN ANOTHER reaches no system the other does not, and each block of a
// system lies whole within one of the other's: its attributes the other's values, its verbs
// given there, its shapes within the other's.
func TestAnEnvironmentWithinAnother(t *testing.T) {
	outer := Environment{Systems: Systems{
		"git":     {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"fetch": {{}}, "push": {{"ref": ir.FromString("^refs/heads/verse/").WithTag("!regexp")}}}}},
		"network": {{Attributes: map[string]string{"mode": "open"}}},
	}}
	for name, c := range map[string]struct {
		inner Environment
		says  string // "" for within
	}{
		"the same": {outer, ""},
		"less": {Environment{Systems: Systems{
			"network": {{Attributes: map[string]string{"mode": "open"}}},
		}}, ""},
		"a narrower push": {Environment{Systems: Systems{
			"git": {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"push": {{"ref": ir.FromString("refs/heads/verse/auto/*").WithTag("!glob")}}}}},
		}}, ""},
		"a system the outer does not reach": {Environment{Systems: Systems{
			"commands": {{Verbs: map[string][]Shape{"run": {{}}}}},
		}}, "reaches no commands"},
		"another repository": {Environment{Systems: Systems{
			"git": {{Attributes: map[string]string{"repo": "github.com/signadot/cli", "from": "main"}, Verbs: map[string][]Shape{"fetch": {{}}}}},
		}}, "repo github.com/signadot/cli is beyond"},
		"a verb the outer does not give": {Environment{Systems: Systems{
			"network": {{Attributes: map[string]string{"mode": "open"}, Verbs: map[string][]Shape{"connect": {{}}}}},
		}}, "gives no `connect`"},
		"a wider push": {Environment{Systems: Systems{
			"git": {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"push": {{"ref": ir.FromString("^refs/heads/").WithTag("!regexp")}}}}},
		}}, "within no shape"},
	} {
		err := c.inner.Within(outer)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: %v, want within", name, err)
		case c.says != "" && err == nil:
			t.Errorf("%s: within, want refused for %q", name, c.says)
		case c.says != "" && !strings.Contains(err.Error(), c.says):
			t.Errorf("%s: refused for another reason: %v", name, err)
		}
	}
}

// AN ENVIRONMENT LESS A VERB gives every other verb its blocks gave, a block that named none
// included, and a system whose blocks gave only that verb is not reached at all.
func TestAnEnvironmentLessAVerb(t *testing.T) {
	all := Environment{Systems: Systems{"verse": {{}}, "network": {{Attributes: map[string]string{"mode": "open"}}}}}
	less := all.Without("verse", "charter-install")
	if len(all.Systems["verse"][0].Verbs) != 0 {
		t.Fatal("Without changed the environment it was given")
	}
	if err := (Environment{Systems: Systems{"verse": {{Verbs: map[string][]Shape{"data-get": {{}}, "charter-remove": {{}}}}}}}).Within(less); err != nil {
		t.Errorf("the other verbs: %v", err)
	}
	for name, inner := range map[string]Environment{
		"every verb":     {Systems: Systems{"verse": {{}}}},
		"the verb alone": {Systems: Systems{"verse": {{Verbs: map[string][]Shape{"charter-install": {{}}}}}}},
	} {
		if err := inner.Within(less); err == nil || !strings.Contains(err.Error(), "charter-install") {
			t.Errorf("%s: %v, want refused for charter-install", name, err)
		}
	}
	if err := (Environment{Systems: Systems{"network": {{Attributes: map[string]string{"mode": "open"}}}}}).Within(less); err != nil {
		t.Errorf("another system: %v", err)
	}
	// A block that does not give the verb stands as written: the network's mode gives no verb.
	if got := all.Without("network", "tcp-client").Systems["network"]; len(got) != 1 || got[0].Attributes["mode"] != "open" {
		t.Errorf("less a verb it does not give, the network is %v", got)
	}
	only := Environment{Systems: Systems{"verse": {{Verbs: map[string][]Shape{"charter-install": {{}}}}}}}
	if _, ok := only.Without("verse", "charter-install").Systems["verse"]; ok {
		t.Error("a system that gave only the verb is still reached")
	}
}

// AS WRITTEN, an attribute still to be bound is within where some value of it could be; bound,
// it is held to its value.
func TestAnEnvironmentWithinAnotherAsWritten(t *testing.T) {
	outer := Environment{Systems: Systems{"git": {{Attributes: map[string]string{"repo": "github.com/signadot/*", "from": "main"}}}}}
	written := Environment{Systems: Systems{"git": {{Attributes: map[string]string{"repo": "$[repo]", "from": "main"}}}}}
	if err := written.WithinAsWritten(outer); err != nil {
		t.Errorf("as written: %v", err)
	}
	if err := written.Within(outer); err == nil {
		t.Error("`$[repo]` bound as itself is within github.com/signadot/*")
	}
	other := Environment{Systems: Systems{"git": {{Attributes: map[string]string{"repo": "github.com/other/$[repo]", "from": "main"}}}}}
	if err := other.WithinAsWritten(outer); err == nil {
		t.Error("another owner before the `$` is within, as written")
	}
}
