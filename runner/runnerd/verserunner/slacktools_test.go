package verserunner

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd/delivery"
	"github.com/signadot/verse/task"
)

// slackAPI is a Slack Web API for the runner's calls: the bot is in #deploys and #general, a
// thread in #deploys has one reply, and what was posted is kept with the token it came with.
type slackAPI struct {
	mu     sync.Mutex
	posted []map[string]string // channel, thread_ts, text, auth
}

func (f *slackAPI) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		answer := func(v map[string]any) { v["ok"] = true; _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("Authorization") != "Bearer xoxb-runners-own" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		switch method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]; method {
		case "users.conversations":
			answer(map[string]any{"channels": []map[string]any{{"id": "C1", "name": "deploys"}, {"id": "C2", "name": "general"}}})
		case "chat.postMessage":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			in["auth"] = r.Header.Get("Authorization")
			f.posted = append(f.posted, in)
			answer(map[string]any{"channel": in["channel"], "ts": "1790000000.000200"})
		case "chat.getPermalink":
			answer(map[string]any{"permalink": "https://acme.slack.com/archives/" + r.URL.Query().Get("channel") + "/p1790000000000200"})
		case "conversations.replies":
			if r.URL.Query().Get("channel") != "C1" || r.URL.Query().Get("ts") != "1790000000.000100" {
				answer(map[string]any{"messages": []any{}})
				return
			}
			answer(map[string]any{"messages": []map[string]any{
				{"ts": "1790000000.000100", "user": "U1", "text": "shipping v1.3?", "reply_count": 1},
				{"ts": "1790000000.000150", "user": "U2", "text": "after the migration"},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "unknown_method"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// slackRunner is a verse runner that holds a Slack bot token and no repos, under the ceiling
// given.
func slackRunner(t *testing.T, api string, ceiling task.Policy) client.Launcher {
	t.Helper()
	return serveConfigured(t, Config{Systems: map[string]System{"slack": {Credential: "SLACK_BOT_TOKEN", Ceiling: ceiling}}},
		Secrets{}, Tokens{Slack: "xoxb-runners-own", SlackAPI: api})
}

// A SCRIPT POSTS TO A CHANNEL ITS POLICY ASKS ABOUT, AND READS A THREAD BACK, with the runner's
// bot token, which the script never holds. The question names the channel and shows the text;
// the approval has the runner post it, ending with the footer that names the task; a post to a
// channel the runner's ceiling closes is refused at the step, the script reading the refusal;
// and a launch whose policy would ask about posting there is refused at install, naming the
// policy's row and the ceiling's.
func TestAScriptPostsToSlackUnderAnAskingPolicy(t *testing.T) {
	withCallTool(t)
	api := &slackAPI{}
	ceiling := task.Policy{{Name: "deploys", Match: &task.Match{Params: map[string]string{"channel": "deploys"}}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: task.Then{Mode: task.ThenDeny}}}
	rt := slackRunner(t, api.serve(t), ceiling)
	script := "#!/bin/sh\n" +
		"verse-call-tool slack-post -channel '#deploys' -text 'v1.3 is rolling out'\necho \"exit=$?\"\n" +
		"verse-call-tool slack-post -channel general -text 'hello all'\necho \"exit=$?\"\n" +
		"verse-call-tool slack-read -channel deploys -thread 1790000000.000100\n" +
		"env | grep -c xoxb || true"
	rt.Launch(runner.Launch{ID: "s1", Doer: runner.Doer{Script: script, With: runner.With{Network: netOpen}}, Policy: task.Policy{
		{Name: "announce", Match: &task.Match{System: "slack", Verb: "post", Params: map[string]string{"channel": "deploys"}}, Then: task.Then{Mode: task.ThenAsk}},
		{Match: &task.Match{System: "slack"}, Then: task.Then{Mode: task.ThenAllow}},
		{Then: task.Then{Mode: task.ThenAllow}}}})
	q := pending(t, rt, "s1")
	if q.Step != task.StepSlackPost || !maps.Equal(q.Fields, map[string]string{"channel": "deploys"}) || q.Gate != "announce" {
		t.Fatalf("asked about %+v, want the post to deploys, by the row announce", q)
	}
	if err := rt.Attach("s1").(resolver).Resolve(q.ID, task.Proceed, "", "user:ana"); err != nil {
		t.Fatal(err)
	}
	snap := ended(t, rt, "s1")
	if snap.Status != task.Done {
		t.Fatalf("task: %+v", snap)
	}
	for _, want := range []string{
		"posted in #deploys: ts 1790000000.000200 https://acme.slack.com/archives/C1/p1790000000000200\nexit=0",
		"denied by ceiling: refused by the runner's ceiling, its row policy[1] for slack: slack.post channel=general\nexit=3",
		"#deploys, the thread 1790000000.000100: 2 message(s), newest last\n1790000000.000100 U1: shipping v1.3? [1 replies]\n1790000000.000150 U2: after the migration",
	} {
		if !strings.Contains(snap.Result, want) {
			t.Errorf("the script read:\n%s\nwant:\n%s", snap.Result, want)
		}
	}
	events, stop := rt.Attach("s1").SubscribeSince(0)
	defer stop()
	var asked []string
	for e := range events {
		if e.Kind == task.EventPending {
			asked = append(asked, e.Action)
		}
	}
	if len(asked) != 1 || asked[0] != "v1.3 is rolling out" {
		t.Errorf("the question showed %q, want the post's text", asked)
	}
	if !strings.HasSuffix(strings.TrimSpace(snap.Result), "0") {
		t.Errorf("the script's environment carries the token:\n%s", snap.Result)
	}
	if len(api.posted) != 1 || api.posted[0]["channel"] != "C1" || api.posted[0]["auth"] != "Bearer xoxb-runners-own" ||
		api.posted[0]["text"] != "v1.3 is rolling out\n\n"+delivery.SlackFooter("s1") {
		t.Errorf("Slack saw %v, want the one post in C1 with the task's footer", api.posted)
	}

	h := rt.Launch(runner.Launch{ID: "s2", Doer: runner.Doer{Script: "#!/bin/sh\necho hi", With: runner.With{Network: netOpen}}, Policy: task.Policy{
		{Name: "everyone", Match: &task.Match{System: "slack", Verb: "post", Params: map[string]string{"channel": "general"}}, Then: task.Then{Mode: task.ThenAsk}},
		{Then: task.Then{Mode: task.ThenAllow}}}})
	if err := h.Err(); !strings.Contains(err, "policy[1]") || !strings.Contains(err, "slack.post channel=general") || !strings.Contains(err, "its row policy[1]: deny everything") {
		t.Errorf("a policy asking about a channel the ceiling closes: %q", err)
	}
}

// EACH SLACK TOOL IS A VERB OF SLACK, a step whose fields are the channel's name and the verb's
// own: a reply's and a read's thread, a reaction's emoji. The channel is named as the policy
// matches it whichever way the call spelled it, and a call with no channel's name, or without
// what its verb requires, derives no fields: it is refused before any row decides it.
func TestASlackToolsStepNamesTheChannel(t *testing.T) {
	want := map[string]struct {
		step   string
		fields map[string]string
	}{
		slackPostTool:  {task.StepSlackPost, map[string]string{"channel": "deploys"}},
		slackReplyTool: {task.StepSlackReply, map[string]string{"channel": "deploys", "thread": "17.1"}},
		slackReadTool:  {task.StepSlackRead, map[string]string{"channel": "deploys", "thread": "17.1"}},
		slackReactTool: {task.StepSlackReact, map[string]string{"channel": "deploys", "emoji": "rocket"}},
	}
	tools := slackTools(&delivery.Client{})
	if len(tools) != len(want) {
		t.Fatalf("%d tools", len(tools))
	}
	for _, tl := range tools {
		w := want[tl.Name]
		if tl.Step != w.step {
			t.Errorf("%s is the step %q, want %q", tl.Name, tl.Step, w.step)
		}
		in := map[string]any{"channel": " #Deploys", "text": "t", "thread": "17.1", "message": "17.2", "emoji": ":rocket:"}
		if got, ok := tl.Fields(in); !ok || !maps.Equal(got, w.fields) {
			t.Errorf("%s derives %v (%v), want %v", tl.Name, got, ok, w.fields)
		}
		for _, bad := range []string{"", "#", "dé-ploys", "deploys now"} {
			if _, ok := tl.Fields(map[string]any{"channel": bad, "text": "t", "thread": "17.1", "message": "17.2", "emoji": "x"}); ok {
				t.Errorf("%s derives fields for the channel %q", tl.Name, bad)
			}
		}
	}
	if _, ok := tools[0].Fields(map[string]any{"channel": "deploys"}); ok {
		t.Error("a post with no text derives fields")
	}
	// A call with no fields is refused by the tool too, and never reaches the runner: the
	// client here has no connection to reach it by.
	for _, tl := range tools {
		out, blocked, err := tl.Do(t.Context(), task.ToolUse{Name: tl.Name, Input: map[string]any{"channel": "deploys now", "text": "t", "thread": "17.1", "message": "17.2", "emoji": "x"}})
		if err != nil || !blocked || !strings.HasPrefix(out, "not sent:") {
			t.Errorf("%s with no channel's name: %q, %v, %v", tl.Name, out, blocked, err)
		}
	}
}

// A RUNNER PERFORMS SLACK WHERE ITS CONFIGURATION NAMES THE BOT TOKEN'S VARIABLE, and every offer
// then holds the system; a `slack` section naming none is refused, saying what it
// needs, and a ceiling that asks is refused as every ceiling that asks is.
func TestARunnerPerformsSlackWithItsBotToken(t *testing.T) {
	c := Config{Systems: map[string]System{"slack": {Credential: "SLACK_BOT_TOKEN"}}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	for _, o := range c.Describe(Secrets{PortkeyAPIKey: "k"}, nil).Offers {
		if s, ok := o.Systems["slack"]; !ok || len(s.Ceiling) != 1 {
			t.Errorf("the offer %s/%v does not intercept slack, with its ceiling: %+v", o.Agent, o.Script, o.Systems["slack"])
		}
	}
	t.Setenv("SLACK_BOT_TOKEN", "")
	if _, unset := TokensFromEnv(c); len(unset) != 1 || unset[0] != "SLACK_BOT_TOKEN (slack)" {
		t.Errorf("unset: %v", unset)
	}
	none := Config{Systems: map[string]System{"slack": {}}}
	if err := none.Check(); err == nil || !strings.Contains(err.Error(), "Slack bot token") {
		t.Errorf("a slack section with no credential: %v", err)
	}
	asks := Config{Systems: map[string]System{"slack": {Credential: "K", Ceiling: task.Policy{{Then: task.Then{Mode: task.ThenAsk}}}}}}
	if err := asks.Check(); err == nil {
		t.Error("a ceiling that asks passed")
	}
	// A withhold in a ceiling would be refused at the launch and let through at the act.
	withholds := Config{Systems: map[string]System{"slack": {Credential: "K", Ceiling: task.Policy{
		{Match: &task.Match{Verb: "post"}, Then: task.Then{Mode: task.ThenWithhold}}}}}}
	if err := withholds.Check(); err == nil || !strings.Contains(err.Error(), "withholding is a task's policy's") {
		t.Errorf("a ceiling that withholds: %v", err)
	}
}
