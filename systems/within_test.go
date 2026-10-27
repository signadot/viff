package systems

import (
	"strings"
	"testing"
)

// AN ENVIRONMENT WITHIN ANOTHER reaches no system the other does not, and each block of a
// system lies whole within one of the other's: its attributes the other's values, its verbs
// given there, its shapes within the other's.
func TestAnEnvironmentWithinAnother(t *testing.T) {
	outer := Environment{Systems: Systems{
		"git":     {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"fetch": {{}}, "push": {{"ref": "refs/heads/verse/**"}}}}},
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
			"git": {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"push": {{"ref": "refs/heads/verse/auto/*"}}}}},
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
			"git": {{Attributes: map[string]string{"repo": "github.com/signadot/verse", "from": "main"}, Verbs: map[string][]Shape{"push": {{"ref": "refs/heads/**"}}}}},
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
