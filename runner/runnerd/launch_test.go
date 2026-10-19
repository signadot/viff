package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// offering is a handler that offers the verse agent (with a model, a conversation, the steps it
// intercepts and the virtual-key annotation) and scripts, as verse-runner does.
func offering(t *testing.T) *httptest.Server {
	t.Helper()
	d := NewInProcessHandler(fakeBuild)
	d.Describe(runner.Description{Offers: []runner.Offer{
		{Agent: "verse", With: map[string]runner.Accepts{"model": runner.AcceptsAny, "network": everyNetwork},
			Annotations: map[string]string{"verse.portkey/virtual-key": "string"}, Converse: true,
			Systems: intercepted("files", "commands", "agent")},
		{Script: true, With: builds()},
	}})
	srv := httptest.NewServer(d.HTTPHandler())
	t.Cleanup(srv.Close)
	return srv
}

// everyNetwork is an offer's network that builds every mode.
var everyNetwork = runner.AcceptsOneOf(runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow)

// builds is an offer's with that builds every network and accepts nothing else.
func builds() map[string]runner.Accepts { return map[string]runner.Accepts{"network": everyNetwork} }

// intercepted is systems an offer intercepts, each allowing everything.
func intercepted(names ...string) map[string]runner.System {
	out := map[string]runner.System{}
	for _, n := range names {
		out[n] = runner.System{Ceiling: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}}}
	}
	return out
}

func postLaunch(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url+"/tasks", "application/tony", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A launch the runner can run starts a task: an agent it offers, and a script.
func TestALaunchItOffersRuns(t *testing.T) {
	srv := offering(t)
	for _, body := range []string{
		`{id: a1, doer: {agent: verse, with: {model: m, env: {BRANCH: main}}, annotations: {verse.portkey/virtual-key: k}}, prompt: go, policy: [{then: allow}], converse: true}`,
		`{id: s1, doer: {script: "echo hi"}}`,
		`{id: a2, doer: {agent: verse}, prompt: go, policy: [{name: m, match: {system: files, verb: write, path: "db/**"}, then: ask}, {name: c, match: {system: commands, verb: run, command: "git push*"}, then: deny}, {name: d, match: {system: agent, verb: call-sub-agent}, then: ask}, {then: allow}]}`,
	} {
		if code, msg := postLaunch(t, srv.URL, body); code != http.StatusCreated {
			t.Errorf("%s = %d %s, want 201", body, code, msg)
		}
	}
}

// What a runner can't run, or can't run yet, is refused with 400 and the reason, and nothing
// starts. Each case names what the refusal must say.
func TestALaunchItCannotHonourIsRefused(t *testing.T) {
	srv := offering(t)
	for _, tc := range []struct{ body, says string }{
		{`{prompt: go, driver: llm}`, "unknown field"},                           // the old launch
		{`{id: x, doer: {agent: verse}, prompt: go, frame: f}`, "unknown field"}, // a dropped field
		{`{doer: {agent: verse}, prompt: go}`, "id"},
		{`{id: x, doer: {agent: verse, script: "echo"}, prompt: go}`, "exclude"},
		{`{id: x, doer: {}}`, "agent"},
		{`{id: x, doer: {agent: verse}}`, "prompt"},
		{`{id: x, doer: {agent: claude}, prompt: go}`, "no agent \"claude\""},
		{`{id: x, doer: {script: "echo", with: {model: m}}}`, "model"},
		{`{id: x, doer: {agent: verse, with: {effort: high}}, prompt: go}`, "effort"},
		{`{id: x, doer: {agent: verse, annotations: {other: v}}, prompt: go}`, "other"},
		{`{id: x, doer: {script: "echo"}, converse: true}`, "conversation"},
		{`{id: x, doer: {script: "echo"}, policy: [{then: sometimes}]}`, "not one of deny, ask, allow"},
		{`{id: x, doer: {script: "echo", with: {env: {PATH: /x}}}}`, "PATH"},
		{`{id: x, doer: {script: "echo", with: {env: {VERSE_CREDENTIAL_FILE: /x}}}}`, "VERSE_CREDENTIAL_FILE"},
		{`{id: x, doer: {script: "echo", with: {env: {"1X": v}}}}`, "variable name"},
		{`{id: x, doer: {script: "echo"}, prompt: go}`, "a script takes no `prompt`"},
		{`{id: x, doer: {script: "echo"}, timeout: soon}`, "not a duration"},
		{`{id: x, doer: {script: "echo"}, timeout: "-1h"}`, "not a duration"},
		{`{id: x, doer: {agent: verse}, prompt: go, policy: [{name: w, match: {system: files, verb: write, repo: a/b}, then: ask}, {then: ask}]}`, "match.repo: not a param"},
		{`{id: x, doer: {script: "echo", with: {git: [{repo: a/b}]}}}`, "intercepts no `git`"},
		{`{id: x, doer: {script: "echo"}, policy: [{name: g, match: {system: commands, verb: run}, then: ask}, {then: allow}]}`, "intercepts no commands"},
		{`{id: x, doer: {script: "echo"}, policy: [{name: g, match: {system: commands, verb: run}, then: deny}, {then: allow}]}`, "intercepts no commands"},
		{`{id: x, doer: {agent: verse}, prompt: go, policy: [{name: g, match: {system: git, verb: push}, then: ask}, {then: ask}]}`, "intercepts no git, so there is nothing to ask"},
		{`{id: x, doer: {agent: verse}, prompt: go, policy: [{name: g, match: {system: network, verb: tcp-client}, then: ask}, {then: ask}]}`, "not a system"},
		{`{id: x, doer: {script: "echo"}, answer: {ok: !irtype true}}`, "answer"},
		{`{id: x, doer: {script: "echo"}, policy: [{name: s, match: {system: task, verb: start}, then: ask}, {then: ask}]}`, "not a system"},
		{`{id: x, doer: {script: "echo"}, policy: [{name: g, match: {system: files, verb: write, command: ls}, then: ask}, {then: ask}]}`, "match.command: not a param"},
		{`{id: x, doer: {script: "echo"}, policy: [{name: g, match: {system: files, verb: write}, then: ask}]}`, "ends with its default row"},
	} {
		code, msg := postLaunch(t, srv.URL, tc.body)
		if code != http.StatusBadRequest || !strings.Contains(msg, tc.says) {
			t.Errorf("%s = %d %q, want 400 saying %q", tc.body, code, msg, tc.says)
		}
	}
}

// A handler with no offers — a test's, an example's — checks the launch's shape only, so any
// agent name passes.
func TestAHandlerWithNoOffersChecksTheShapeOnly(t *testing.T) {
	srv := httptest.NewServer(NewInProcessHandler(fakeBuild).HTTPHandler())
	defer srv.Close()
	if code, msg := postLaunch(t, srv.URL, `{id: x, doer: {agent: anything}, prompt: go}`); code != http.StatusCreated {
		t.Errorf("= %d %s, want 201", code, msg)
	}
	if code, _ := postLaunch(t, srv.URL, `{id: y, doer: {agent: anything}}`); code != http.StatusBadRequest {
		t.Errorf("an agent with no prompt = %d, want 400", code)
	}
}

// Launch, the programmatic seam, refuses as POST /tasks does.
func TestLaunchChecksTheLaunch(t *testing.T) {
	d := NewInProcessHandler(fakeBuild)
	if _, err := d.Launch(runner.Launch{ID: "x", Doer: runner.Doer{Agent: "a"}}); err == nil {
		t.Fatal("an agent with no prompt was launched")
	}
	p, err := d.Launch(runner.Launch{ID: "y", Doer: runner.Doer{Agent: "a"}, Prompt: "go"})
	if err != nil || p == nil {
		t.Fatalf("Launch = %v, %v", p, err)
	}
}

// The variables a runner gives a task's shell are its own, whatever its configuration names:
// a launch can't set one of them.
func TestARunnersOwnVariablesAreRefused(t *testing.T) {
	d := NewInProcessHandler(fakeBuild)
	d.OwnEnv([]string{"PATH", "FOO"})
	srv := httptest.NewServer(d.HTTPHandler())
	defer srv.Close()
	for body, want := range map[string]int{
		`{id: a, doer: {script: "echo", with: {env: {FOO: x}}}}`:                   http.StatusBadRequest,
		`{id: b, doer: {script: "echo", with: {env: {VERSE_CREDENTIAL_FILE: x}}}}`: http.StatusBadRequest,
		`{id: c, doer: {script: "echo", with: {env: {BAR: x}}}}`:                   http.StatusCreated,
	} {
		if code, msg := postLaunch(t, srv.URL, body); code != want {
			t.Errorf("%s = %d %s, want %d", body, code, msg, want)
		}
	}
}

// A launch's repo is on a forge the runner's offer lists.
func TestARepoIsOnAForgeTheRunnerServes(t *testing.T) {
	allow, deny := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}
	desc := runner.Description{Offers: []runner.Offer{{Agent: "verse", With: builds(),
		Systems: map[string]runner.System{"git": {Ceiling: task.Policy{
			{Match: &task.Match{System: "git", Params: map[string]string{"repo": "github.com/**"}}, Then: allow},
			{Match: &task.Match{System: "git", Params: map[string]string{"repo": "git.example.com/**"}}, Then: allow},
			{Then: deny}}}}}}}
	for _, c := range []struct {
		repo runner.Repo
		says string // "" = accepted
	}{
		{runner.Repo{Repo: "acme/app"}, ""},
		{runner.Repo{Repo: "git.example.com/acme/app"}, ""},
		{runner.Repo{Repo: "gitlab.com/acme/app"}, "with.git[0]: this runner's ceiling refuses git on gitlab.com/acme/app"},
	} {
		l := &runner.Launch{ID: "x", Prompt: "p", Doer: runner.Doer{Agent: "verse", With: runner.With{Git: []runner.Repo{c.repo}}}}
		err := CheckLaunch(l, desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: refused: %v", c.repo.Repo, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want %q", c.repo.Repo, err, c.says)
		}
	}
}

// A script's repos are an agent's: it calls the same tools, so rows on the git and github steps
// decide its calls as they do the agent's.
func TestAScriptsReposMayBeWrittenAndGated(t *testing.T) {
	desc := runner.Description{Offers: []runner.Offer{{Script: true, With: builds(), Systems: intercepted("git", "github")}}}
	launch := func(r runner.Repo, rows ...task.Row) *runner.Launch {
		return &runner.Launch{ID: "x", Policy: append(task.Policy(rows), task.Row{Then: task.Then{Mode: task.ThenAsk}}),
			Doer: runner.Doer{Script: "echo", With: runner.With{Git: []runner.Repo{r}}}}
	}
	if err := CheckLaunch(launch(runner.Repo{Repo: "acme/app"}), desc, nil); err != nil {
		t.Errorf("refused for a script: %v", err)
	}
	for _, m := range []*task.Match{{System: "git", Verb: "push"}, {System: "github", Verb: "create-pr"}, {System: "github", Verb: "update-pr"}} {
		row := task.Row{Name: "g", Match: m, Then: task.Then{Mode: task.ThenAsk}}
		if err := CheckLaunch(launch(runner.Repo{Repo: "acme/app"}, row), desc, nil); err != nil {
			t.Errorf("a script's row on %s.%s: %v", m.System, m.Verb, err)
		}
		p := task.Policy{row, {Then: task.Then{Mode: task.ThenAsk}}}
		if err := CheckPolicy(p, p, "script", runner.With{}, desc); err != nil {
			t.Errorf("a running script's new row on %s.%s: %v", m.System, m.Verb, err)
		}
	}
}

// CONTAINMENT: a launch's policy opens nothing the runner's ceilings close. A row that allows
// or asks about a call the ceiling refuses is refused at the launch, naming both lines; a row
// naming no repo is held against each repository the launch checks out; a repository the git
// ceiling refuses every call on is refused as a fact; a row on a system the offer does not
// perform is refused; and a slide is checked the same way, with the task's repositories.
func TestALaunchsPolicyIsWithinTheRunnersCeilings(t *testing.T) {
	allow, deny, ask := task.Then{Mode: task.ThenAllow}, task.Then{Mode: task.ThenDeny}, task.Then{Mode: task.ThenAsk}
	desc := runner.Description{Offers: []runner.Offer{{Script: true, With: builds(),
		Systems: map[string]runner.System{
			"git": {Ceiling: task.Policy{
				{Name: "no-release", Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/release/**"}}, Then: deny},
				{Name: "ours", Match: &task.Match{System: "git", Params: map[string]string{"repo": "github.com/acme/**"}}, Then: allow},
				{Name: "nobody-elses", Match: &task.Match{System: "git"}, Then: deny},
			}},
			"github":   {Ceiling: task.Policy{{Then: allow}}},
			"commands": {Ceiling: task.Policy{{Then: allow}}},
		}}}}
	launch := func(repo string, rows ...task.Row) *runner.Launch {
		return &runner.Launch{ID: "x", Policy: append(task.Policy(rows), task.Row{Then: ask}),
			Doer: runner.Doer{Script: "echo", With: runner.With{Git: []runner.Repo{{Repo: repo}}}}}
	}
	push := func(then task.Then, params ...string) task.Row {
		m := &task.Match{System: "git", Verb: "push", Params: map[string]string{}}
		for i := 0; i+1 < len(params); i += 2 {
			m.Params[params[i]] = params[i+1]
		}
		return task.Row{Name: "r", Match: m, Then: then}
	}
	for _, c := range []struct {
		name string
		l    *runner.Launch
		says string // "" = accepted
	}{
		{"a push the ceiling allows", launch("acme/app", push(allow, "ref", "refs/heads/verse/auto/*")), ""},
		{"an ask about a push the ceiling allows", launch("acme/app", push(ask, "ref", "refs/heads/main")), ""},
		{"a push to a ref the ceiling refuses", launch("acme/app", push(allow, "ref", "refs/heads/release/1.0")), "its row no-release: deny verb=push ref=refs/heads/release/**"},
		{"a pattern wider than the ceiling's", launch("acme/app", push(allow, "ref", "refs/heads/**")), ""},
		{"an ask about a refused ref", launch("acme/app", push(ask, "ref", "refs/heads/release/1.0")), "policy[0]: this runner's ceiling refuses what the row would ask"},
		{"a deny row beyond the ceiling", launch("acme/app", push(deny, "ref", "refs/heads/release/1.0")), ""},
		{"a row on a repo the ceiling does not serve", launch("acme/app", push(allow, "repo", "github.com/other/app")), "its row nobody-elses"},
		{"a repo the ceiling refuses every call on", launch("other/app"), "with.git[0]: this runner's ceiling refuses git on github.com/other/app"},
		{"a row on a system the runner does not perform", launch("acme/app", task.Row{Match: &task.Match{System: "linear-issue", Verb: "create"}, Then: allow}), "intercepts no linear-issue"},
		{"a row on a system held inside the run", launch("acme/app", task.Row{Match: &task.Match{System: "commands", Verb: "run"}, Then: ask}), ""},
		{"a row on a performed system with no ceiling written", launch("acme/app", task.Row{Match: &task.Match{System: "github", Verb: "create-pr"}, Then: allow}), ""},
	} {
		err := CheckLaunch(c.l, desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want it refused saying %q", c.name, err, c.says)
		}
	}
	// The slide, with the task's repositories as they are.
	with := runner.With{Git: []runner.Repo{{Repo: "acme/app"}}}
	prev := task.Policy{push(ask), {Then: ask}}
	if err := CheckPolicy(task.Policy{push(allow, "ref", "refs/heads/release/2.0"), {Then: ask}}, prev, "script", with, desc); err == nil || !strings.Contains(err.Error(), "no-release") {
		t.Errorf("a slide past the ceiling: %v, want it refused naming the ceiling's row", err)
	}
	if err := CheckPolicy(task.Policy{push(allow, "ref", "refs/heads/verse/auto/*"), {Then: ask}}, prev, "script", with, desc); err != nil {
		t.Errorf("a slide within the ceiling: %v", err)
	}
}

// THE NETWORK IS A FACT OF THE RUN, ABSENT BEING NONE, and an offer says which networks its
// runner builds: a launch asking for one it does not build is refused where it is written, the
// absent one among them on a runner that cannot take the network away. An offer that names no
// network builds none a launch may ask for.
func TestTheNetworkIsAFactTheOfferBuilds(t *testing.T) {
	offer := func(modes ...string) runner.Description {
		return runner.Description{Offers: []runner.Offer{{Script: true, With: map[string]runner.Accepts{"network": runner.AcceptsOneOf(modes...)}}}}
	}
	sandboxed, unconfined := offer(runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow), offer(runner.NetworkOpen)
	launch := func(n *runner.Network) *runner.Launch {
		return &runner.Launch{ID: "x", Doer: runner.Doer{Script: "echo", With: runner.With{Network: n}}}
	}
	open, none := &runner.Network{Mode: runner.NetworkOpen}, &runner.Network{Mode: runner.NetworkNone}
	hosts := &runner.Network{Mode: runner.NetworkAllow, Allow: []string{"pypi.org"}}
	for _, c := range []struct {
		name string
		desc runner.Description
		n    *runner.Network
		says string
	}{
		{"sandboxed, absent", sandboxed, nil, ""},
		{"sandboxed, hosts", sandboxed, hosts, ""},
		{"unconfined, open", unconfined, open, ""},
		{"unconfined, absent", unconfined, nil, "builds the network open, not none (absent is none)"},
		{"unconfined, none", unconfined, none, "not none"},
		{"unconfined, hosts", unconfined, hosts, "not allow"},
		{"silent, absent", runner.Description{Offers: []runner.Offer{{Script: true}}}, nil, "not none"},
		{"silent, open", runner.Description{Offers: []runner.Offer{{Script: true}}}, open, "not open"},
	} {
		err := CheckLaunch(launch(c.n), c.desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want it refused saying %q", c.name, err, c.says)
		}
	}
}

// A TRACKER IS A FACT OF `git-issue` as a repository is of `git`: an offer that holds git and
// not git-issue refuses a launch naming a tracker, and one that holds both takes it.
func TestATrackerNeedsTheGitIssueSystem(t *testing.T) {
	l := runner.Launch{ID: "x", Doer: runner.Doer{Script: "echo", With: runner.With{
		Git: []runner.Repo{{Repo: "acme/app"}}, GitIssue: []runner.Tracker{{Repo: "acme/app"}}}}}
	gitOnly := runner.Description{Offers: []runner.Offer{{Script: true, With: builds(), Systems: intercepted("git")}}}
	if err := CheckOffer(&l, gitOnly); err == nil || !strings.Contains(err.Error(), "intercepts no `git-issue`") {
		t.Errorf("a tracker on an offer without git-issue: %v", err)
	}
	both := runner.Description{Offers: []runner.Offer{{Script: true, With: builds(), Systems: intercepted("git", "git-issue")}}}
	if err := CheckOffer(&l, both); err != nil {
		t.Errorf("a tracker on an offer with git-issue: %v", err)
	}
}
