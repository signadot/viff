package stock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A SCRIPT RAISES AN ASK FOR ITS OWNER, and the runner carries it to verse: the raiser is handed
// the task and the ask as the script wrote it, its name this task's id where the script named
// none, its due a date where the script gave a duration, and the script reads the id verse
// answered. A runner with no raiser offers no question-raise.
func TestAScriptRaisesAnAskTheRunnerCarriesToVerse(t *testing.T) {
	withCallTool(t)
	var mu sync.Mutex
	var saw []map[string]any
	raise := func(_ context.Context, taskID string, ask []byte) (int, string, error) {
		var doc map[string]any
		if err := codec.Unmarshal(ask, &doc, codec.Tony); err != nil {
			return 400, err.Error(), nil
		}
		doc["for-task"] = taskID
		mu.Lock()
		saw = append(saw, doc)
		mu.Unlock()
		return 201, "id: " + doc["name"].(string) + "-1a2b", nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"question": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["question"] = []systems.Block{{}}
	script := "#!/bin/sh\n" +
		"verse-call-tool question-raise -question 'Land it?' -to user:alice@example.com -due 48h -then '{spawn: {script: \"true\"}}'\n"
	allow := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

	rt := serveRaising(t, c, Secrets{}, Tokens{}, raise)
	rt.Launch(runner.Launch{ID: "r1", Script: script, Environment: env, Autonomy: allow})
	snap := ended(t, rt, "r1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "raised ask r1-1a2b") {
		t.Fatalf("the script read %q (%s), want the ask's id", snap.Result, snap.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saw) != 1 {
		t.Fatalf("the raiser was handed %d asks", len(saw))
	}
	got := saw[0]
	then, _ := got["then"].(map[string]any)
	spawn, _ := then["spawn"].(map[string]any)
	if got["for-task"] != "r1" || got["name"] != "r1" || got["question"] != "Land it?" || spawn["script"] != "true" {
		t.Errorf("the raiser was handed %v", got)
	}
	if due, err := time.Parse(time.RFC3339, fmt.Sprint(got["due"])); err != nil || time.Until(due) < 47*time.Hour || time.Until(due) > 49*time.Hour {
		t.Errorf("the ask's due is %v, want a date 48h from now", got["due"])
	}
	if to, _ := got["to"].([]any); len(to) != 1 || to[0] != "user:alice@example.com" {
		t.Errorf("the ask is put to %v, want alice", got["to"])
	}

	plain := serveConfigured(t, c, Secrets{}, Tokens{})
	plain.Launch(runner.Launch{ID: "r2", Script: script, Environment: env, Autonomy: allow})
	if snap := ended(t, plain, "r2"); strings.Contains(snap.Result, "raised ask") {
		t.Errorf("a runner with no raiser raised an ask: %q", snap.Result)
	}
}
