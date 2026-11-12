package stock

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd/delivery"
	"github.com/signadot/verse/pkg/runner/runnerd/mcpclient"
	"github.com/signadot/verse/pkg/runner/runnerd/verse"
	"github.com/signadot/verse/pkg/runner/runnerd/workspace"
	"github.com/signadot/verse/pkg/systems"
	"github.com/signadot/verse/pkg/task"
)

// stepInputs is a use of each stock tool that is a step, as a model would call it: one each
// tool reads to its step, and the runner reads to the same. A tool that is a step and has none
// here fails the test below, so a tool added is held to it.
//
// The tracker in the checkout (git-issue's create, comment and close) is written by the tool
// itself, as a commit is, and asks the runner nothing: those are in inTheCheckout instead.
var stepInputs = map[string]map[string]any{
	pushTool:         {"repo": "svc", "branch": "feature", "force": true},
	fetchTool:        {"repo": "svc"},
	createPRTool:     {"repo": "svc", "base": "main", "title": "a change", "body": "why", "draft": true},
	updatePRTool:     {"repo": "svc", "title": "a change"},
	listPRsTool:      {"repo": "svc"},
	readPRTool:       {"repo": "svc", "pr": "7"},
	commentPRTool:    {"repo": "svc", "pr": "7", "body": "looks right"},
	replyCommentTool: {"repo": "svc", "pr": "7", "comment": "4242", "body": "done"},

	"git-issue-pull":       {"repo": "svc"},
	"git-issue-push":       {"repo": "svc"},
	"github-issue-create":  {"repo": "svc", "title": "the roof leaks"},
	"github-issue-comment": {"repo": "svc", "issue": "7", "body": "still"},
	"github-issue-close":   {"repo": "svc", "issue": "7"},
	"linear-issue-create":  {"team": "eng", "title": "the roof leaks"},
	"linear-issue-comment": {"issue": "eng-7", "body": "still"},
	"linear-issue-close":   {"issue": "eng-7"},

	slackPostTool:  {"channel": " #Deploys", "text": "shipped"},
	slackReplyTool: {"channel": "deploys", "thread": "1700000000.000100", "text": "shipped"},
	slackReadTool:  {"channel": "deploys", "thread": "1700000000.000100"},
	slackReactTool: {"channel": "deploys", "message": "1700000000.000100", "emoji": ":eyes:"},

	"secret-exec": {"name": "npm-token", "argv": []any{"npm", "publish", "--tag", "a b"}, "dir": "./packages/a/", "env": map[string]any{"NODE_ENV": "production"}},

	// The tools of a server's list (mcpTestServer), as a doer's wire hands a use: every number
	// a float, a `null` a nil.
	"mcp-linear-get_issue": {"id": "ENG-7"},
	"mcp-linear-create_issue": {"team": "ENG", "title": "the roof leaks", "labels": []any{"bug", "roof"}, "estimate": float64(3), "assignee": nil,
		"draft": true, "meta": map[string]any{"ref": "007", "ratio": 0.5, "none": map[string]any{}, "list": []any{}}, "note": "héllo ☃"},
	"mcp-linear-delete_issue": {"id": "ENG-7"},
	"mcp-linear-with_input":   {"input": "x"},
	"mcp-linear-a_tool_whose_name_is_longer_than_claude_takes_once": {},
	"mcp-linear-any_args":   {"k": "v"},
	"mcp-linear-no_args":    {},
	"mcp-linear-set_fields": {"id": "ENG-7", "k": "v"},
	"mcp-linear-needs_more": {"id": "ENG-7", "team": "ENG"},

	"verse-ask-create":      {"name": "land", "question": "Land it?", "to": "user:ana@example.com, group:leads", "label": "team=payments", "on": "[{match: {verdict: proceed}, then: {spawn: {script: \"true\"}}}]"},
	"verse-ask-get":         {"name_or_id": "land"},
	"verse-ask-list":        {},
	"verse-data-get":        {"at": "acme.notes.n1"},
	"verse-data-put":        {"at": "acme.notes.n1", "value": "{stage: open}"},
	"verse-data-rm":         {"at": "acme.notes.n1"},
	"verse-charter-get":     {"path": "repos.r.hello"},
	"verse-charter-check":   {"at": "repos.r", "charters": "at: repos.r.hello\n"},
	"verse-charter-install": {"charters": "at: repos.r.hello\n"},
	"verse-charter-remove":  {"path": "repos.r.hello.rules.lift"},
}

// inTheCheckout is the stock tools that are a step and are performed in the task's checkout,
// by the tool, with no request to the runner: there is no step of the runner's to agree with.
var inTheCheckout = []string{"git-issue-create", "git-issue-comment", "git-issue-close"}

// EACH STOCK TOOL'S STEP IS THE STEP THE RUNNER READS FROM ITS REQUEST. The task asks about the
// tool's step, and the runner performs the act on an answer about the step it reads itself
// (delivery.Channel.answered): a tool that spells a field otherwise than the runner would have
// every answer to it refused as about another call. Each tool here is used under an autonomy
// that asks about everything, and the step the runner decides on is the tool's.
func TestEachToolsStepIsTheRunners(t *testing.T) {
	forge := t.TempDir()
	seed(t, forge, "acme/svc")
	env := systems.Environment{Systems: systems.Systems{
		"git":    {{Attributes: map[string]string{"repo": "acme/svc"}, Verbs: everyVerb("git")}},
		"secret": withSecret(t).Systems["secret"],
	}}
	for _, name := range []string{"github", "github-issue", "git-issue"} {
		env.Systems[name] = []systems.Block{{Attributes: map[string]string{"repo": "acme/svc"}, Verbs: everyVerb(name)}}
	}
	for _, name := range []string{"linear-issue", "slack", "verse"} {
		env.Systems[name] = []systems.Block{{Verbs: everyVerb(name)}}
	}
	env.Systems["mcp"] = []systems.Block{{Attributes: map[string]string{"server": "linear"}, Verbs: everyVerb("mcp")}}
	mcpURL, _ := mcpTestServer(t, "")
	l := &runner.Launch{ID: "t1", Prompt: "p", Environment: env, Autonomy: task.Autonomy{{Name: "every", Then: task.Then{Mode: task.ThenAsk}}}}
	repos, err := workspace.FromLaunch(l.Repos(), forge)
	if err != nil {
		t.Fatal(err)
	}
	mirrors := workspace.NewMirrors(t.TempDir(), workspace.TokenAuth(""))
	ws, err := mirrors.Open(t.Context(), "t1", repos, t.TempDir(), workspace.Author{})
	if err != nil {
		t.Fatal(err)
	}
	ceilings := map[string]systems.Ceiling{}
	for _, d := range systems.All() {
		ceilings[d.Name] = systems.Open()
	}
	ceilings["secret"] = systems.Ceiling{{Verbs: map[string][]systems.Shape{"exec": anyVars(t)}}}
	s := &delivery.Server{Mirrors: mirrors, Log: io.Discard, Ceilings: ceilings, HTTP: &http.Client{Transport: unreached{t}},
		Services: delivery.Services{LinearAuth: delivery.Holds("a-key"), SlackToken: delivery.Holds("xoxb-a-token"),
			GitHubAPI: "https://api.github.invalid",
			MCP:       mcpclient.Servers{"linear": {URL: mcpURL, Timeout: 5 * time.Second}},
			Verse: func(_ context.Context, _, method, path string, _ []byte) (int, string, error) {
				t.Errorf("a request reached verse: %s %s", method, path)
				return http.StatusInternalServerError, "", nil
			}}}
	var mu sync.Mutex
	var decided []task.Step
	near, far, err := delivery.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		s.Serve(delivery.Task{ID: "t1", Workspace: ws, Root: ws.Root, Launch: l,
			Credential: func(workspace.Repo) (string, error) { return "the-runners-own", nil },
			Answered: func(st task.Step, _ string) (string, []string, bool) {
				mu.Lock()
				defer mu.Unlock()
				decided = append(decided, st)
				return "", nil, false
			}}, near)
	}()
	conn, err := net.FileConn(far)
	far.Close()
	if err != nil {
		t.Fatal(err)
	}
	cl := delivery.NewClient(conn)
	defer func() { cl.Close(); <-served }()

	tools := append(repoTools(cl, ws, true), issueTools(cl, ws, l.Trackers(), true, true)...)
	secret, _ := secretTools(cl, ws, env)
	tools = append(append(tools, slackTools(cl)...), secret...)
	tools = append(append(append(tools, verseTools(cl)...), verseDataTools(cl)...), verseCharterTools(cl)...)
	// The tools of a server's list, as the task builds them, for a doer that takes any name.
	tools = append(tools, mcpTools(cl, mcpLists(cl, []string{"linear"}, env), nil, io.Discard)...)
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Step == "" || slices.Contains(inTheCheckout, tl.Name) {
			continue
		}
		seen[tl.Name] = true
		// A stock tool's name is its step's (git-push, git.push): the tools are filtered by their
		// step (verse.Tool.StepName), and one whose name said another step would read otherwise
		// wherever a name is read. An MCP tool's name is `mcp-<server>-<tool>`, its step's system
		// first, and its Step says the rest.
		if tl.Step == task.StepMCPCall {
			if !strings.HasPrefix(tl.Name, "mcp-linear-") {
				t.Errorf("%s is the step %s, and its name is not mcp-<server>-<tool>", tl.Name, tl.Step)
			}
		} else if system, verb, ok := systems.SplitTool(tl.Name); !ok || systems.StepName(system, verb) != tl.Step {
			t.Errorf("%s is the step %s, and its name is not that step's", tl.Name, tl.Step)
		}
		in, ok := stepInputs[tl.Name]
		if !ok {
			t.Errorf("%s is the step %s, and stepInputs has no use of it", tl.Name, tl.Step)
			continue
		}
		in = asTaken(tl.Params, in)
		st, ok := toolStep(tl, in)
		if !ok {
			t.Errorf("%s: %v reads to no step", tl.Name, in)
			continue
		}
		mu.Lock()
		decided = nil
		mu.Unlock()
		out, _, err := tl.Do(t.Context(), task.ToolUse{Name: tl.Name, Input: in, Decision: "t1~d1"})
		mu.Lock()
		got := slices.Clone(decided)
		mu.Unlock()
		switch {
		case err != nil:
			t.Errorf("%s: %v", tl.Name, err)
		case len(got) != 1:
			t.Errorf("%s: the runner decided %d step(s), want the one: %q", tl.Name, len(got), out)
		case !got[0].Same(st):
			t.Errorf("%s: the tool's step is\n\t%s\nand the runner's\n\t%s\nwhich differ in %s", tl.Name, st, got[0], strings.Join(st.Differs(got[0]), ", "))
		}
	}
	for name := range stepInputs {
		if !seen[name] {
			t.Errorf("stepInputs has a use of %s, which is no stock tool that is a step", name)
		}
	}
}

// toolStep is the step a use of tl is, as the loop works it out (verse's stepOf).
func toolStep(tl verse.Tool, in map[string]any) (task.Step, bool) {
	if tl.Build != nil {
		return tl.Build(in)
	}
	fields, ok := tl.Fields(in)
	if !ok {
		return task.Step{}, false
	}
	return task.NewStep(tl.Step, fields), true
}

// unreached fails a test that reaches a service: a call made where no act may run.
type unreached struct{ t *testing.T }

func (u unreached) RoundTrip(r *http.Request) (*http.Response, error) {
	u.t.Errorf("a call reached %s %s", r.Method, r.URL)
	return nil, io.ErrUnexpectedEOF
}
