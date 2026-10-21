package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/systems/systemstest"
)

// allowAll lets every step the environment gives go ahead: a row allowing each system, then
// `then: allow`.
func allowAll() Autonomy {
	var p Autonomy
	for _, d := range systems.All() {
		p = append(p, Row{Match: &Match{System: d.Name}, Then: Then{Mode: ThenAllow}})
	}
	return append(p, Row{Then: Then{Mode: ThenAllow}})
}

// anyEnv is an environment giving every verb of each system that needs no attribute to name its
// block (files, commands, agent, question, slack, linear-issue, …), any call of it: what a test of
// the policy alone runs in, the environment out of its way.
func anyEnv() systems.Environment {
	e := systems.Environment{Systems: systems.Systems{}}
	for _, d := range systems.All() {
		if _, named := d.Unique(); named {
			continue
		}
		b := systems.Block{Verbs: map[string][]systems.Shape{}}
		for _, v := range d.Verbs {
			b.Verbs[v.Name] = []systems.Shape{{}}
		}
		e.Systems[d.Name] = []systems.Block{b}
	}
	return e
}

// askOn is a policy that asks before the tool `tool`, under toolSup: every other step goes ahead.
func askOn(tool string) Autonomy {
	return Autonomy{
		{Name: "g", Match: &Match{System: "commands", Verb: "run", Params: map[string]string{"command": tool}}, Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAllow}},
	}
}

// asksOnCommands gives the task the shell, and asks before each command.
func asksOnCommands() Autonomy {
	return Autonomy{{Match: &Match{System: "commands"}, Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAsk}}}
}

// on is a match of one system's verb with the params given.
func on(system, verb string, params ...string) *Match {
	m := &Match{System: system, Verb: verb}
	for i := 0; i+1 < len(params); i += 2 {
		if m.Params == nil {
			m.Params = map[string]string{}
		}
		m.Params[params[i]] = params[i+1]
	}
	return m
}

// toolSup is a supervisor for the engine's tests, whose drivers propose tools by made-up names:
// every tool is a `commands.run` whose command is the tool's name, so a row names a tool by its
// command. The runner's ceilings leave the environment open, so the autonomy decides.
func toolSup(p Autonomy, r Resolver) *Supervisor {
	s := systemstest.Opened(NewSupervisor(anyEnv(), p, r), anyEnv())
	s.SetStepOf(func(tu ToolUse) (Step, bool) {
		if st, ok := StepOf(tu); ok {
			return st, true
		}
		return Step{Name: StepCommandsRun, Fields: map[string]string{"command": tu.Name}}, true
	})
	return s
}

// The verse tools are the steps the spec names; anything else is no step.
func TestTheVerseToolsAreSteps(t *testing.T) {
	for _, tc := range []struct {
		tu   ToolUse
		want string
	}{
		{ToolUse{Name: "write_file", Input: map[string]any{"path": "db/x.sql"}}, "files.write path=db/x.sql"},
		{ToolUse{Name: "run_bash", Input: map[string]any{"command": "make test"}}, "commands.run command=make test"},
		{ToolUse{Name: "call_agent", CallAgent: &CallAgentRequest{Agent: "reviewer"}}, "agent.call-sub-agent agent=reviewer"},
	} {
		st, ok := StepOf(tc.tu)
		if !ok || st.String() != tc.want {
			t.Errorf("%s = %q %v, want %q", tc.tu.Name, st, ok, tc.want)
		}
	}
	// A path is the one the executor writes, however the model spelled it.
	for _, spelled := range []string{"db/x.sql", "./db/x.sql", "/db/x.sql", "db/../db/x.sql", "db//x.sql"} {
		st, _ := StepOf(ToolUse{Name: "write_file", Input: map[string]any{"path": spelled}})
		if st.Fields["path"] != "db/x.sql" {
			t.Errorf("%q is the step %s, want path=db/x.sql", spelled, st)
		}
	}
	for _, name := range []string{"read_file", "list_dir", "ask", "yield"} {
		if st, ok := StepOf(ToolUse{Name: name}); ok {
			t.Errorf("%s is the step %s, want none", name, st)
		}
	}
}

// A row's match covers a step by its system and verb and each param it gives: `*` within a
// segment and `**` across, and a command as a whole string with `*` matching anything. A param
// it leaves out matches anything, and so does a system or a verb.
func TestARowMatchesAStep(t *testing.T) {
	write := func(path string) Step { return Step{Name: StepFilesWrite, Fields: map[string]string{"path": path}} }
	run := func(cmd string) Step { return Step{Name: StepCommandsRun, Fields: map[string]string{"command": cmd}} }
	for _, tc := range []struct {
		m    *Match
		s    Step
		want bool
	}{
		{nil, run("ls"), true},
		{&Match{}, run("ls"), true},
		{&Match{Verb: "write"}, write("x"), true},
		{&Match{Verb: "write"}, run("ls"), false},
		{&Match{System: "files"}, write("x"), true},
		{on("files", "write"), write("anything/at/all"), true},
		{on("files", "write"), run("ls"), false},
		{on("files", "write", "path", "db/*.sql"), write("db/x.sql"), true},
		{on("files", "write", "path", "db/*.sql"), write("db/m/x.sql"), false},
		{on("files", "write", "path", "db/**"), write("db/m/x.sql"), true},
		{on("files", "write", "path", "db/**.sql"), write("db/m/x.go"), false},
		{on("files", "write", "path", "db/x.sql"), write("db/x.sql"), true},
		{on("files", "write", "path", "db/x.sql"), write("db/xsql"), false},
		{on("commands", "run", "command", "git push*"), run("git push origin main"), true},
		{on("commands", "run", "command", "rm *"), run("rm -rf /tmp/a/b"), true},
		{on("commands", "run", "command", "make"), run("make test"), false},
		// A command of several lines is matched as a whole: `*` crosses the newline.
		{on("commands", "run", "command", "git push*"), run("git push origin main\n"), true},
		{on("commands", "run", "command", "rm *"), run("rm -rf x\necho done"), true},
		{on("commands", "run", "command", "*git push*"), run("cd repo\ngit push origin main"), true},
	} {
		if got := tc.m.Matches(tc.s); got != tc.want {
			t.Errorf("%+v on %s = %v, want %v", tc.m, tc.s, got, tc.want)
		}
	}
}

// THE LAST ROW IS THE DIAL: a call no row above matched gets it, and a step a row allows whatever
// its fields is never asked about.
func TestTheLastRowIsTheDial(t *testing.T) {
	ask, allow := Then{Mode: ThenAsk}, Then{Mode: ThenAllow}
	if p := (Autonomy{{Then: allow}, {Then: ask}}); p.Default() != ThenAsk || p.MayAsk(StepGitPush) {
		t.Errorf("the dial reads %s, may ask %v", p.Default(), p.MayAsk(StepGitPush))
	}
	p := Autonomy{{Match: on("git", "push", "ref", "refs/heads/verse/*"), Then: allow}, {Match: on("git", "push"), Then: ask}, {Then: allow}}
	if !p.MayAsk(StepGitPush) || p.MayAsk(StepCommandsRun) {
		t.Errorf("may ask: push %v, run %v", p.MayAsk(StepGitPush), p.MayAsk(StepCommandsRun))
	}
}

// A STEP OUTSIDE THE ENVIRONMENT IS REFUSED BEFORE ANY ROW IS READ, AND NOBODY IS ASKED: even
// `then: allow` decides only what the environment gives, and the refusal names the environment,
// not a row, saying what it does give.
func TestAStepOutsideTheEnvironmentIsRefusedFirst(t *testing.T) {
	asked := 0
	env := systems.Environment{Systems: systems.Systems{"files": {{Verbs: map[string][]systems.Shape{"write": {{"path": "docs/**"}}}}}}}
	sup := systemstest.Opened(NewSupervisor(env, Autonomy{{Then: Then{Mode: ThenAllow}}},
		resolverFunc(func(GateQuestion) Resolution { asked++; return Resolution{Choice: Proceed, Proceed: true} })), env)
	for _, c := range []struct {
		st   Step
		says string
	}{
		{Step{Name: StepCommandsRun, Fields: map[string]string{"command": "ls"}}, "the environment gives no `commands` (it gives: files)"},
		{Step{Name: StepFilesWrite, Fields: map[string]string{"path": "src/x.go"}}, "it gives {path: docs/**}"},
	} {
		res, err := sup.Ask(context.Background(), c.st, ToolUse{})
		if err != nil || res.Proceed || res.By != ByEnvironment || res.Question.Gate != EnvironmentGate ||
			!strings.Contains(res.Note, "outside the environment: ") || !strings.Contains(res.Note, c.says) || asked != 0 {
			t.Errorf("%s: %+v %v, asked %d; want refused by the environment saying %q", c.st, res, err, asked, c.says)
		}
	}
	if res, err := sup.Ask(context.Background(), Step{Name: StepFilesWrite, Fields: map[string]string{"path": "docs/a.md"}}, ToolUse{}); err != nil || !res.Proceed || res.By != ByAuto {
		t.Errorf("a step within: %+v %v; want it allowed by the row", res, err)
	}
	if res, _ := systemstest.Opened(NewSupervisor(systems.Environment{}, nil, resolverFunc(func(GateQuestion) Resolution { asked++; return Resolution{} })), systems.Environment{}).
		Ask(context.Background(), Step{Name: StepFilesWrite, Fields: map[string]string{"path": "a"}}, ToolUse{}); res.Proceed ||
		!strings.Contains(res.Note, "gives no system") || asked != 0 {
		t.Errorf("an empty environment: %+v, asked %d; want refused saying what to add", res, asked)
	}
}

// A PUSH IS WITHIN THE ENVIRONMENT ON THE FIELDS THE RUNNER FILLS: a shape that admits a branch
// only through `own: true` lets the task's own branch go and refuses another's.
func TestAPushIsWithinOnTheFieldsTheRunnerFills(t *testing.T) {
	env := systems.Environment{Systems: systems.Systems{"git": {{Attributes: map[string]string{"repo": "github.com/acme/app"},
		Verbs: map[string][]systems.Shape{"push": {{"ref": "refs/heads/verse/auto/*", "own": "true"}}}}}}}
	sup := systemstest.Opened(NewSupervisor(env, Autonomy{{Then: Then{Mode: ThenAllow}}}, resolverFunc(func(GateQuestion) Resolution { return Resolution{} })), env)
	push := func(own string) Step {
		return Step{Name: StepGitPush, Fields: map[string]string{"repo": "github.com/acme/app", "ref": "refs/heads/verse/auto/t1",
			"delete": "false", "force": "false", "exists": "true", "own": own}}
	}
	if res, _ := sup.Ask(context.Background(), push("true"), ToolUse{}); !res.Proceed {
		t.Errorf("its own branch: %+v, want it allowed", res)
	}
	if res, _ := sup.Ask(context.Background(), push("false"), ToolUse{}); res.Proceed || res.By != ByEnvironment {
		t.Errorf("another's branch: %+v, want it refused by the environment", res)
	}
}

// A row that names a step says how that step is asked; a step no row names is decided by the
// default row, and the question names the row by its name or its place.
func TestARowSaysHowAStepIsAsked(t *testing.T) {
	p := Autonomy{{Name: "release", Match: on("git", "push"), Then: Then{Mode: ThenAsk, Ask: &Ask{Need: 2}}}, {Then: Then{Mode: ThenAsk}}}
	if row, i := p.Decide(Step{Name: StepGitPush}); p.NameOf(i) != "release" || row.Then.Ask.Need != 2 {
		t.Errorf("a push is decided by %s %+v, want the release row", p.NameOf(i), row)
	}
	if row, i := p.Decide(Step{Name: StepCommandsRun}); p.NameOf(i) != "autonomy[1]" || row.Then.Mode != ThenAsk {
		t.Errorf("a command is decided by %s %+v, want the default row", p.NameOf(i), row)
	}
	// The decision no row made has a name a question can carry and Named can resolve, and
	// that no row can be given.
	first := Autonomy{p[0]}
	if row, i := first.Decide(Step{Name: StepCommandsRun}); i != -1 || p.NameOf(i) != "autonomy[default]" || row.Then.Mode != DefaultAutonomy()[0].Then.Mode {
		t.Errorf("a call no row matches is decided by %s %+v, want autonomy[default] as DefaultPolicy decides", p.NameOf(i), row)
	}
	if r, ok := p.Named("autonomy[default]"); !ok || r.Then.Mode != DefaultAutonomy()[0].Then.Mode {
		t.Errorf("Named(autonomy[default]) = %+v %v, want DefaultPolicy's row", r, ok)
	}
	bracketed := Autonomy{{Name: "autonomy[default]", Then: Then{Mode: ThenAllow}}}
	if err := bracketed.Check(); err == nil {
		t.Error("a row named autonomy[default] was accepted")
	}
	if r, ok := p.Named("autonomy[1]"); !ok || r.Match != nil {
		t.Errorf("named autonomy[1]: %+v %v", r, ok)
	}
}

// A DENY ROW REFUSES WITHOUT ASKING: the step is refused in the row's name, nobody is asked, and
// the task goes on.
func TestADenyRowRefusesWithoutAsking(t *testing.T) {
	asked := false
	sup := systemstest.Opened(NewSupervisor(anyEnv(), Autonomy{
		{Name: "no-rm", Match: on("commands", "run", "command", "rm *"), Then: Then{Mode: ThenDeny}},
		{Then: Then{Mode: ThenAllow}},
	}, resolverFunc(func(GateQuestion) Resolution { asked = true; return Resolution{Choice: Proceed, Proceed: true} })), anyEnv())
	res, err := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "rm -rf /"}})
	if err != nil || res.Proceed || res.Choice != Deny || res.By != ByAutonomy || res.Cast() || asked ||
		!strings.Contains(res.Note, "denied by the autonomy's row no-rm") || res.Question.Gate != "no-rm" {
		t.Fatalf("Check = %+v, %v (asked %v)", res, err, asked)
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}); !res.Proceed || res.By != ByAuto {
		t.Errorf("ls = %+v, want it allowed by the default row", res)
	}
}

// The first row that matches is the one that asks, and its question carries what the row says:
// how many approvals settle it, how long it stands, and who is asked.
func TestTheFirstMatchingRowAsksItsQuestion(t *testing.T) {
	g := Autonomy{
		{Name: "migrations", Match: on("files", "write", "path", "db/**"),
			Then: Then{Mode: ThenAsk, Ask: &Ask{Need: 2, Within: "24h", Answerers: []string{"group:dba"}}}},
		{Name: "any-write", Match: on("files", "write"), Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAllow}},
	}
	var posed GateQuestion
	sup := systemstest.Opened(NewSupervisor(anyEnv(), g, resolverFunc(func(q GateQuestion) Resolution {
		posed = q
		return Resolution{Choice: Proceed, Proceed: true, By: "user:dba"}
	})), anyEnv())
	sup.SetOrigin("t1")
	res, err := sup.Check(context.Background(), ToolUse{Name: "write_file", Input: map[string]any{"path": "db/x.sql"}})
	if err != nil || !res.Proceed {
		t.Fatalf("Check = %+v, %v", res, err)
	}
	if posed.Gate != "migrations" || posed.Origin != "t1" || posed.Step.Name != StepFilesWrite ||
		posed.Deadline == nil || posed.Deadline.Within != "24h" || len(posed.Answerers) != 1 {
		t.Fatalf("posed %+v", posed)
	}
	if need, _ := Need(posed.OptionsOrDefault(), Proceed); need != 2 {
		t.Errorf("proceed needs %d approvals, want 2", need)
	}
	// A step no row names is the default row's.
	res, _ = sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}})
	if !res.Proceed || res.By != ByAuto {
		t.Errorf("an unnamed command = %+v, want it allowed by the default row", res)
	}
}

// Replacing the policy takes effect from the next step, for a sub-agent's supervisor too.
func TestReplacedPolicyAppliesFromTheNextStep(t *testing.T) {
	asked := 0
	sup := systemstest.Opened(NewSupervisor(anyEnv(), allowAll(), resolverFunc(func(GateQuestion) Resolution {
		asked++
		return Resolution{Choice: Deny, By: "user:x"}
	})), anyEnv())
	child := sup.child()
	run := ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}
	if res, _ := child.Check(context.Background(), run); !res.Proceed {
		t.Fatalf("before: %+v", res)
	}
	sup.SetAutonomy(Autonomy{{Name: "cmd", Match: on("commands", "run"), Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAllow}}})
	if res, _ := child.Check(context.Background(), run); res.Proceed || asked != 1 {
		t.Fatalf("after: %+v, asked %d", res, asked)
	}
}

// A policy that can't be installed is refused, saying why: a `then` that is not one of the
// three, a system or a verb or a param the documents do not describe, a `$` in a pattern, an
// ask's shape on a row that does not ask, a last row that names a call, and a row that matches
// every call before the last.
func TestAPolicyIsChecked(t *testing.T) {
	ask, allow := Then{Mode: ThenAsk}, Then{Mode: ThenAllow}
	for _, tc := range []struct {
		p    Autonomy
		says string
	}{
		{Autonomy{{Then: Then{Mode: "sometimes"}}}, "not one of"},
		{Autonomy{{Match: on("commands", "run")}, {Then: ask}}, "then:"},
		{Autonomy{{Match: on("deploy", "now"), Then: ask}, {Then: ask}}, "not a system"},
		{Autonomy{{Match: on("files", "run"), Then: ask}, {Then: ask}}, "no verb"},
		{Autonomy{{Match: &Match{Verb: "push"}, Then: ask}, {Then: ask}}, "names its system"},
		{Autonomy{{Match: on("files", "write", "command", "ls"), Then: ask}, {Then: ask}}, "match.command: not a field"},
		{Autonomy{{Match: &Match{Params: map[string]string{"ref": "main"}}, Then: ask}, {Then: ask}}, "names its system"},
		{Autonomy{{Match: &Match{Verb: "close", Params: map[string]string{"issue": "eng-42"}}, Then: Then{Mode: ThenDeny}}, {Then: allow}}, "names its system"},
		{Autonomy{{Match: on("git", "push", "ref", "$subject.head"), Then: ask}, {Then: ask}}, "not bound"},
		{Autonomy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Need: -1}}}, {Then: ask}}, "need"},
		{Autonomy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Within: "soon"}}}, {Then: ask}}, "within"},
		{Autonomy{{Match: on("commands", "run"), Then: Then{Mode: ThenAllow, Ask: &Ask{Need: 2}}}, {Then: ask}}, "goes with `ask`"},
		{Autonomy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Labels: map[string]string{"a b": "c"}}}}, {Then: ask}}, "labels"},
		{Autonomy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Within: "1m", Default: "allow"}}}, {Then: ask}}, "not one of the question's answers, proceed or deny"},
		{Autonomy{{Match: on("commands", "run"), Then: ask}}, "ends with its default row"},
		{Autonomy{{Then: allow}, {Match: on("commands", "run"), Then: ask}, {Then: ask}}, "never decide"},
		{Autonomy{{Name: "two words", Then: ask}}, "a word"},
		{Autonomy{{Name: "turn", Match: on("commands", "run"), Then: ask}, {Then: ask}}, "no row's"},
		{Autonomy{{Name: "release", Match: on("slack", "post"), Then: ask}, {Name: "release", Match: on("git", "push"), Then: ask}, {Then: ask}}, "is row 0's"},
		{Autonomy{{Name: "question-ask", Match: on("commands", "run"), Then: ask}, {Then: ask}}, "no row's"},
		{Autonomy{{Match: on("commands", ""), Then: Then{Mode: "withhold"}}, {Then: ask}}, `"withhold" is not one of deny, ask, allow`},
		{Autonomy{{Name: "environment", Match: on("commands", "run"), Then: ask}, {Then: ask}}, "no row's"},
		{Autonomy{{Match: &Match{System: "network"}, Then: ask}, {Then: ask}}, "a connection can't wait for an answer"},
		{Autonomy{{Match: on("git", "push", "delete", "True"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "true or false"},
		{Autonomy{{Match: on("git", "push", "ref", "main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "a full ref (refs/heads/main)"},
		{Autonomy{{Match: on("git", "push", "ref", "*", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Autonomy{{Match: on("git", "push", "ref", "*/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Autonomy{{Match: on("git", "push", "ref", "refs/*"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Autonomy{{Match: on("git", "push", "delete", ""), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "never empty"},
		{Autonomy{{Match: on("git", "push", "repo", "acme/app"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "`host/owner/name`"},
		{Autonomy{{Match: on("github", "create-pr", "repo", "github.com/Acme/App"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "lowercase (github.com/acme/app)"},
		{Autonomy{{Match: on("github", "create-pr", "base", "refs/heads/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "a branch's name (main)"},
		{Autonomy{{Match: on("linear-issue", "create", "team", "eng"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "uppercase (ENG)"},
		{Autonomy{{Match: on("slack", "post", "channel", "#Deploys"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "lowercase, without a `#` (deploys)"},
	} {
		if err := tc.p.Check(); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%+v: %v, want an error saying %q", tc.p, err, tc.says)
		}
	}
	for _, ok := range []Autonomy{
		{},
		{{Then: ask}},
		{{Match: on("git", "push", "repo", "**"), Then: allow}, {Then: ask}},
		{{Match: &Match{System: "git", Params: map[string]string{"ref": "refs/heads/main"}}, Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "**", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "refs/heads/*", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "*/heads/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("github", "update-pr", "base", ""), Then: allow}, {Then: ask}}, // an update that keeps its base
		{{Match: on("git", "push", "force", "*"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
	} {
		if err := ok.Check(); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
}

// resolverFunc answers every question with what f says.
type resolverFunc func(GateQuestion) Resolution

func (f resolverFunc) Resolve(_ context.Context, q GateQuestion, _ ToolUse) (Resolution, error) {
	res := f(q)
	res.Question = q
	return res, nil
}

// A gate id must split back into the id of whatever posed it, on GateSep and nowhere else
// — that split is how versed routes a `gate resolve` to the owning task, and how the CLI
// tells a gate id from the task id it belongs to. Nothing else pins it: producer
// (Supervisor.Check) and splitters (versed, cmd/verse) live in three packages, so a
// delimiter changed in one and not the others compiles, passes every other test, and fails
// only at resolve time with "unknown or already-answered decision".
func TestGateIDSplitsOnGateSepToItsOwner(t *testing.T) {
	const origin = "review-demo.pr.1" // a real spawnID shape: hyphens and colons, no GateSep
	sup := toolSup(allowAll(), AutoResolver{Who: "agent"})
	sup.SetOrigin(origin)

	res, err := sup.Check(context.Background(), ToolUse{Name: "x"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	id := res.Question.ID
	owner, rest, ok := strings.Cut(id, GateSep)
	if !ok {
		t.Fatalf("gate id %q carries no GateSep %q — a resolve cannot find its owner", id, GateSep)
	}
	if owner != origin {
		t.Errorf("owner = %q, want %q — resolve would route %q to the wrong task", owner, origin, id)
	}
	if strings.Contains(rest, GateSep) {
		t.Errorf("gate id %q has a second GateSep in %q — the split is ambiguous", id, rest)
	}
}

// A task starts without asking, under any autonomy: its start is verse's, decided before it was
// launched. Under `autonomy: ask` its first step asks; approved, it runs, and denied, the agent is
// told and the task goes on.
func TestATaskStartsWithoutAsking(t *testing.T) {
	for _, choice := range []string{Proceed, Deny} {
		reg := NewRegistry()
		e := &recExec{}
		p := NewTask("t", toolSup(asksOnCommands(), reg), &scriptDriver{steps: []ToolUse{tool("work")}}, e)
		done := make(chan Outcome, 1)
		go func() { oc, _ := p.Run(context.Background()); done <- oc }()
		waitFor(t, func() bool { return len(reg.Pending()) == 1 })
		q := reg.Pending()[0].Question
		if q.Gate != "autonomy[0]" || q.Step.Name != StepCommandsRun || q.Step.Fields["command"] != "work" {
			t.Fatalf("first asked %+v, want the step under the row on commands", q)
		}
		reg.Answer(q.ID, Choose(choice, "user:ana"))
		oc := <-done
		ran := len(e.order) == 1
		if oc.Reason != string(Done) || ran != (choice == Proceed) {
			t.Errorf("%s: %s, ran %v", choice, oc.Reason, e.order)
		}
	}
}

// A task cancelled while a step waits for its answer ends cancelled, with the question off the
// docket.
func TestATaskCancelledWhileAStepWaits(t *testing.T) {
	reg := NewRegistry()
	p := NewTask("t", toolSup(asksOnCommands(), reg), &scriptDriver{steps: []ToolUse{tool("work")}}, &recExec{})
	done := make(chan Outcome, 1)
	go func() { oc, _ := p.Run(context.Background()); done <- oc }()
	waitFor(t, func() bool { return len(reg.Pending()) == 1 })
	p.Cancel("not now", "")
	if oc := <-done; oc.Reason != string(Cancelled) || len(reg.Pending()) != 0 {
		t.Fatalf("%s, pending %v", oc.Reason, reg.Pending())
	}
}

// A running task's policy is replaced from the next step, and its read plane says by whom.
func TestSetPolicyOnARunningTask(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{}, &recExec{})
	events, stop := p.Subscribe()
	defer stop()
	if err := p.SetAutonomy(Autonomy{{Then: Then{Mode: "sometimes"}}}, "user:ana"); err == nil {
		t.Fatal("an invalid policy was installed")
	}
	if err := p.SetAutonomy(askOn("deploy"), "user:ana"); err != nil {
		t.Fatal(err)
	}
	if g := p.sup.Autonomy(); len(g) != 2 || g[0].Match.Params["command"] != "deploy" {
		t.Fatalf("policy now %+v", g)
	}
	e := <-events
	if e.Kind != EventAutonomy || e.By != "user:ana" || e.Text != "2 row(s), default allow" {
		t.Fatalf("event %+v", e)
	}
}

// An ended task's policy is not replaced, and nothing is said after its end.
func TestSetPolicyOnAnEndedTask(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{}, &recExec{})
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.SetAutonomy(askOn("x"), "user:ana"); !errors.Is(err, ErrEnded) {
		t.Fatalf("SetPolicy = %v, want ErrEnded", err)
	}
	if g := p.sup.Autonomy(); len(g) != len(allowAll()) {
		t.Fatalf("the policy changed: %+v", g)
	}
	var last Event
	events, stop := p.SubscribeSince(0)
	defer stop()
	for e := range events {
		last = e
	}
	if last.Kind != EventStatus {
		t.Fatalf("the stream ends with %+v, want the terminal", last)
	}
}

// syncLog is a task log a test reads while the task's goroutines write it.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// THE RUNNER'S CEILING IS READ AFTER THE POLICY, AT EACH STEP: a step beyond it is refused in
// the ceiling's name though the policy allowed it or would have asked, saying what the ceiling
// gives, and nobody is asked; a step within it is decided by the policy as before; a system with
// no ceiling in the set is closed, as is every system for a supervisor given no ceilings; a
// sub-agent's supervisor shares the ceiling.
func TestTheCeilingIsReadAfterThePolicy(t *testing.T) {
	asked := 0
	sup := systemstest.Opened(NewSupervisor(anyEnv(), Autonomy{
		{Name: "ship", Match: on("commands", "run", "command", "make ship*"), Then: Then{Mode: ThenAsk}},
		{Match: &Match{System: "files"}, Then: Then{Mode: ThenAllow}},
		{Then: Then{Mode: ThenAllow}},
	}, resolverFunc(func(GateQuestion) Resolution { asked++; return Resolution{Choice: Proceed, Proceed: true} })), anyEnv())
	sup.SetCeilings(map[string]systems.Ceiling{"commands": {{
		Verbs: map[string][]systems.Shape{"run": {{"command": "ls*"}, {"command": "make test*"}}},
	}}})
	for _, command := range []string{"make ship v1", "rm -rf /"} {
		res, err := sup.child().Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": command}})
		if err != nil || res.Proceed || res.Choice != Deny || res.By != ByCeiling || asked != 0 ||
			!strings.Contains(res.Note, "beyond the runner's ceiling for commands") ||
			!strings.Contains(res.Note, "{command: ls*}, {command: make test*}") ||
			!strings.Contains(res.Note, "a check that missed") {
			t.Fatalf("%s: Check = %+v, %v (asked %d)", command, res, err, asked)
		}
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}); !res.Proceed || res.By != ByAuto {
		t.Errorf("ls = %+v, want it allowed by the default row", res)
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "write_file", Input: map[string]any{"path": "x"}}); res.Proceed || res.By != ByCeiling ||
		!strings.Contains(res.Note, "states no ceiling for files") {
		t.Errorf("a step of a system with no ceiling = %+v, want it refused by the ceiling", res)
	}
	// A system held with an empty ceiling gives nothing.
	sup.SetCeilings(map[string]systems.Ceiling{"files": nil})
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "write_file", Input: map[string]any{"path": "x"}}); res.Proceed || res.By != ByCeiling {
		t.Errorf("a step of a system whose ceiling is empty = %+v, want it refused by the ceiling", res)
	}
	// A supervisor given no ceilings closes every system.
	sup.SetCeilings(nil)
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}); res.Proceed || res.By != ByCeiling {
		t.Errorf("a step under no ceilings = %+v, want it refused by the ceiling", res)
	}
}

// AN ACTION OF SEVERAL STEPS GOES AHEAD ONLY WHERE EACH WOULD: a write named `docs/x` that lands
// on `db/x` is refused by a row, the environment or a ceiling on either, wherever the row
// stands; each row that asks about either asks once, in the order of the steps, and every answer
// must be a yes; a row asking about both asks once.
func TestAnActionOfSeveralStepsGoesAheadOnlyWhereEachWould(t *testing.T) {
	write := func(path string) Step { return Step{Name: StepFilesWrite, Fields: map[string]string{"path": path}} }
	steps := []Step{write("docs/x"), write("db/x")}
	var asked []string
	supIn := func(env systems.Environment, p Autonomy, no string) *Supervisor {
		asked = nil
		s := NewSupervisor(env, p, resolverFunc(func(q GateQuestion) Resolution {
			asked = append(asked, q.Gate+" "+q.Step.Fields["path"])
			if q.Gate == no {
				return Resolution{Choice: Deny, Steer: []string{"no from " + q.Gate}}
			}
			return Resolution{Choice: Proceed, Proceed: true, Steer: []string{"yes from " + q.Gate}}
		}))
		s.SetStepsOf(func(ToolUse) []Step { return steps })
		s.SetCeilings(systemstest.Open(env))
		return s
	}
	sup := func(p Autonomy, no string) *Supervisor { return supIn(anyEnv(), p, no) }
	check := func(s *Supervisor) Resolution {
		t.Helper()
		res, err := s.Check(context.Background(), ToolUse{Name: "write"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	docs := Row{Name: "docs", Match: on("files", "write", "path", "docs/**"), Then: Then{Mode: ThenAllow}}
	allowFiles := Autonomy{{Match: &Match{System: "files"}, Then: Then{Mode: ThenAllow}}}
	onlyDocs := systems.Ceiling{{Verbs: map[string][]systems.Shape{"write": {{"path": "docs/**"}}}}}

	// A catch-all deny below an allow on the path named: the step it lands on is refused.
	if res := check(sup(Autonomy{docs, {Name: "rest", Match: on("files", "write"), Then: Then{Mode: ThenDeny}}}, "")); res.Proceed ||
		res.By != ByAutonomy || res.Question.Gate != "rest" || res.Question.Step.Fields["path"] != "db/x" || len(asked) != 0 {
		t.Errorf("allow docs, deny the rest: %+v (asked %v), want refused by rest on db/x", res, asked)
	}
	// An environment that gives writes under docs alone, under an autonomy that allows every write.
	if res := check(supIn(systems.Environment{Systems: systems.Systems{"files": {{Verbs: map[string][]systems.Shape{"write": {{"path": "docs/**"}}}}}}}, allowFiles, "")); res.Proceed ||
		res.By != ByEnvironment || res.Question.Step.Fields["path"] != "db/x" {
		t.Errorf("environment of docs: %+v, want refused by the environment on db/x", res)
	}
	// The runner's ceiling on where it lands, under an autonomy that allows every write.
	s := sup(allowFiles, "")
	s.SetCeilings(map[string]systems.Ceiling{"files": onlyDocs})
	if res := check(s); res.Proceed || res.By != ByCeiling || res.Question.Step.Fields["path"] != "db/x" {
		t.Errorf("ceiling of docs: %+v, want refused by the ceiling on db/x", res)
	}
	// Two rows ask: each once, in the order of the steps, and both answers decide.
	asks := Autonomy{
		{Name: "schema", Match: on("files", "write", "path", "db/**"), Then: Then{Mode: ThenAsk}},
		{Name: "doc", Match: on("files", "write", "path", "docs/**"), Then: Then{Mode: ThenAsk}},
	}
	if res := check(sup(asks, "")); !res.Proceed || strings.Join(asked, ", ") != "doc docs/x, schema db/x" ||
		strings.Join(res.Steer, ", ") != "yes from doc, yes from schema" {
		t.Errorf("two asking rows, both yes: %+v (asked %v)", res, asked)
	}
	if res := check(sup(asks, "schema")); res.Proceed || res.Question.Gate != "schema" ||
		strings.Join(res.Steer, ", ") != "yes from doc, no from schema" {
		t.Errorf("two asking rows, the second no: %+v (asked %v), want refused by schema", res, asked)
	}
	// One row asking about both asks once, about the first.
	if res := check(sup(Autonomy{{Name: "w", Match: on("files", "write"), Then: Then{Mode: ThenAsk}}}, "")); !res.Proceed ||
		strings.Join(asked, ", ") != "w docs/x" {
		t.Errorf("one asking row: %+v (asked %v), want one question about docs/x", res, asked)
	}
	// An allow on the path named and an ask on where it lands: asked about where it lands.
	if res := check(sup(Autonomy{docs, {Then: Then{Mode: ThenAsk}}}, "")); !res.Proceed || strings.Join(asked, ", ") != "autonomy[1] db/x" {
		t.Errorf("allow docs, ask the rest: %+v (asked %v), want one question about db/x", res, asked)
	}
	// Every step allowed: allowed, nobody asked, as the first step.
	if res := check(sup(allowFiles, "")); !res.Proceed ||
		res.By != ByAuto || len(asked) != 0 || res.Question.Step.Fields["path"] != "docs/x" {
		t.Errorf("all allowed: %+v (asked %v)", res, asked)
	}
}

// A NUMBER IN A MATCH IS THE NUMBER IT SPELLS: `issue: 7` matches the step whose issue is 7, as
// `issue: "7"` does.
func TestANumberInAMatchIsItsDigits(t *testing.T) {
	for _, src := range []string{`{match: {system: github-issue, verb: comment, issue: 7}, then: allow}`,
		`{match: {system: github-issue, verb: comment, issue: "7"}, then: allow}`} {
		var r Row
		if err := r.FromTony([]byte(src)); err != nil {
			t.Fatal(err)
		}
		if got := r.Match.Params["issue"]; got != "7" {
			t.Errorf("%s reads the issue as %q", src, got)
		}
		if !r.Match.Matches(Step{Name: StepGitHubIssueComment, Fields: map[string]string{"repo": "x", "issue": "7"}}) {
			t.Errorf("%s does not match issue 7", src)
		}
		if r.Match.Matches(Step{Name: StepGitHubIssueComment, Fields: map[string]string{"repo": "x", "issue": "8"}}) {
			t.Errorf("%s matches issue 8", src)
		}
	}
}

// NULL IS NO PATTERN: a param written null would otherwise drop out of the match, and an allow
// row with it would allow every call of the verb.
func TestANullInAMatchIsRefused(t *testing.T) {
	for _, src := range []string{`{match: {system: git, verb: push, ref: null}, then: allow}`,
		`{match: {system: null, verb: push}, then: allow}`} {
		var r Row
		if err := r.FromTony([]byte(src)); err == nil || !strings.Contains(err.Error(), "null is no pattern") {
			t.Errorf("%s: %v (%+v), want it refused", src, err, r.Match)
		}
	}
}

// A MISSPELLED ASK IS REFUSED, HOWEVER THE POLICY IS READ: `neeed: 2` dropped would ask one
// approval of the docket where the row asks two of a group.
func TestAMisspelledAskIsRefused(t *testing.T) {
	src := `{match: {system: git, verb: push}, then: {ask: {answerer: [group:release], neeed: 2}}}`
	var r Row
	if err := r.FromTony([]byte(src)); err == nil {
		t.Errorf("read as %+v, want the misspelling refused", r.Then.Ask)
	}
	var p Autonomy
	if err := codec.Unmarshal([]byte("["+src+", {then: allow}]"), &p, codec.Tony); err == nil {
		t.Errorf("read as %+v, want the misspelling refused", p[0].Then.Ask)
	}
}
