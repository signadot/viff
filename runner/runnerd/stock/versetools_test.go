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
// answered. A runner that reaches no verse refuses a launch that names the verse system.
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
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["verse"] = []systems.Block{{}}
	script := "#!/bin/sh\n" +
		"verse-call-tool verse-ask-create -question 'Land it?' -to 'user:alice@example.com, group:leads' -label team=payments,kind=deploy -due ' 48h ' -then '{spawn: {script: \"true\"}}'\n"
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
	if to, _ := got["to"].([]any); len(to) != 2 || to[0] != "user:alice@example.com" || to[1] != "group:leads" {
		t.Errorf("the ask is put to %v, want alice and the leads", got["to"])
	}
	if labels, _ := got["labels"].(map[string]any); labels["team"] != "payments" || labels["kind"] != "deploy" {
		t.Errorf("the ask's labels are %v, want team and kind", got["labels"])
	}

	// A runner that reaches no verse doesn't offer the verse system, and refuses a launch that
	// names it.
	if _, ok := c.Describe(nil).Offers[0].Systems["verse"]; ok {
		t.Error("a runner that reaches no verse offers the verse system")
	}
	plain := serveConfigured(t, c, Secrets{}, Tokens{})
	frames, stop := plain.Launch(runner.Launch{ID: "r2", Script: script, Environment: env, Autonomy: allow}).Subscribe()
	defer stop()
	if f := <-frames; f.Kind != task.EventStatus || task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "verse") {
		t.Errorf("a launch naming verse on a runner that reaches none came back %+v", f)
	}
}

// THE TOOL TAKES WHAT verse_ask_create TAKES: `to` a comma-separated list, `then` and `else` tony
// documents, `label` name=value pairs, as a model writes them; and each as the value a script's
// verse-call-tool already read it as.
func TestTheAskCreateToolTakesWhatTheMCPToolTakes(t *testing.T) {
	in := map[string]any{
		"to":    "user:ana@example.com, group:leads",
		"then":  `{spawn: {script: "true"}}`,
		"label": "team=payments, kind=deploy",
	}
	if got := listIn(in, "to"); len(got) != 2 || got[1] != "group:leads" {
		t.Errorf("to = %v", got)
	}
	if doc, err := docIn(in, "then"); err != nil || doc["spawn"] == nil {
		t.Errorf("then = %v, %v", doc, err)
	}
	if labels, err := labelsIn(in, "label"); err != nil || labels["kind"] != "deploy" {
		t.Errorf("label = %v, %v", labels, err)
	}
	if _, err := docIn(map[string]any{"then": "spawn it"}, "then"); err == nil {
		t.Error("a then that is no document was taken")
	}
	if _, err := labelsIn(map[string]any{"label": "team"}, "label"); err == nil {
		t.Error("a label with no value was taken")
	}
	if got := listIn(map[string]any{"to": []any{"user:ana@example.com"}}, "to"); len(got) != 1 {
		t.Errorf("a list to = %v", got)
	}
}
