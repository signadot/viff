package trigger

import "testing"

func TestProbeDeriveResult2(t *testing.T) {
	for name, src := range map[string]string{
		"onDone derive of $result":       "spawn: {prompt: hi}\nonDone: {derive: [{path: [demo, finding], value: {note: $result}}]}",
		"onDone derive of $subject.name": "spawn: {prompt: hi}\nonDone: {derive: [{path: [demo, finding], value: {note: $subject.name}}]}",
		"onDone patch of $result":        "spawn: {prompt: hi}\nonDone: {patch: {note: $result}}",
		"seq element derive of $result":  "seq: [{derive: [{path: [demo, finding], value: {note: $result}}]}]",
	} {
		var a Action
		if err := a.FromTony([]byte(src)); err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		t.Logf("%-32s validate → %v", name, a.validate())
	}
}
