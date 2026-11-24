package viffd

import (
	"strings"
	"time"

	"go.viff.dev/viff/internal/trigger"
	"go.viff.dev/viff/pkg/entity"
)

// userRules is the baseline charter without the system's own rules: every viff instance stands on the
// base charter (system.tony), so a test asserting what a person installed reads past it.
//
// Armed, once the pass a placed rule waits on has run: a rule just restored or installed is
// pending for it, briefly, and a reading taken before the pass would miss it.
func userRules(eng *trigger.Engine) []trigger.Trigger {
	deadline := time.Now().Add(2 * time.Second)
	for passing(eng) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var out []trigger.Trigger
	for _, t := range eng.Triggers() {
		if eng.OwnerOf(t.Name) == entity.SystemCharter {
			continue
		}
		out = append(out, t)
	}
	return out
}

// passing is whether any rule of the engine waits on the pass that arms it.
func passing(eng *trigger.Engine) bool {
	for _, p := range eng.Pendings() {
		if strings.HasPrefix(p.Cause, "waiting for a pass") {
			return true
		}
	}
	return false
}
