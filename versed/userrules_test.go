package versed

import (
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/trigger"
)

// userRules is the baseline charter without the system's own rules: every verse stands on the
// base charter (system.tony), so a test asserting what a person installed reads past it.
func userRules(eng *trigger.Engine) []trigger.Trigger {
	var out []trigger.Trigger
	for _, t := range eng.Triggers() {
		if eng.InstalledBy(t.Name) == entity.SystemCharter {
			continue
		}
		out = append(out, t)
	}
	return out
}
