package runnerd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// gitEnv is an environment that checks out repos, fetching each.
func gitEnv(repos ...string) systems.Environment {
	e := systems.Environment{Systems: systems.Systems{}}
	for _, r := range repos {
		e.Systems["git"] = append(e.Systems["git"], systems.Block{Attributes: map[string]string{"repo": r},
			Verbs: map[string][]systems.Shape{"fetch": {{}}}})
	}
	return e
}

// reachEnv is an environment checking out repo with its fetch and any push, and giving every verb
// of each of the other named systems (in repo's block, for one a repo names its blocks by): what a
// run may do is then its rows' and the ceilings', which is what the tests using it look at.
func reachEnv(repo string, names ...string) systems.Environment {
	e := gitEnv(repo)
	e.Systems["git"][0].Verbs["push"] = []systems.Shape{{}}
	for _, name := range names {
		d, _ := systems.Get(name)
		verbs := map[string][]systems.Shape{}
		for _, v := range d.Verbs {
			verbs[v.Name] = []systems.Shape{{}}
		}
		b := systems.Block{Verbs: verbs}
		if u, ok := d.Unique(); ok {
			b.Attributes = map[string]string{u.Name: repo}
		}
		e.Systems[name] = []systems.Block{b}
	}
	return e
}

// netEnv is an environment whose network is mode, under allow reaching hosts.
func netEnv(mode string, hosts ...string) systems.Environment {
	b := systems.Block{Attributes: map[string]string{"mode": mode}}
	for _, h := range hosts {
		if b.Verbs == nil {
			b.Verbs = map[string][]systems.Shape{}
		}
		b.Verbs["tcp-client"] = append(b.Verbs["tcp-client"], systems.Shape{"host": ir.FromString(h)})
	}
	return systems.Environment{Systems: systems.Systems{"network": {b}}}
}

// offering is a handler that offers the verse agent (with a model, a conversation, the steps it
// intercepts and the virtual-key annotation) and scripts, as verse-runner does.
func offering(t *testing.T) *httptest.Server {
	t.Helper()
	d := NewInProcessHandler(fakeBuild)
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{
		{Agent: &runner.AgentOffer{Name: "verse", Settings: map[string]runner.Setting{"model": {}},
			Annotations: map[string]string{"verse.portkey/virtual-key": "string"}}, Converse: true,
			Systems: withNet(intercepted("files", "commands", "agent"))},
		{Script: true, Systems: withNet(nil)},
	}})
	srv := httptest.NewServer(d.HTTPHandler())
	t.Cleanup(srv.Close)
	return srv
}

// builds is the network an offer holds, building the modes given, or every mode, with no ceiling.
func builds(modes ...string) runner.System {
	if len(modes) == 0 {
		modes = []string{runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow}
	}
	b := systems.CeilingBlock{Attributes: map[string]*ir.Node{"mode": systems.OneOf(modes...)}}
	if slices.Contains(modes, runner.NetworkAllow) {
		b.Verbs = map[string][]systems.Shape{"tcp-client": {{}}} // allow builds a connection to any host
	}
	return runner.System{Builds: systems.Ceiling{b}}
}

// withNet is sys with the network an offer holds, building every mode (builds), where sys does
// not hold it already.
func withNet(sys map[string]runner.System) map[string]runner.System {
	out := map[string]runner.System{}
	for k, v := range sys {
		out[k] = v
	}
	if _, held := out["network"]; !held {
		out["network"] = builds()
	}
	return out
}

// agentOffer is the verse agent's offer, holding sys and the network, taking a model.
func agentOffer(sys map[string]runner.System) runner.Offer {
	return runner.Offer{Agent: &runner.AgentOffer{Name: "verse", Settings: map[string]runner.Setting{"model": {}}}, Systems: withNet(sys)}
}

// described is a description of the offers given, at this verse's version.
func described(offers ...runner.Offer) runner.Description {
	return runner.Description{Version: runner.DescriptionVersion, Offers: offers}
}

// intercepted is systems an offer intercepts, each under an open ceiling (systems.Open).
func intercepted(names ...string) map[string]runner.System {
	out := map[string]runner.System{}
	for _, n := range names {
		out[n] = runner.System{Ceiling: systems.Open()}
	}
	return out
}

// ceiling reads a ceiling as a runner's config writes it.
func ceiling(t *testing.T, text string) systems.Ceiling {
	t.Helper()
	var c systems.Ceiling
	if err := codec.Unmarshal([]byte(text), &c, codec.Tony); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return c
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
		`{id: a1, agent: {name: verse, model: m, annotations: {verse.portkey/virtual-key: k}}, environment: {vars: {BRANCH: main}}, prompt: go, autonomy: [{then: allow}], converse: true}`,
		`{id: s1, script: "echo hi"}`,
		`{id: a2, agent: {name: verse}, prompt: go, environment: {systems: {files: {write: {}}, commands: {run: {}}, agent: {call-sub-agent: {}}}}, autonomy: [{name: m, match: {system: files, verb: write, path: !regexp "^db/"}, then: ask}, {name: c, match: {system: commands, verb: run, command: !regexp "^git push"}, then: deny}, {name: d, match: {system: agent, verb: call-sub-agent}, then: ask}, {then: allow}]}`,
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
		{`{id: x, agent: {name: verse}, prompt: go, frame: f}`, "unknown field"}, // a dropped field
		{`{id: x, doer: {agent: verse}, prompt: go}`, "unknown field"},           // the doer, gone
		{`{id: x, prompt: go, policy: [{then: allow}]}`, "unknown field"},        // the policy, now autonomy
		{`{agent: {name: verse}, prompt: go}`, "id"},
		{`{id: x, script: "echo", prompt: go}`, "exclude"},
		{`{id: x, agent: {name: verse}, script: "echo"}`, "a script takes no `agent`"},
		{`{id: x, agent: {name: verse, model: m}, script: "echo"}`, "a script takes no `agent`"},
		{`{id: x, person: "user:ana@example.com", prompt: go}`, "no runner takes a person yet"},
		{`{id: x, agent: {name: verse}}`, "prompt"},
		{`{id: x, agent: {name: claude}, prompt: go}`, "no agent \"claude\""},
		{`{id: x, prompt: go}`, "names its agent"}, // verse binds its default agent; a runner keeps none
		{`{id: x, agent: {name: verse, effort: high}, prompt: go}`, "effort"},
		{`{id: x, agent: {name: verse, annotations: {other: v}}, prompt: go}`, "other"},
		{`{id: x, script: "echo", converse: true}`, "conversation"},
		{`{id: x, script: "echo", autonomy: [{then: sometimes}]}`, "not one of deny, ask, allow"},
		{`{id: x, script: "echo", environment: {vars: {PATH: /x}}}`, "PATH"},
		{`{id: x, script: "echo", environment: {vars: {VERSE_CREDENTIAL_FILE: /x}}}`, "VERSE_CREDENTIAL_FILE"},
		{`{id: x, script: "echo", environment: {vars: {"1X": v}}}`, "a variable's name"},
		{`{id: x, script: "echo", environment: {systems: {pager: {page: {}}}}}`, "not a system"},
		{`{id: x, script: "echo", timeout: soon}`, "not a duration"},
		{`{id: x, script: "echo", timeout: "-1h"}`, "not a duration"},
		{`{id: x, agent: {name: verse}, prompt: go, autonomy: [{name: w, match: {system: files, verb: write, repo: a/b}, then: ask}, {then: ask}]}`, "match.repo: not a field"},
		{`{id: x, script: "echo", environment: {systems: {git: {repo: a/b, fetch: {}}}}}`, "holds no `git`"},
		// A row matching nothing the environment gives never decides, and is refused where it is
		// written; a system the environment gives that the offer does not hold is refused too.
		{`{id: x, script: "echo", environment: {systems: {commands: {run: {}}}}}`, "holds no `commands`"},
		{`{id: x, script: "echo", answer: {ok: !irtype true}}`, "answer"},
		{`{id: x, script: "echo", autonomy: [{name: s, match: {system: task, verb: start}, then: ask}, {then: ask}]}`, "not a system"},
		{`{id: x, script: "echo", autonomy: [{name: g, match: {system: files, verb: write, command: ls}, then: ask}, {then: ask}]}`, "match.command: not a field"},
		{`{id: x, script: "echo", autonomy: [{name: g, match: {system: files, verb: write}, then: ask}]}`, "ends with its default row"},
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
	if code, msg := postLaunch(t, srv.URL, `{id: x, agent: {name: anything}, prompt: go, environment: {systems: {network: {mode: open}}}}`); code != http.StatusCreated {
		t.Errorf("= %d %s, want 201", code, msg)
	}
	if code, _ := postLaunch(t, srv.URL, `{id: y, agent: {name: anything}}`); code != http.StatusBadRequest {
		t.Errorf("an agent with no prompt = %d, want 400", code)
	}
}

// A HANDLER WITH NO OFFERS REFUSES A LAUNCH NAMING NO NETWORK: no network block is the network
// none, which a handler running tasks in its own process can't build, and with no offers there is
// nothing else to refuse it. An open network runs.
func TestAHandlerWithNoOffersRefusesAnAbsentNetwork(t *testing.T) {
	srv := httptest.NewServer(NewInProcessHandler(fakeBuild).HTTPHandler())
	defer srv.Close()
	if code, msg := postLaunch(t, srv.URL, `{id: none, agent: {name: anything}, prompt: go}`); code != http.StatusBadRequest ||
		!strings.Contains(msg, "can't take the network away from them; only open") {
		t.Errorf("no network block = %d %q, want 400 refusing it", code, msg)
	}
	if code, msg := postLaunch(t, srv.URL, `{id: open, agent: {name: anything}, prompt: go, environment: {systems: {network: {mode: open}}}}`); code != http.StatusCreated {
		t.Errorf("an open network = %d %q, want 201", code, msg)
	}
}

// Launch, the programmatic seam, refuses as POST /tasks does.
func TestLaunchChecksTheLaunch(t *testing.T) {
	d := NewInProcessHandler(fakeBuild)
	if _, err := d.Launch(runner.Launch{ID: "x", Agent: &runner.Agent{Name: "a"}}); err == nil {
		t.Fatal("an agent with no prompt was launched")
	}
	p, err := d.Launch(runner.Launch{ID: "y", Agent: &runner.Agent{Name: "a"}, Prompt: "go", Environment: netEnv(runner.NetworkOpen)})
	if err != nil || p == nil {
		t.Fatalf("Launch = %v, %v", p, err)
	}
}

// The variables a runner gives a task's shell are its own, whatever its configuration names:
// a launch can't set one of them.
func TestARunnersOwnVariablesAreRefused(t *testing.T) {
	d := NewInProcessHandler(fakeBuild)
	d.OwnEnv(func(*runner.Launch) []string { return []string{"PATH", "FOO"} })
	srv := httptest.NewServer(d.HTTPHandler())
	defer srv.Close()
	for body, want := range map[string]int{
		`{id: a, script: "echo", environment: {vars: {FOO: x}}}`:                                   http.StatusBadRequest,
		`{id: b, script: "echo", environment: {vars: {VERSE_CREDENTIAL_FILE: x}}}`:                 http.StatusBadRequest,
		`{id: c, script: "echo", environment: {vars: {BAR: x}, systems: {network: {mode: open}}}}`: http.StatusCreated,
	} {
		if code, msg := postLaunch(t, srv.URL, body); code != want {
			t.Errorf("%s = %d %s, want %d", body, code, msg, want)
		}
	}
}

// A launch's repo is on a forge the runner's ceiling lists.
func TestARepoIsOnAForgeTheRunnerServes(t *testing.T) {
	desc := runner.Description{Offers: []runner.Offer{{Agent: &runner.AgentOffer{Name: "verse"},
		Systems: map[string]runner.System{"git": {Ceiling: ceiling(t, `{repo: !or [!regexp "^github\\.com/", !regexp "^git\\.example\\.com/"], fetch: {}}`)}}}}}
	for _, c := range []struct {
		repo runner.Repo
		says string // "" = accepted
	}{
		{runner.Repo{Repo: "acme/app"}, ""},
		{runner.Repo{Repo: "git.example.com/acme/app"}, ""},
		{runner.Repo{Repo: "gitlab.com/acme/app"}, "environment.systems.git: beyond this runner's ceiling for git: repo gitlab.com/acme/app is beyond !or [!regexp ^github\\.com/, !regexp ^git\\.example\\.com/]"},
	} {
		l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: "verse"}, Environment: gitEnv(c.repo.Repo)}
		err := CheckLaunch(l, desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: refused: %v", c.repo.Repo, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want %q", c.repo.Repo, err, c.says)
		}
	}
}

// A VALUE A FIRING BOUND IS HELD AS WRITTEN: payload text with a `$` in it is not read as a
// binding still to come, so a branch named `$x` is no way past a ceiling of `main`, and a repo of
// `$a/../..` none past the name check and an owner's ceiling.
func TestAValueAFiringBoundIsHeldAsWritten(t *testing.T) {
	desc := runner.Description{Offers: []runner.Offer{{Agent: &runner.AgentOffer{Name: "verse"},
		Systems: map[string]runner.System{"git": {Ceiling: ceiling(t, `{repo: !regexp "^github\\.com/acme/", from: main, fetch: {}}`)}}}}}
	spec := launch.Spec{Prompt: "p", Environment: systems.Environment{Systems: systems.Systems{"git": {{
		Attributes: map[string]string{"repo": "$[repo]", "from": "$[head]"}, Verbs: map[string][]systems.Shape{"fetch": {{}}}}}}}}
	for _, c := range []struct {
		b    launch.Binding
		says string // "" = accepted
	}{
		{launch.Binding{"repo": "acme/app", "head": "main"}, ""},
		{launch.Binding{"repo": "acme/app", "head": "$x"}, "from $x is beyond main"},
		{launch.Binding{"repo": "$a/../..", "head": "main"}, `repo: "$a/../.."`},
		{launch.Binding{"repo": "acme/$x", "head": "main"}, `repo: "acme/$x"`},
	} {
		bound, err := spec.Bind(c.b)
		if err != nil {
			t.Fatalf("%v: %v", c.b, err)
		}
		l := &runner.Launch{ID: "x", Prompt: "p", Agent: &runner.Agent{Name: "verse"}, Environment: bound.Environment}
		err = CheckLaunch(l, desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%v: refused: %v", c.b, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%v: %v, want %q", c.b, err, c.says)
		}
	}
}

// A script's repos are an agent's: it calls the same tools, so rows on the git and github steps
// decide its calls as they do the agent's.
func TestAScriptsReposMayBeWrittenAndGated(t *testing.T) {
	desc := runner.Description{Offers: []runner.Offer{{Script: true, Systems: intercepted("git", "github")}}}
	launch := func(r runner.Repo, rows ...task.Row) *runner.Launch {
		return &runner.Launch{ID: "x", Autonomy: append(task.Autonomy(rows), task.Row{Then: task.Then{Mode: task.ThenAsk}}),
			Script: "echo", Environment: reachEnv(r.Repo, "github")}
	}
	if err := CheckLaunch(launch(runner.Repo{Repo: "acme/app"}), desc, nil); err != nil {
		t.Errorf("refused for a script: %v", err)
	}
	for _, m := range []*task.Match{{System: "git", Verb: "push"}, {System: "github", Verb: "create-pr"}, {System: "github", Verb: "update-pr"}} {
		row := task.Row{Name: "g", Match: m, Then: task.Then{Mode: task.ThenAsk}}
		if err := CheckLaunch(launch(runner.Repo{Repo: "acme/app"}, row), desc, nil); err != nil {
			t.Errorf("a script's row on %s.%s: %v", m.System, m.Verb, err)
		}
		l := launch(runner.Repo{Repo: "acme/app"}, row)
		if err := CheckAutonomy(l.Autonomy, l); err != nil {
			t.Errorf("a running script's new row on %s.%s: %v", m.System, m.Verb, err)
		}
	}
}

// CONTAINMENT: each block of a launch's environment lies within its system's ceiling, one
// ceiling block holding it whole. A repository outside the ceiling's patterns, a push shape wider
// than any of the ceiling's, a bare verb under a ceiling that names a field, a verb the ceiling
// block does not give, and a system held with an empty ceiling are refused, naming the ceiling;
// a system the ceiling leaves open is not; the autonomy is not held to the ceiling (its rows
// reach the environment, which is within it), so a slide is the reach check alone.
func TestALaunchsEnvironmentIsWithinTheRunnersCeilings(t *testing.T) {
	ask := task.Then{Mode: task.ThenAsk}
	desc := runner.Description{Offers: []runner.Offer{{Script: true,
		Systems: map[string]runner.System{
			"git": {Ceiling: ceiling(t, `
- repo: !regexp "^github\\.com/acme/"
  fetch: {}
  push: [{ref: !regexp "^refs/heads/verse/"}, {ref: !glob "refs/heads/release/*", force: false}]
- repo: github.com/acme/site
  fetch: {}`)},
			"github":   {Ceiling: systems.Open()},
			"commands": {Ceiling: ceiling(t, `{run: {command: !regexp "^make "}}`)},
			"files":    {},
		}}}}
	env := func(git string, more ...string) systems.Environment {
		t.Helper()
		e, err := readEnvText(`systems: {git: ` + git + strings.Join(more, "") + `}`)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	launch := func(e systems.Environment) *runner.Launch {
		return &runner.Launch{ID: "x", Autonomy: task.Autonomy{{Then: ask}}, Script: "echo", Environment: e}
	}
	for _, c := range []struct {
		name string
		env  systems.Environment
		says string // "" = accepted
	}{
		{"a push within", env(`{repo: acme/app, fetch: {}, push: {ref: !glob "refs/heads/verse/auto/*"}}`), ""},
		{"a release push that keeps force out", env(`{repo: acme/app, push: {ref: "refs/heads/release/1.0", force: false}}`), ""},
		{"a second block of the ceiling", env(`{repo: acme/site, fetch: {}}`), ""},
		{"a repository outside", env(`{repo: other/app, fetch: {}}`),
			"environment.systems.git: beyond this runner's ceiling for git: [0]: repo github.com/other/app is beyond !regexp ^github\\.com/acme/"},
		{"a push wider than any shape", env(`{repo: acme/app, push: {ref: !regexp "^refs/heads/"}}`), "push {ref: !regexp ^refs/heads/} is within no shape of its"},
		{"a bare push under a ceiling naming ref", env(`{repo: acme/app, push: {}}`), "push {} is within no shape of its: {ref: !regexp ^refs/heads/verse/}"},
		{"a release push leaving force open", env(`{repo: acme/app, push: {ref: "refs/heads/release/1.0"}}`), "within no shape"},
		{"a verb the block does not give", env(`{repo: acme/site, push: {ref: refs/heads/main}}`), "[1]: it gives no `push` (it gives: fetch)"},
		{"an open ceiling", env(`{repo: acme/app, fetch: {}}`, `, github: {repo: acme/app, create-pr: {}}`), ""},
		{"a command within", env(`{repo: acme/app, fetch: {}}`, `, commands: {run: {command: "make test"}}`), ""},
		{"a command beyond", env(`{repo: acme/app, fetch: {}}`, `, commands: {run: {}}`), "environment.systems.commands: beyond this runner's ceiling for commands"},
		{"a system held with an empty ceiling", env(`{repo: acme/app, fetch: {}}`, `, files: {write: {}}`), "environment.systems.files: beyond this runner's ceiling for files: the ceiling gives nothing of it"},
	} {
		err := CheckLaunch(launch(c.env), desc, nil)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%s: %v, want it refused saying %q", c.name, err, c.says)
		}
	}
	// The slide is held to the environment the launch had, which is within the ceilings.
	running := launch(env(`{repo: acme/app, push: {ref: !regexp "^refs/heads/verse/"}}`))
	push := func(ref *ir.Node) task.Row {
		return task.Row{Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ref}}, Then: task.Then{Mode: task.ThenAllow}}
	}
	if err := CheckAutonomy(task.Autonomy{push(ir.FromString("refs/heads/release/2.0")), {Then: ask}}, running); err == nil || !strings.Contains(err.Error(), "matches nothing the environment gives") {
		t.Errorf("a slide reaching past the environment: %v", err)
	}
	if err := CheckAutonomy(task.Autonomy{push(systems.Glob("refs/heads/verse/auto/*")), {Then: ask}}, running); err != nil {
		t.Errorf("a slide within the environment: %v", err)
	}
}

// A NETWORK WITH NO CEILING IS BOUNDED ONLY BY WHAT THE RUNNER BUILDS; one with a ceiling holds
// the environment's network block within it.
func TestANetworkCeilingBoundsOnlyWhereWritten(t *testing.T) {
	offer := func(sys map[string]runner.System) runner.Description {
		return runner.Description{Offers: []runner.Offer{{Script: true, Systems: sys}}}
	}
	l := &runner.Launch{ID: "x", Script: "echo", Environment: netEnv(runner.NetworkAllow, "pypi.org")}
	for name, sys := range map[string]map[string]runner.System{
		"held with no ceiling": {"network": {}},
		"not held":             nil,
	} {
		if err := CheckLaunch(l, offer(sys), nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bounded := offer(map[string]runner.System{"network": {Ceiling: ceiling(t, `{mode: !or [none, allow], tcp-client: {host: !glob "*.github.com"}}`)}})
	if err := CheckLaunch(l, bounded, nil); err == nil || !strings.Contains(err.Error(), "beyond this runner's ceiling for network") {
		t.Errorf("a host beyond the network ceiling: %v", err)
	}
	if err := CheckLaunch(&runner.Launch{ID: "x", Script: "echo", Environment: netEnv(runner.NetworkAllow, "api.github.com")}, bounded, nil); err != nil {
		t.Errorf("a host within the network ceiling: %v", err)
	}
}

// THE NETWORK IS A FACT OF THE RUN, ABSENT BEING NONE, and an offer says which networks its
// runner builds (the network's `builds`): a launch asking for one it does not build is refused
// where it is written, saying what it builds, the absent one among them on a runner that cannot
// take the network away. (An offer holding no network is no registered runner's:
// Description.Check refuses it.)
func TestTheNetworkIsAFactTheOfferBuilds(t *testing.T) {
	offer := func(modes ...string) runner.Description {
		return described(runner.Offer{Script: true, Systems: map[string]runner.System{"network": builds(modes...)}})
	}
	sandboxed, unconfined := offer(runner.NetworkOpen, runner.NetworkNone, runner.NetworkAllow), offer(runner.NetworkOpen)
	launch := func(e systems.Environment) *runner.Launch {
		return &runner.Launch{ID: "x", Script: "echo", Environment: e}
	}
	open, none := netEnv(runner.NetworkOpen), netEnv(runner.NetworkNone)
	hosts := netEnv(runner.NetworkAllow, "pypi.org")
	absent := systems.Environment{}
	for _, c := range []struct {
		name string
		desc runner.Description
		n    systems.Environment
		says string
	}{
		{"sandboxed, absent", sandboxed, absent, ""},
		{"sandboxed, hosts", sandboxed, hosts, ""},
		{"unconfined, open", unconfined, open, ""},
		{"unconfined, absent", unconfined, absent, "environment.systems.network: mode none: this runner builds open only"},
		{"unconfined, none", unconfined, none, "mode none: this runner builds open only"},
		{"unconfined, hosts", unconfined, hosts, "mode allow: this runner builds open only"},
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
	l := runner.Launch{ID: "x", Script: "echo", Environment: gitEnv("acme/app")}
	l.Environment.Systems["git-issue"] = []systems.Block{{Attributes: map[string]string{"repo": "acme/app"}}}
	gitOnly := runner.Description{Offers: []runner.Offer{{Script: true, Systems: intercepted("git")}}}
	if err := CheckOffer(&l, gitOnly); err == nil || !strings.Contains(err.Error(), "holds no `git-issue`") {
		t.Errorf("a tracker on an offer without git-issue: %v", err)
	}
	both := runner.Description{Offers: []runner.Offer{{Script: true, Systems: intercepted("git", "git-issue")}}}
	if err := CheckOffer(&l, both); err != nil {
		t.Errorf("a tracker on an offer with git-issue: %v", err)
	}
}

// readEnvText reads an environment as a spawn writes it.
func readEnvText(text string) (systems.Environment, error) {
	var e systems.Environment
	err := codec.Unmarshal([]byte(text), &e, codec.Tony)
	return e, err
}

// AN AGENT'S SETTINGS ARE THE ONES ITS OFFER TAKES, each a value it takes: an undeclared one is
// refused naming what the agent takes, a value outside its patterns or of the wrong form is
// refused, a value bound at the firing passes, and an annotation the offer does not name is
// refused.
func TestAnAgentsSettingsAreTheOffers(t *testing.T) {
	o := agentOffer(nil)
	o.Agent.Settings = map[string]runner.Setting{
		"model":       {Values: []string{"claude-*"}, Default: true},
		"temperature": {Form: runner.FormNumber},
	}
	o.Agent.Annotations = map[string]string{"verse.portkey/virtual-key": "string"}
	desc := described(o)
	for _, c := range []struct {
		settings, annotations map[string]string
		says                  string // "" = accepted
	}{
		{map[string]string{"model": "claude-sonnet-5", "temperature": "0.2"}, nil, ""},
		{nil, nil, ""},
		{map[string]string{"model": "$[subject.model]"}, nil, `"$[subject.model]" is none of claude-*`},
		{map[string]string{"effort": "high"}, nil, "agent.effort: verse on this runner takes no `effort` (it takes: model, temperature)"},
		{map[string]string{"model": "gpt-5"}, nil, `agent.model: verse on this runner takes no such model: "gpt-5" is none of claude-*`},
		{map[string]string{"temperature": "warm"}, nil, `"warm" is not a number`},
		{nil, map[string]string{"verse.portkey/virtual-key": "k"}, ""},
		{nil, map[string]string{"other": "x"}, `takes no "other"`},
	} {
		l := &runner.Launch{ID: "x", Prompt: "go", Agent: &runner.Agent{Name: "verse", Settings: c.settings, Annotations: c.annotations},
			Environment: netEnv(runner.NetworkOpen)}
		err := CheckOffer(l, desc)
		switch {
		case c.says == "" && err != nil:
			t.Errorf("%v %v: refused: %v", c.settings, c.annotations, err)
		case c.says != "" && (err == nil || !strings.Contains(err.Error(), c.says)):
			t.Errorf("%v %v: %v, want it refused saying %q", c.settings, c.annotations, err, c.says)
		}
	}
}

// WHAT A DOER CAN'T RUN WITHOUT, ITS ENVIRONMENT GIVES: a launch missing a required step is
// refused where it is written, naming both ways to add it; the empty block gives it.
func TestADoersRequiredStepsAreGiven(t *testing.T) {
	o := agentOffer(intercepted("commands", "files"))
	o.Agent.Name = "codex"
	o.Requires = []string{task.StepCommandsRun, task.StepFilesWrite}
	desc := described(o)
	env := func(text string) systems.Environment {
		t.Helper()
		var e systems.Environment
		if err := codec.Unmarshal([]byte(text), &e, codec.Tony); err != nil {
			t.Fatal(err)
		}
		return e
	}
	for text, says := range map[string]string{
		`{systems: {network: {mode: open}, files: {write: {}}}}`:                                               "codex on this runner requires commands.run: add `commands: {}` or `commands: {run: {}}`",
		`{systems: {network: {mode: open}, commands: {run: {}}}}`:                                              "requires files.write: add `files: {}` or `files: {write: {}}`",
		`{systems: {network: {mode: open}, commands: {}, files: {}}}`:                                          "",
		`{systems: {network: {mode: open}, commands: {run: {command: !regexp "^make "}}, files: {write: {}}}}`: "", // narrowed, but given
	} {
		l := &runner.Launch{ID: "x", Prompt: "go", Agent: &runner.Agent{Name: "codex"}, Environment: env(text)}
		err := CheckOffer(l, desc)
		if says == "" && err != nil || says != "" && (err == nil || !strings.Contains(err.Error(), says)) {
			t.Errorf("%s: %v, want %q", text, err, says)
		}
	}
}
