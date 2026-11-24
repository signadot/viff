package stock

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/runner/runnerd/viff"
	"go.viff.dev/viff/pkg/systems"
	"go.viff.dev/viff/pkg/task"
)

// A SCRIPT RAISES AN ASK FOR ITS OWNER, and the runner carries it to viff: the raiser is handed
// the task and the ask as the script wrote it, its due a date where the script gave a duration,
// and the script reads the id viff answered, then reads the ask back and lists its owner's asks.
// A runner that reaches no viff refuses a launch that names the viff system.
func TestAScriptRaisesAnAskTheRunnerCarriesToViff(t *testing.T) {
	withCallTool(t)
	var mu sync.Mutex
	var saw []map[string]any
	raise := func(_ context.Context, taskID, method, path string, ask []byte) (int, string, error) {
		// What it reads back, after: the ask it raised, answered, and one its owner raised before.
		if method == http.MethodGet && (path == "/asks" || strings.HasPrefix(path, "/asks?")) {
			return 200, `- {id: r1-1a2b, name: r1, question: "Land it?", answer: yes, status: answered}
- {id: tea-9z9z, name: tea, question: "Tea?", status: pending}`, nil
		}
		var doc map[string]any
		if err := codec.Unmarshal(ask, &doc, codec.Tony); err != nil {
			return 400, err.Error(), nil
		}
		doc["for-task"] = taskID
		mu.Lock()
		saw = append(saw, doc)
		mu.Unlock()
		name, _ := doc["name"].(string)
		return 201, "id: " + cmp.Or(name, "run") + "-1a2b", nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"viff": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["viff"] = []systems.Block{{}}
	script := "#!/bin/sh\n" +
		"viff-call-tool viff-ask-create -name r1 -question 'Land it?' -to 'user:alice@example.com, group:leads' -label team=payments,kind=deploy -due ' 48h ' -on '[{match: {verdict: proceed}, then: {spawn: {script: \"true\"}}}]'\n" +
		"viff-call-tool viff-ask-get -name_or_id r1-1a2b\n" +
		"viff-call-tool viff-ask-list\n"
	allow := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

	rt := serveRaising(t, c, Secrets{}, Tokens{}, raise)
	rt.Launch(runner.Launch{ID: "r1", Script: script, Environment: env, Autonomy: allow})
	snap := ended(t, rt, "r1")
	if snap.Status != task.Done || !strings.Contains(snap.Result, "raised question r1-1a2b: Land it?") {
		t.Fatalf("the script read %q (%s), want the ask's id", snap.Result, snap.Status)
	}
	if !strings.Contains(snap.Result, "answer: yes") {
		t.Errorf("the script read the ask back as %q, want it answered", snap.Result)
	}
	if !strings.Contains(snap.Result, "tea-9z9z") {
		t.Errorf("the script listed its owner's asks as %q, want the owner's other ask among them", snap.Result)
	}
	// An ask the script names nothing is raised all the same, and viff names it, as
	// viff_ask_create and `viff ask -q` leave it to viff: the task's id is not its name.
	rt.Launch(runner.Launch{ID: "r3", Script: "#!/bin/sh\nviff-call-tool viff-ask-create -question 'Ship it?'\n",
		Environment: env, Autonomy: allow})
	if snap := ended(t, rt, "r3"); snap.Status != task.Done || !strings.Contains(snap.Result, "raised question run-1a2b: Ship it?") {
		t.Errorf("a raise naming no ask read %q (%s), want it raised under viff's name", snap.Result, snap.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saw) != 2 {
		t.Fatalf("the raiser was handed %d asks", len(saw))
	}
	if name, _ := saw[1]["name"].(string); name != "" {
		t.Errorf("an ask the script named nothing was handed to viff as %v, want no name", saw[1])
	}
	got := saw[0]
	rows, _ := got["on"].([]any)
	var spawn map[string]any
	if len(rows) == 1 {
		row, _ := rows[0].(map[string]any)
		then, _ := row["then"].(map[string]any)
		spawn, _ = then["spawn"].(map[string]any)
	}
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

	// A runner that reaches no viff doesn't offer the viff system, and refuses a launch that
	// names it.
	if _, ok := c.Describe(nil).Offers[0].Systems["viff"]; ok {
		t.Error("a runner that reaches no viff offers the viff system")
	}
	plain := serveConfigured(t, c, Secrets{}, Tokens{})
	frames, stop := plain.Launch(runner.Launch{ID: "r2", Script: script, Environment: env, Autonomy: allow}).Subscribe()
	defer stop()
	if f := <-frames; f.Kind != task.EventStatus || task.Status(f.Text) != task.Failed || !strings.Contains(f.Err, "viff") {
		t.Errorf("a launch naming viff on a runner that reaches none came back %+v", f)
	}
}

// A SCRIPT READS AND WRITES THE VIFF FOR ITS OWNER, and the runner carries each act on its
// route: the write as the PutEntity `viff data --put` sends, with its condition; the read with
// the query `viff data` sends, trimmed as viff_data trims it, or listed one kinded path per
// line; the removal said as `viff data --rm` says it.
func TestAScriptReadsAndWritesTheViffTheRunnerCarries(t *testing.T) {
	withCallTool(t)
	var mu sync.Mutex
	var saw []string
	var wrote map[string]any
	carry := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		mu.Lock()
		defer mu.Unlock()
		saw = append(saw, method+" "+path)
		switch {
		case method == http.MethodPost && path == "/entity":
			if err := codec.Unmarshal(body, &wrote, codec.Tony); err != nil {
				return 400, err.Error(), nil
			}
			return 200, "{stage: open, owner: dana}", nil
		case method == http.MethodGet && strings.Contains(path, "paths=1"):
			return 200, "{paths: [acme.notes, acme.notes.n1]}", nil
		case method == http.MethodGet:
			// A read at an address of names is answered as the node there.
			return 200, "{stage: open, owner: dana}", nil
		case method == http.MethodDelete:
			return 204, "", nil
		}
		return 404, method + " " + path, nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"viff": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["viff"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"viff-call-tool viff-data-put -at acme.notes.n1 -value '{stage: open}' -create\n" +
		"echo ---\n" +
		"viff-call-tool viff-data-get -at acme.notes.n1 -trim '{stage}'\n" +
		"echo ---\n" +
		"viff-call-tool viff-data-get -at acme.notes -paths -depth 1\n" +
		"echo ---\n" +
		"viff-call-tool viff-data-rm -at acme.notes.n1\n"
	allow := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

	rt := serveRaising(t, c, Secrets{}, Tokens{}, carry)
	rt.Launch(runner.Launch{ID: "d1", Script: script, Environment: env, Autonomy: allow})
	snap := ended(t, rt, "d1")
	if snap.Status != task.Done {
		t.Fatalf("the script ended %s: %q", snap.Status, snap.Result)
	}
	parts := strings.Split(snap.Result, "---")
	if len(parts) != 4 {
		t.Fatalf("the script said %q", snap.Result)
	}
	if !strings.Contains(parts[0], "owner: dana") {
		t.Errorf("the write answered %q, want what stands there after it", parts[0])
	}
	if got := strings.TrimSpace(parts[1]); !strings.Contains(got, "stage: open") || strings.Contains(got, "owner") {
		t.Errorf("the trimmed read answered %q, want only the stage", got)
	}
	if got := strings.TrimSpace(parts[2]); got != "acme.notes\nacme.notes.n1" {
		t.Errorf("the listing answered %q, want one kinded path per line", got)
	}
	if got := strings.TrimSpace(parts[3]); got != "removed acme notes n1" {
		t.Errorf("the removal said %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"POST /entity", "GET /state?at=acme.notes.n1", "GET /state?at=acme.notes&depth=1&paths=1", "DELETE /entity?at=acme.notes.n1"}
	if strings.Join(saw, "|") != strings.Join(want, "|") {
		t.Errorf("the runner carried %v, want %v", saw, want)
	}
	payload, _ := wrote["payload"].(map[string]any)
	when, _ := wrote["when"].([]any)
	if wrote["at"] != "acme.notes.n1" || payload["stage"] != "open" || len(when) != 1 {
		t.Errorf("the write carried %v, want the note on create", wrote)
	}
}

// A TOOL REFUSES WHAT verbs.Read AND THE GRAMMAR REFUSE, saying it after what was not done and
// naming the parameter, before anything is carried.
func TestTheViffToolsRefuseNamingTheParameter(t *testing.T) {
	tools := map[string]viff.Tool{}
	for _, tl := range append(append(viffTools(nil), viffDataTools(nil)...), viffCharterTools(nil)...) {
		tools[tl.Name] = tl
	}
	for _, c := range []struct {
		tool string
		in   map[string]any
		want string
	}{
		{dataPutTool, map[string]any{"at": "acme.notes.n1", "value": "{stage: "}, "not written: value: "},
		{dataPutTool, map[string]any{"at": "acme.notes.n1", "value": "1", "create": true, "update": true}, "not written: create: create and update are opposites"},
		{dataGetTool, map[string]any{"if_at": "labels"}, "not read: if_at: "},
		{dataGetTool, map[string]any{"depth": 1}, "not read: depth goes with paths"},
		{dataPutTool, map[string]any{"at": "acme.notes.n1", "value": 1}, "not written: value is a string"},
		{askCreateTool, map[string]any{"questoin": "q"}, "not raised: questoin is not a parameter"},
		{askCreateTool, map[string]any{"question": "q", "on": "spawn it"}, "not raised: on: "},
		{askGetTool, map[string]any{"name_or_id": "  "}, "not read: name the ask"},
		{charterRemoveTool, map[string]any{}, "not removed: path names what to remove"},
	} {
		out, isErr, err := tools[c.tool].Do(context.Background(), task.ToolUse{Input: c.in})
		if err != nil || !isErr || !strings.HasPrefix(out, c.want) {
			t.Errorf("%s %v = %q, %v, %v, want %q", c.tool, c.in, out, isErr, err, c.want)
		}
	}
}

// A SCRIPT READS ITS OWNER'S CHARTERS AND CHECKS ITS OWN, and the runner carries each on its
// route: a charter reads as its file with the notes the file cannot say, as viff_charter answers
// it; a listing one kinded path per rule and level; a check carries the charters, one document
// apiece, and answers viff's verdict.
func TestAScriptReadsTheChartersTheRunnerCarries(t *testing.T) {
	withCallTool(t)
	var mu sync.Mutex
	var saw []string
	var checked string
	carry := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		mu.Lock()
		defer mu.Unlock()
		saw = append(saw, method+" "+path)
		switch {
		case method == http.MethodGet && strings.Contains(path, "depth=1"):
			return 200, "{children: {hello: {writes: [repos.r.hello.*], rules: [{name: lift}], strata: [{name: low}]}}}", nil
		case method == http.MethodGet:
			return 200, "{writes: [repos.r.hello.*], rules: [{name: lift, held: \"waiting for stratum high\", condition: {subject: {path: repos.r.hello.low.*}}}]}", nil
		case method == http.MethodPost && path == "/charter/check?at=repos.r":
			checked = string(body)
			return 200, "{verdict: installs}", nil
		}
		return 404, method + " " + path, nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"viff": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["viff"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"viff-call-tool viff-charter-get -path repos.r.hello\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-get -path repos.r -depth 1 -paths\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-check -at repos.r -charters 'at: repos.r.hello\n---\nat: repos.r.bye'\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-get -path repos.r.hello -trim '{at: null}'\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-get -path repos.r.hello -if '{at: !glob [1]}' || true\n"
	allow := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

	rt := serveRaising(t, c, Secrets{}, Tokens{}, carry)
	rt.Launch(runner.Launch{ID: "c1", Script: script, Environment: env, Autonomy: allow})
	snap := ended(t, rt, "c1")
	if snap.Status != task.Done {
		t.Fatalf("the script ended %s: %q", snap.Status, snap.Result)
	}
	parts := strings.Split(snap.Result, "---")
	if len(parts) < 5 {
		t.Fatalf("the script said %q", snap.Result)
	}
	file := parts[0]
	if !strings.Contains(file, "at: repos.r.hello") || !strings.Contains(file, "name: lift") || strings.Contains(file, "held: \"waiting") ||
		!strings.Contains(file, "note: repos.r.hello: rule lift is held: waiting for stratum high") {
		t.Errorf("the charter read as %q, want its file and the note on the held rule", file)
	}
	if got := strings.TrimSpace(parts[1]); got != "repos.r.hello.rules.lift\nrepos.r.hello.strata.low" {
		t.Errorf("the listing answered %q, want one kinded path per rule and level", got)
	}
	if !strings.Contains(parts[2], "verdict: installs") {
		t.Errorf("the check answered %q, want viff's verdict", parts[2])
	}
	if trimmed := parts[3]; !strings.Contains(trimmed, "at: repos.r.hello") || strings.Contains(trimmed, "name: lift") {
		t.Errorf("the trimmed charter read as %q, want its at: alone", trimmed)
	}
	// A pattern that does not apply is said, as `viff charter --if` says it, never an answer
	// that kept nothing.
	if got := strings.TrimSpace(parts[4]); !strings.HasPrefix(got, "not read: if: cannot glob") {
		t.Errorf("the charter read with a pattern that does not apply answered %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"GET /charter?path=repos.r.hello", "GET /charter?depth=1&path=repos.r", "POST /charter/check?at=repos.r",
		"GET /charter?path=repos.r.hello", "GET /charter?path=repos.r.hello"}
	if strings.Join(saw, "|") != strings.Join(want, "|") {
		t.Errorf("the runner carried %v, want %v", saw, want)
	}
	if !strings.Contains(checked, "at: repos.r.hello\n---\n") || !strings.Contains(checked, "at: repos.r.bye") {
		t.Errorf("the check carried %q, want the charters one document apiece", checked)
	}
}

// A SCRIPT INSTALLS AND REMOVES ITS OWNER'S CHARTERS, and the runner carries each on its route: an
// install sends the charters as `viff charter -a` does, a rule list at its address or each
// charter at its own at: with a prune, and answers what went where as viff_charter_install says
// it; a removal says what went; what viff refuses, the script reads in viff's words.
func TestAScriptInstallsAndRemovesTheChartersTheRunnerCarries(t *testing.T) {
	withCallTool(t)
	var mu sync.Mutex
	var saw, sent []string
	carry := func(_ context.Context, taskID, method, path string, body []byte) (int, string, error) {
		mu.Lock()
		defer mu.Unlock()
		saw = append(saw, method+" "+path)
		switch {
		case method == http.MethodPost && path == "/charter?at=repos.r.hello":
			sent = append(sent, string(body))
			return 201, "{installed: [{at: repos.r.hello, rules: 1, strata: 0}]}", nil
		case method == http.MethodPost && path == "/charter?prune=repos.r":
			sent = append(sent, string(body))
			return 201, "{installed: [{at: repos.r.hello, rules: 0, strata: 0, expires: \"2026-10-31T00:00:00Z\"}], pruned: [{at: repos.r.old, uninstalled: true}]}", nil
		case method == http.MethodPost:
			return 403, "rule \"wide\": action.spawn.environment.systems.commands: beyond", nil
		case method == http.MethodDelete:
			return 204, "", nil
		}
		return 404, method + " " + path, nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"viff": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["viff"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"viff-call-tool viff-charter-install -at repos.r.hello -charters '[{name: lift, condition: {subject: repos.r.hello.*}, action: {patch: {seen: true}}}, {name: drop, condition: {subject: repos.r.hello.*}, action: {patch: {seen: false}}}]'\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-install -at repos.r -prune -charters 'at: repos.r.hello\nttl: 72h\n---\nat: repos.r.bye'\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-install -at repos.r.wide -charters '[{name: wide}]' || true\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-remove -path repos.r.hello.rules.lift\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-remove -path repos.r.bye\n" +
		"echo ---\n" +
		"viff-call-tool viff-charter-install -at repos.r.a -charters 'at: repos.r.a\n---\nat: repos.r.b' || true\n"
	allow := task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}

	rt := serveRaising(t, c, Secrets{}, Tokens{}, carry)
	rt.Launch(runner.Launch{ID: "c1", Script: script, Environment: env, Autonomy: allow})
	snap := ended(t, rt, "c1")
	if snap.Status != task.Done {
		t.Fatalf("the script ended %s: %q", snap.Status, snap.Result)
	}
	parts := strings.Split(snap.Result, "---")
	if len(parts) < 6 {
		t.Fatalf("the script said %q", snap.Result)
	}
	if got := strings.TrimSpace(parts[0]); !strings.HasPrefix(got, "package repos.r.hello: installed 1 trigger(s)") ||
		!strings.Contains(got, "viff_charter shows every charter") {
		t.Errorf("the install answered %q, want what went where", got)
	}
	if got := parts[1]; !strings.Contains(got, "ttl: it is removed at 2026-10-31T00:00:00Z") ||
		!strings.Contains(got, "package repos.r.old: uninstalled, not in the charters given") {
		t.Errorf("the pruning install answered %q, want its ttl and what the prune took", got)
	}
	if got := strings.TrimSpace(parts[2]); !strings.HasPrefix(got, "refused: the viff refused it: rule \"wide\"") {
		t.Errorf("a refused install answered %q, want viff's words", got)
	}
	if got := strings.TrimSpace(parts[3]); got != "removed the rule lift" {
		t.Errorf("the rule's removal answered %q", got)
	}
	if got := strings.TrimSpace(parts[4]); got != "uninstalled the charter at repos.r.bye; what it wrote is still there" {
		t.Errorf("the package's removal answered %q", got)
	}
	if got := strings.TrimSpace(parts[5]); !strings.Contains(got, "not installed: several charters are each installed at their own at:") {
		t.Errorf("several charters at one at answered %q, want it refused before it was carried", got)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"POST /charter?at=repos.r.hello", "POST /charter?prune=repos.r", "POST /charter?at=repos.r.wide",
		"DELETE /charter?path=repos.r.hello.rules.lift", "DELETE /charter?path=repos.r.bye"}
	if strings.Join(saw, "|") != strings.Join(want, "|") {
		t.Errorf("the runner carried %v, want %v", saw, want)
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "name: lift") || !strings.Contains(sent[0], "---\n") ||
		!strings.Contains(sent[1], "ttl: 72h") || !strings.Contains(sent[1], "at: repos.r.bye") {
		t.Errorf("the installs carried %q, want the rules and the charters one document apiece", sent)
	}
}
