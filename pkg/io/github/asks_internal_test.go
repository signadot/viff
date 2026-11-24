package github

import "testing"

// An option is matched by its name, not its case, and a `/viff link` line does not hide a command
// on a later line.
func TestPlacingACommand(t *testing.T) {
	asks := []asked{{ID: "q1", Options: []string{"LGTM", "Hold"}}, {ID: "q2", Options: []string{"proceed", "deny"}, Settled: true}}
	if a, canon, say, could := placed(asks, "lgtm", "ship it"); a == nil || a.ID != "q1" || canon != "LGTM" || say != "ship it" || could != nil {
		t.Errorf("lgtm = %v %q %q %v, want q1 as LGTM with the words kept", a, canon, say, could)
	}
	if a, _, _, could := placed(asks, "proceed", ""); a != nil || could != nil {
		t.Errorf("an option only a settled question offers placed: %v %v", a, could)
	}
	if option, say, ok := command("/viff link abc\n/viff Hold not yet"); !ok || option != "Hold" || say != "not yet" {
		t.Errorf("command after a link line = %q %q %v, want Hold", option, say, ok)
	}
}
