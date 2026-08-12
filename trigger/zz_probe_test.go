package trigger

import "testing"

func TestProbeDeriveResult2(t *testing.T) {
	for name, src := range map[string]string{
		"onDone derive of $result":      "spawn: {prompt: hi}\nonDone: {derive: {demo: {finding: {note: $result}}}}",
		"onDone derive of $subject.id":  "spawn: {prompt: hi}\nonDone: {derive: {demo: {finding: {note: $subject.id}}}}",
		"onDone patch of $result":       "spawn: {prompt: hi}\nonDone: {patch: {note: $result}}",
		"seq element derive of $result": "seq: [{derive: {demo: {finding: {note: $result}}}}]",
	} {
		var a Action
		if err := a.FromTony([]byte(src)); err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		t.Logf("%-32s validate → %v", name, a.validate())
	}
}
