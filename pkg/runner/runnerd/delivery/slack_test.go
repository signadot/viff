package delivery

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// slackAPI is a Slack Web API whose bot is in #deploys and #general, keeping each write it was
// asked for, with the token.
func slackAPI(t *testing.T, saw *[]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		answer := func(v map[string]any) { v["ok"] = true; _ = json.NewEncoder(w).Encode(v) }
		switch method {
		case "users.conversations":
			answer(map[string]any{"channels": []map[string]any{{"id": "C1", "name": "deploys"}, {"id": "C2", "name": "general"}}})
		case "chat.postMessage":
			*saw = append(*saw, r.Header.Get("Authorization")+" post "+in["channel"]+" "+in["thread_ts"]+" "+in["text"])
			answer(map[string]any{"channel": in["channel"], "ts": "17.2"})
		case "chat.getPermalink":
			answer(map[string]any{"permalink": "https://acme.slack.com/p17"})
		case "reactions.add":
			*saw = append(*saw, "react "+in["channel"]+" "+in["timestamp"]+" "+in["name"])
			answer(map[string]any{})
		case "conversations.history":
			answer(map[string]any{"messages": []map[string]any{{"ts": "17.1", "user": "U1", "text": "deploying"}}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A TASK'S MESSAGE IN SLACK ENDS WITH THE TASK'S NAME, posted with the runner's bot token; a reply
// lands in its thread, a reaction on its message, and a read answers the channel's messages. The
// policy's `deny` and the runner's ceiling are each read again here, on the channel's name, and
// a runner with no bot token says where one is configured.
func TestATasksSlackMessageNamesTheTask(t *testing.T) {
	var saw []string
	// The runner's ceiling gives #deploys alone.
	deploys := []systems.Shape{{"channel": ir.FromString("deploys")}}
	ceiling := systems.Ceiling{{Verbs: map[string][]systems.Shape{"post": deploys, "reply": deploys, "read": deploys, "react": deploys}}}
	s := &Server{Log: io.Discard, Services: Services{SlackToken: "xoxb-own", SlackAPI: slackAPI(t, &saw)}, Ceilings: map[string]systems.Ceiling{"slack": ceiling}}
	l := within(nil, "slack")
	l.Autonomy = task.Autonomy{
		{Name: "quiet", Match: &task.Match{System: "slack", Verb: "react"}, Then: task.Then{Mode: task.ThenDeny}},
		{Match: &task.Match{System: "slack"}, Then: task.Then{Mode: task.ThenAllow}}, {Then: task.Then{Mode: task.ThenAllow}}}
	tk := Task{ID: "t7", Workspace: &workspace.Workspace{}, Launch: l}

	if rep := s.answer(tk, frame(t, Request{Op: OpSlackPost, Channel: "#Deploys", Body: "v1.3 is out"})); !rep.OK || rep.Out != "posted in #deploys: ts 17.2 https://acme.slack.com/p17" {
		t.Errorf("post: %+v", rep)
	}
	// A post naming a thread is a reply the policy decided as a post: refused, and nothing sent.
	if rep := s.answer(tk, frame(t, Request{Op: OpSlackPost, Channel: "deploys", Thread: "17.2", Body: "in the thread"})); !strings.Contains(rep.Refused, "a post names no thread") || len(saw) != 1 {
		t.Errorf("a post with a thread: %+v (sent %v)", rep, saw)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpSlackReply, Channel: "deploys", Thread: "17.2", Body: "it holds"})); !rep.OK || !strings.HasPrefix(rep.Out, "replied in #deploys, in the thread 17.2") {
		t.Errorf("reply: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpSlackRead, Channel: "deploys"})); !rep.OK || rep.Out != "#deploys: 1 message(s), newest last\n17.1 U1: deploying" {
		t.Errorf("read: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpSlackPost, Channel: "general", Body: "hi"})); !strings.Contains(rep.Refused, "the runner's ceiling") {
		t.Errorf("a post the ceiling closes: %+v", rep)
	}
	if rep := s.answer(tk, frame(t, Request{Op: OpSlackReact, Channel: "deploys", Message: "17.2", Emoji: ":rocket:"})); !strings.Contains(rep.Refused, "the autonomy's row quiet") {
		t.Errorf("a reaction the policy denies: %+v", rep)
	}
	want := []string{
		"Bearer xoxb-own post C1  v1.3 is out\n\n_verse task t7_",
		"Bearer xoxb-own post C1 17.2 it holds\n\n_verse task t7_",
	}
	if strings.Join(saw, "|") != strings.Join(want, "|") {
		t.Errorf("Slack saw %q, want %q", saw, want)
	}

	free := &Server{Log: io.Discard, Services: s.Services, Ceilings: map[string]systems.Ceiling{"slack": systems.Open()}}
	if rep := free.answer(Task{ID: "t8", Launch: within(nil, "slack")}, frame(t, Request{Op: OpSlackReact, Channel: "deploys", Message: "17.2", Emoji: ":rocket:"})); !rep.OK || rep.Out != "reacted :rocket: to 17.2 in #deploys" {
		t.Errorf("react: %+v", rep)
	}
	if rep := free.answer(Task{ID: "t8", Launch: within(nil, "slack")}, frame(t, Request{Op: OpSlackPost, Channel: "C1", Body: "hi"})); !strings.Contains(rep.Refused, "no channel named c1") {
		t.Errorf("a channel's id: %+v", rep)
	}
	none := &Server{Log: io.Discard}
	if rep := none.answer(tk, frame(t, Request{Op: OpSlackPost, Channel: "deploys", Body: "x"})); !strings.Contains(rep.Error, "SLACK_BOT_TOKEN") {
		t.Errorf("a runner with no bot token: %+v", rep)
	}
}
