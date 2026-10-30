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

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd/verse"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// A SCRIPT RAISES AN ASK FOR ITS OWNER, and the runner carries it to verse: the raiser is handed
// the task and the ask as the script wrote it, its due a date where the script gave a duration,
// and the script reads the id verse answered, then reads the ask back and lists its owner's asks.
// A runner that reaches no verse refuses a launch that names the verse system.
func TestAScriptRaisesAnAskTheRunnerCarriesToVerse(t *testing.T) {
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
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["verse"] = []systems.Block{{}}
	script := "#!/bin/sh\n" +
		"verse-call-tool verse-ask-create -name r1 -question 'Land it?' -to 'user:alice@example.com, group:leads' -label team=payments,kind=deploy -due ' 48h ' -then '{spawn: {script: \"true\"}}'\n" +
		"verse-call-tool verse-ask-get -name_or_id r1-1a2b\n" +
		"verse-call-tool verse-ask-list\n"
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
	// An ask the script names nothing is raised all the same, and verse names it, as
	// verse_ask_create and `verse ask -q` leave it to verse: the task's id is not its name.
	rt.Launch(runner.Launch{ID: "r3", Script: "#!/bin/sh\nverse-call-tool verse-ask-create -question 'Ship it?'\n",
		Environment: env, Autonomy: allow})
	if snap := ended(t, rt, "r3"); snap.Status != task.Done || !strings.Contains(snap.Result, "raised question run-1a2b: Ship it?") {
		t.Errorf("a raise naming no ask read %q (%s), want it raised under verse's name", snap.Result, snap.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saw) != 2 {
		t.Fatalf("the raiser was handed %d asks", len(saw))
	}
	if name, _ := saw[1]["name"].(string); name != "" {
		t.Errorf("an ask the script named nothing was handed to verse as %v, want no name", saw[1])
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

// THE TOOL TAKES WHAT verse_ask_create TAKES, read as `verse ask` reads it (api.NewAsk): `to` a
// comma-separated list, `then` and `else` tony documents, `label` name=value pairs, each a string.
// What the CLI refuses, the tool refuses, and a parameter that is not a string is refused rather
// than read as nothing.
func TestTheAskCreateToolTakesWhatTheMCPToolTakes(t *testing.T) {
	var schema map[string]any
	for _, tool := range verseTools(nil) {
		if tool.Name == askCreateTool {
			schema = tool.Params
		}
	}
	read := func(in map[string]any) (api.Ask, error) {
		p, err := paramsOf(in, schema)
		if err != nil {
			return api.Ask{}, err
		}
		return api.NewAsk(api.AskSpec{Question: "q", To: p.words["to"], Label: p.words["label"]}, time.Now())
	}
	if _, err := read(map[string]any{"questoin": "q"}); err == nil || !strings.Contains(err.Error(), "questoin is not a parameter") {
		t.Errorf("a parameter the tool does not take = %v, want it refused by name", err)
	}
	ask, err := read(map[string]any{"to": "user:ana@example.com, group:leads", "label": "team=payments, kind=deploy"})
	if err != nil || len(ask.To) != 2 || ask.To[1] != "group:leads" || ask.Labels["kind"] != "deploy" || len(ask.Labels) != 2 {
		t.Errorf("read as %+v, %v", ask, err)
	}
	if doc, err := docOf("then", `{spawn: {script: "true"}}`); err != nil || entity.Get(doc, "spawn") == nil {
		t.Errorf("then = %v, %v", doc, err)
	}
	if _, err := docOf("then", "spawn it"); err == nil {
		t.Error("a then that is no document was taken")
	}
	for _, bad := range []map[string]any{
		{"label": "team"}, {"to": "user:ana@example.com,,group:leads"},
		{"to": []any{"user:ana@example.com"}}, {"label": map[string]any{"team": "payments"}},
	} {
		if _, err := read(bad); err == nil {
			t.Errorf("%v was taken, where verse_ask_create refuses it", bad)
		}
	}
}

// A SCRIPT READS AND WRITES THE VERSE FOR ITS OWNER, and the runner carries each act on its
// route: the write as the PutEntity `verse data --put` sends, with its condition; the read with
// the query `verse data` sends, trimmed as verse_data trims it, or listed one kinded path per
// line; the removal said as `verse data --rm` says it.
func TestAScriptReadsAndWritesTheVerseTheRunnerCarries(t *testing.T) {
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
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["verse"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"verse-call-tool verse-data-put -at acme.notes.n1 -value '{stage: open}' -create\n" +
		"echo ---\n" +
		"verse-call-tool verse-data-get -at acme.notes.n1 -trim '{stage}'\n" +
		"echo ---\n" +
		"verse-call-tool verse-data-get -at acme.notes -paths -depth 1\n" +
		"echo ---\n" +
		"verse-call-tool verse-data-rm -at acme.notes.n1\n"
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

// THE DATA TOOLS TAKE WHAT verse_data AND ITS KIN TAKE, typed as they type it: a string, a
// boolean, an integer however the codec carried it. A parameter of another type, or one the tool
// does not take, is refused rather than read as nothing.
func TestTheDataToolsTakeWhatTheMCPToolsTake(t *testing.T) {
	schema := map[string]any{
		"at":    map[string]any{"type": "string"},
		"paths": map[string]any{"type": "boolean"},
		"depth": map[string]any{"type": "integer"},
	}
	p, err := paramsOf(map[string]any{"at": "acme.notes", "paths": true, "depth": int64(2), "if": nil}, schema)
	if err != nil || p.words["at"] != "acme.notes" || !p.flags["paths"] || p.nums["depth"] != 2 {
		t.Errorf("read as %+v, %v", p, err)
	}
	if p, err := paramsOf(map[string]any{"depth": float64(1)}, schema); err != nil || p.nums["depth"] != 1 {
		t.Errorf("a whole float read as %+v, %v", p, err)
	}
	for _, bad := range []map[string]any{
		{"paths": "yes"}, {"depth": 1.5}, {"at": 7}, {"bogus": "x"},
	} {
		if _, err := paramsOf(bad, schema); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}

	// What the grammar refuses, a tool says naming its own parameter, before anything is carried.
	tools := map[string]verse.Tool{}
	for _, tl := range verseDataTools(nil) {
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
	} {
		out, isErr, err := tools[c.tool].Do(context.Background(), task.ToolUse{Input: c.in})
		if err != nil || !isErr || !strings.HasPrefix(out, c.want) {
			t.Errorf("%s %v = %q, %v, %v, want %q", c.tool, c.in, out, isErr, err, c.want)
		}
	}
}

// A SCRIPT READS ITS OWNER'S CHARTERS AND CHECKS ITS OWN, and the runner carries each on its
// route: a charter reads as its file with the notes the file cannot say, as verse_charter answers
// it; a listing one kinded path per rule and level; a check carries the charters, one document
// apiece, and answers verse's verdict.
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
			return 200, "{writes: [repos.r.hello.*], rules: [{name: lift, held: high, condition: {subject: {path: repos.r.hello.low.*}}}]}", nil
		case method == http.MethodPost && path == "/charter/check?at=repos.r":
			checked = string(body)
			return 200, "{verdict: installs}", nil
		}
		return 404, method + " " + path, nil
	}
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["verse"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"verse-call-tool verse-charter-get -path repos.r.hello\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-get -path repos.r -depth 1 -paths\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-check -at repos.r -charters 'at: repos.r.hello\n---\nat: repos.r.bye'\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-get -path repos.r.hello -trim '{at: null}'\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-get -path repos.r.hello -if '{at: !glob [1]}' || true\n"
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
	if !strings.Contains(file, "at: repos.r.hello") || !strings.Contains(file, "name: lift") || strings.Contains(file, "held: high") ||
		!strings.Contains(file, "note: repos.r.hello: rule lift is held: it waits for stratum high") {
		t.Errorf("the charter read as %q, want its file and the note on the held rule", file)
	}
	if got := strings.TrimSpace(parts[1]); got != "repos.r.hello.rules.lift\nrepos.r.hello.strata.low" {
		t.Errorf("the listing answered %q, want one kinded path per rule and level", got)
	}
	if !strings.Contains(parts[2], "verdict: installs") {
		t.Errorf("the check answered %q, want verse's verdict", parts[2])
	}
	if trimmed := parts[3]; !strings.Contains(trimmed, "at: repos.r.hello") || strings.Contains(trimmed, "name: lift") {
		t.Errorf("the trimmed charter read as %q, want its at: alone", trimmed)
	}
	// A pattern that does not apply is said, as `verse charter --if` says it, never an answer
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
// install sends the charters as `verse charter -a` does, a rule list at its address or each
// charter at its own at: with a prune, and answers what went where as verse_charter_install says
// it; a removal says what went; what verse refuses, the script reads in verse's words.
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
	c := Config{Script: &Script{Systems: map[string]System{"verse": {Ceiling: systems.Open()}}}}
	env := envOf(netOpen, nil, nil)
	env.Systems["verse"] = []systems.Block{{}}
	script := "#!/bin/sh\nset -e\n" +
		"verse-call-tool verse-charter-install -at repos.r.hello -charters '[{name: lift, condition: {subject: repos.r.hello.*}, action: {patch: {seen: true}}}, {name: drop, condition: {subject: repos.r.hello.*}, action: {patch: {seen: false}}}]'\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-install -at repos.r -prune -charters 'at: repos.r.hello\nttl: 72h\n---\nat: repos.r.bye'\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-install -at repos.r.wide -charters '[{name: wide}]' || true\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-remove -path repos.r.hello.rules.lift\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-remove -path repos.r.bye\n" +
		"echo ---\n" +
		"verse-call-tool verse-charter-install -at repos.r.a -charters 'at: repos.r.a\n---\nat: repos.r.b' || true\n"
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
		!strings.Contains(got, "verse_charter shows every charter") {
		t.Errorf("the install answered %q, want what went where", got)
	}
	if got := parts[1]; !strings.Contains(got, "ttl: it is removed at 2026-10-31T00:00:00Z") ||
		!strings.Contains(got, "package repos.r.old: uninstalled, not in the charters given") {
		t.Errorf("the pruning install answered %q, want its ttl and what the prune took", got)
	}
	if got := strings.TrimSpace(parts[2]); !strings.HasPrefix(got, "refused: the verse refused it: rule \"wide\"") {
		t.Errorf("a refused install answered %q, want verse's words", got)
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
