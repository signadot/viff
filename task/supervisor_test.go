package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/systems"
)

// allowAll gives the task every system's verbs and lets every step go ahead: a row naming each
// system, then `then: allow`.
func allowAll() Policy {
	var p Policy
	for _, d := range systems.All() {
		p = append(p, Row{Match: &Match{System: d.Name}, Then: Then{Mode: ThenAllow}})
	}
	return append(p, Row{Then: Then{Mode: ThenAllow}})
}

// askOn is a policy that asks before the tool `tool`, under toolSup: every other step goes ahead.
func askOn(tool string) Policy {
	return Policy{
		{Name: "g", Match: &Match{System: "commands", Verb: "run", Params: map[string]string{"command": tool}}, Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAllow}},
	}
}

// asksOnCommands gives the task the shell, and asks before each command.
func asksOnCommands() Policy {
	return Policy{{Match: &Match{System: "commands"}, Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAsk}}}
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
// command.
func toolSup(p Policy, r Resolver) *Supervisor {
	s := NewSupervisor(p, r)
	s.stepOf = func(tu ToolUse) (Step, bool) {
		if st, ok := StepOf(tu); ok {
			return st, true
		}
		return Step{Name: StepCommandsRun, Fields: map[string]string{"command": tu.Name}}, true
	}
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

// THE POLICY PROVIDES WHAT ITS ROWS NAME, AND THE LAST ROW IS THE DIAL OVER THAT: a verb no row
// names is not provided, whatever the last row says, and a call of a provided verb no row above
// matched gets the last row. A row on a verb's params provides the verb; a withhold takes it out
// wherever it stands.
func TestThePolicyProvidesWhatItNames(t *testing.T) {
	steps := []string{StepGitPush, StepGitHubCreatePR, StepFilesWrite, StepCommandsRun, StepAgentCallSubAgent}
	ask, allow, deny := Then{Mode: ThenAsk}, Then{Mode: ThenAllow}, Then{Mode: ThenDeny}
	for _, tc := range []struct {
		p        Policy
		provided []string
		asks     []string
	}{
		{Policy{}, nil, nil},
		{Policy{{Then: ask}}, nil, nil},
		{Policy{{Then: allow}}, nil, nil},
		{Policy{{Match: on("files", "write"), Then: ask}, {Match: &Match{System: "git"}, Then: allow}, {Then: deny}}, []string{StepGitPush, StepFilesWrite}, []string{StepFilesWrite}},
		{Policy{{Match: on("git", "push", "ref", "refs/heads/main"), Then: deny}, {Then: ask}}, []string{StepGitPush}, []string{StepGitPush}},
		{Policy{{Match: &Match{System: "git"}, Then: Then{Mode: ThenWithhold}}, {Match: &Match{System: "git"}, Then: allow}, {Match: &Match{System: "commands"}, Then: ask}, {Then: allow}}, []string{StepCommandsRun}, []string{StepCommandsRun}},
	} {
		var provided, asked []string
		for _, s := range steps {
			if !tc.p.Provides(s) {
				continue
			}
			provided = append(provided, s)
			if row, _ := tc.p.Decide(Step{Name: s}); row.Then.Mode == ThenAsk {
				asked = append(asked, s)
			}
		}
		if strings.Join(provided, ",") != strings.Join(tc.provided, ",") || strings.Join(asked, ",") != strings.Join(tc.asks, ",") {
			t.Errorf("%+v provides %v and asks %v, want %v and %v", tc.p, provided, asked, tc.provided, tc.asks)
		}
	}
	if p := (Policy{{Then: allow}, {Then: ask}}); p.Default() != ThenAsk || p.MayAsk(StepGitPush) {
		t.Errorf("the dial reads %s, may ask %v", p.Default(), p.MayAsk(StepGitPush))
	}
	p := Policy{{Match: on("git", "push", "ref", "refs/heads/verse/*"), Then: allow}, {Match: on("git", "push"), Then: ask}, {Then: allow}}
	if !p.MayAsk(StepGitPush) || p.MayAsk(StepCommandsRun) {
		t.Errorf("may ask: push %v, run %v", p.MayAsk(StepGitPush), p.MayAsk(StepCommandsRun))
	}
}

// A VERB NO ROW NAMES IS REFUSED, AND NOBODY IS ASKED: the default row decides only the verbs the
// rows name, so `then: allow` last does not let an unnamed one through.
func TestAnUnnamedVerbIsRefused(t *testing.T) {
	asked := 0
	sup := NewSupervisor(Policy{{Match: on("files", "write"), Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAllow}}},
		resolverFunc(func(GateQuestion) Resolution { asked++; return Resolution{Choice: Proceed, Proceed: true} }))
	res, err := sup.Ask(context.Background(), Step{Name: StepCommandsRun, Fields: map[string]string{"command": "ls"}}, ToolUse{Name: "run_bash"})
	if err != nil || res.Proceed || res.By != ByPolicy || !strings.Contains(res.Note, "not provided") || asked != 0 {
		t.Fatalf("an unnamed verb: %+v %v, asked %d; want refused by the policy, nobody asked", res, err, asked)
	}
	if res, err := sup.Ask(context.Background(), Step{Name: StepFilesWrite, Fields: map[string]string{"path": "a"}}, ToolUse{Name: "write_file"}); err != nil || !res.Proceed || asked != 1 {
		t.Fatalf("a named verb: %+v %v, asked %d; want it asked and let go", res, err, asked)
	}
	if (Policy{{Then: Then{Mode: ThenAllow}}}).Possible(Step{Name: StepGitPush}) {
		t.Error("a push is possible under a policy that names no verb")
	}
}

// A row that names a step says how that step is asked; a step no row names is decided by the
// default row, and the question names the row by its name or its place.
func TestARowSaysHowAStepIsAsked(t *testing.T) {
	p := Policy{{Name: "release", Match: on("git", "push"), Then: Then{Mode: ThenAsk, Ask: &Ask{Need: 2}}}, {Then: Then{Mode: ThenAsk}}}
	if row, i := p.Decide(Step{Name: StepGitPush}); p.NameOf(i) != "release" || row.Then.Ask.Need != 2 {
		t.Errorf("a push is decided by %s %+v, want the release row", p.NameOf(i), row)
	}
	if row, i := p.Decide(Step{Name: StepCommandsRun}); p.NameOf(i) != "policy[1]" || row.Then.Mode != ThenAsk {
		t.Errorf("a command is decided by %s %+v, want the default row", p.NameOf(i), row)
	}
	// The decision no row made has a name a question can carry and Named can resolve, and
	// that no row can be given.
	first := Policy{p[0]}
	if row, i := first.Decide(Step{Name: StepCommandsRun}); i != -1 || p.NameOf(i) != "policy[default]" || row.Then.Mode != DefaultPolicy()[0].Then.Mode {
		t.Errorf("a call no row matches is decided by %s %+v, want policy[default] as DefaultPolicy decides", p.NameOf(i), row)
	}
	if r, ok := p.Named("policy[default]"); !ok || r.Then.Mode != DefaultPolicy()[0].Then.Mode {
		t.Errorf("Named(policy[default]) = %+v %v, want DefaultPolicy's row", r, ok)
	}
	bracketed := Policy{{Name: "policy[default]", Then: Then{Mode: ThenAllow}}}
	if err := bracketed.Check(); err == nil {
		t.Error("a row named policy[default] was accepted")
	}
	if r, ok := p.Named("policy[1]"); !ok || r.Match != nil {
		t.Errorf("named policy[1]: %+v %v", r, ok)
	}
}

// A DENY ROW REFUSES WITHOUT ASKING: the step is refused in the row's name, nobody is asked, and
// the task goes on.
func TestADenyRowRefusesWithoutAsking(t *testing.T) {
	asked := false
	sup := NewSupervisor(Policy{
		{Name: "no-rm", Match: on("commands", "run", "command", "rm *"), Then: Then{Mode: ThenDeny}},
		{Then: Then{Mode: ThenAllow}},
	}, resolverFunc(func(GateQuestion) Resolution { asked = true; return Resolution{Choice: Proceed, Proceed: true} }))
	res, err := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "rm -rf /"}})
	if err != nil || res.Proceed || res.Choice != Deny || res.By != ByPolicy || res.Cast() || asked ||
		!strings.Contains(res.Note, "denied by the policy's row no-rm") || res.Question.Gate != "no-rm" {
		t.Fatalf("Check = %+v, %v (asked %v)", res, err, asked)
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}); !res.Proceed || res.By != ByAuto {
		t.Errorf("ls = %+v, want it allowed by the default row", res)
	}
}

// The first row that matches is the one that asks, and its question carries what the row says:
// how many approvals settle it, how long it stands, and who is asked.
func TestTheFirstMatchingRowAsksItsQuestion(t *testing.T) {
	g := Policy{
		{Name: "migrations", Match: on("files", "write", "path", "db/**"),
			Then: Then{Mode: ThenAsk, Ask: &Ask{Need: 2, Within: "24h", Answerers: []string{"group:dba"}}}},
		{Name: "any-write", Match: on("files", "write"), Then: Then{Mode: ThenAsk}},
		{Then: Then{Mode: ThenAllow}},
	}
	var posed GateQuestion
	sup := NewSupervisor(g, resolverFunc(func(q GateQuestion) Resolution {
		posed = q
		return Resolution{Choice: Proceed, Proceed: true, By: "user:dba"}
	}))
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
	// A verb no row names is not provided: refused, whatever the default row says.
	res, _ = sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}})
	if res.Proceed || res.By != ByPolicy {
		t.Errorf("an unnamed command = %+v, want it refused by the policy", res)
	}
}

// Replacing the policy takes effect from the next step, for a sub-agent's supervisor too.
func TestReplacedPolicyAppliesFromTheNextStep(t *testing.T) {
	asked := 0
	sup := NewSupervisor(allowAll(), resolverFunc(func(GateQuestion) Resolution {
		asked++
		return Resolution{Choice: Deny, By: "user:x"}
	}))
	child := sup.child()
	run := ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}
	if res, _ := child.Check(context.Background(), run); !res.Proceed {
		t.Fatalf("before: %+v", res)
	}
	sup.SetPolicy(Policy{{Name: "cmd", Match: on("commands", "run"), Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAllow}}})
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
		p    Policy
		says string
	}{
		{Policy{{Then: Then{Mode: "sometimes"}}}, "not one of"},
		{Policy{{Match: on("commands", "run")}, {Then: ask}}, "then:"},
		{Policy{{Match: on("deploy", "now"), Then: ask}, {Then: ask}}, "not a system"},
		{Policy{{Match: on("files", "run"), Then: ask}, {Then: ask}}, "no verb"},
		{Policy{{Match: &Match{Verb: "push"}, Then: ask}, {Then: ask}}, "names its system"},
		{Policy{{Match: on("files", "write", "command", "ls"), Then: ask}, {Then: ask}}, "match.command: not a param"},
		{Policy{{Match: &Match{Params: map[string]string{"ref": "main"}}, Then: ask}, {Then: ask}}, "names its system"},
		{Policy{{Match: &Match{Verb: "close", Params: map[string]string{"issue": "eng-42"}}, Then: Then{Mode: ThenDeny}}, {Then: allow}}, "names its system"},
		{Policy{{Match: on("git", "push", "ref", "$subject.head"), Then: ask}, {Then: ask}}, "not bound"},
		{Policy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Need: -1}}}, {Then: ask}}, "need"},
		{Policy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Within: "soon"}}}, {Then: ask}}, "within"},
		{Policy{{Match: on("commands", "run"), Then: Then{Mode: ThenAllow, Ask: &Ask{Need: 2}}}, {Then: ask}}, "goes with `ask`"},
		{Policy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Labels: map[string]string{"a b": "c"}}}}, {Then: ask}}, "labels"},
		{Policy{{Match: on("commands", "run"), Then: Then{Mode: ThenAsk, Ask: &Ask{Within: "1m", Default: "allow"}}}, {Then: ask}}, "not one of the question's answers, proceed or deny"},
		{Policy{{Match: on("commands", "run"), Then: ask}}, "ends with its default row"},
		{Policy{{Then: allow}, {Match: on("commands", "run"), Then: ask}, {Then: ask}}, "never decide"},
		{Policy{{Name: "two words", Then: ask}}, "a word"},
		{Policy{{Name: "turn", Match: on("commands", "run"), Then: ask}, {Then: ask}}, "no row's"},
		{Policy{{Name: "release", Match: on("slack", "post"), Then: ask}, {Name: "release", Match: on("git", "push"), Then: ask}, {Then: ask}}, "is policy[0]'s"},
		{Policy{{Name: "question-ask", Match: on("commands", "run"), Then: ask}, {Then: ask}}, "no row's"},
		{Policy{{Match: on("commands", "run", "command", "rm *"), Then: Then{Mode: ThenWithhold}}, {Then: ask}}, "withhold names a system or a verb, not a call"},
		{Policy{{Match: &Match{Verb: "push"}, Then: Then{Mode: ThenWithhold}}, {Then: ask}}, "withhold names a system or a verb"},
		{Policy{{Then: Then{Mode: ThenWithhold}}}, "withhold names a system or a verb"},
		{Policy{{Match: on("git", "push", "delete", "True"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "true or false"},
		{Policy{{Match: on("git", "push", "ref", "main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "a full ref (refs/heads/main)"},
		{Policy{{Match: on("git", "push", "ref", "*", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Policy{{Match: on("git", "push", "ref", "*/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Policy{{Match: on("git", "push", "ref", "refs/*"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "does not cross its `/`"},
		{Policy{{Match: on("git", "push", "delete", ""), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "never empty"},
		{Policy{{Match: on("git", "push", "repo", "acme/app"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "`host/owner/name`"},
		{Policy{{Match: on("github", "create-pr", "repo", "github.com/Acme/App"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "lowercase (github.com/acme/app)"},
		{Policy{{Match: on("github", "create-pr", "base", "refs/heads/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "a branch's name (main)"},
		{Policy{{Match: on("linear-issue", "create", "team", "eng"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "uppercase (ENG)"},
		{Policy{{Match: on("slack", "post", "channel", "#Deploys"), Then: Then{Mode: ThenDeny}}, {Then: allow}}, "lowercase, without a `#` (deploys)"},
	} {
		if err := tc.p.Check(); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%+v: %v, want an error saying %q", tc.p, err, tc.says)
		}
	}
	for _, ok := range []Policy{
		{},
		{{Then: ask}},
		{{Match: on("git", "push", "repo", "**"), Then: allow}, {Then: ask}},
		{{Match: &Match{System: "git", Params: map[string]string{"ref": "refs/heads/main"}}, Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "**", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "refs/heads/*", "delete", "true"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("git", "push", "ref", "*/heads/main"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("github", "update-pr", "base", ""), Then: allow}, {Then: ask}}, // an update that keeps its base
		{{Match: on("git", "push", "force", "*"), Then: Then{Mode: ThenDeny}}, {Then: allow}},
		{{Match: on("commands", ""), Then: Then{Mode: ThenWithhold}}, {Match: on("slack", "react"), Then: Then{Mode: ThenWithhold}}, {Then: allow}},
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
		if q.Gate != "policy[0]" || q.Step.Name != StepCommandsRun || q.Step.Fields["command"] != "work" {
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
	if err := p.SetPolicy(Policy{{Then: Then{Mode: "sometimes"}}}, "user:ana"); err == nil {
		t.Fatal("an invalid policy was installed")
	}
	if err := p.SetPolicy(askOn("deploy"), "user:ana"); err != nil {
		t.Fatal(err)
	}
	if g := p.sup.Policy(); len(g) != 2 || g[0].Match.Params["command"] != "deploy" {
		t.Fatalf("policy now %+v", g)
	}
	e := <-events
	if e.Kind != EventPolicy || e.By != "user:ana" || e.Text != "2 row(s), default allow" {
		t.Fatalf("event %+v", e)
	}
}

// An ended task's policy is not replaced, and nothing is said after its end.
func TestSetPolicyOnAnEndedTask(t *testing.T) {
	p := NewTask("t", toolSup(allowAll(), AutoResolver{}), &scriptDriver{}, &recExec{})
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.SetPolicy(askOn("x"), "user:ana"); !errors.Is(err, ErrEnded) {
		t.Fatalf("SetPolicy = %v, want ErrEnded", err)
	}
	if g := p.sup.Policy(); len(g) != len(allowAll()) {
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

// THE RUNNER'S CEILING IS READ AFTER THE POLICY, AT EACH STEP: a step the ceiling's deny row
// matches is refused in the ceiling's name though the policy allowed it or would have asked,
// and nobody is asked; a step the ceiling lets through is decided by the policy as before; a
// sub-agent's supervisor shares the ceiling.
func TestTheCeilingIsReadAfterThePolicy(t *testing.T) {
	asked := 0
	sup := NewSupervisor(Policy{
		{Name: "ship", Match: on("commands", "run", "command", "make ship*"), Then: Then{Mode: ThenAsk}},
		{Match: &Match{System: "files"}, Then: Then{Mode: ThenAllow}},
		{Then: Then{Mode: ThenAllow}},
	}, resolverFunc(func(GateQuestion) Resolution { asked++; return Resolution{Choice: Proceed, Proceed: true} }))
	sup.SetCeilings(map[string]Policy{"commands": {
		{Name: "no-ship", Match: on("commands", "run", "command", "make ship*"), Then: Then{Mode: ThenDeny}},
		{Name: "no-rm", Match: on("commands", "run", "command", "rm *"), Then: Then{Mode: ThenDeny}},
		{Then: Then{Mode: ThenAllow}},
	}})
	for _, c := range []struct{ command, row string }{{"make ship v1", "no-ship"}, {"rm -rf /", "no-rm"}} {
		res, err := sup.child().Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": c.command}})
		if err != nil || res.Proceed || res.Choice != Deny || res.By != ByCeiling || asked != 0 ||
			!strings.Contains(res.Note, "refused by the runner's ceiling, its row "+c.row+" for commands") {
			t.Fatalf("%s: Check = %+v, %v (asked %d)", c.command, res, err, asked)
		}
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}}); !res.Proceed || res.By != ByAuto {
		t.Errorf("ls = %+v, want it allowed by the default row", res)
	}
	if res, _ := sup.Check(context.Background(), ToolUse{Name: "write_file", Input: map[string]any{"path": "x"}}); !res.Proceed {
		t.Errorf("a step of a system with no ceiling = %+v, want it allowed", res)
	}
}

// A WITHHOLD IS THE VERB'S ABSENCE: a step of a withheld verb is refused in the row's name and
// nobody is asked (the tool was never offered, so the call is a mistake); the policy says what
// it withholds; and a replacement keeps that set exactly, since what is withheld is fixed when
// the run starts.
func TestAWithholdIsTheVerbsAbsence(t *testing.T) {
	asked := false
	withhold := Then{Mode: ThenWithhold}
	p := Policy{
		{Name: "no-shell", Match: on("commands", ""), Then: withhold},
		{Match: on("slack", "react"), Then: withhold},
		{Match: on("slack", "react"), Then: withhold},
		{Then: Then{Mode: ThenAsk}},
	}
	sup := NewSupervisor(p, resolverFunc(func(GateQuestion) Resolution { asked = true; return Resolution{Choice: Proceed, Proceed: true} }))
	res, err := sup.Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}})
	if err != nil || res.Proceed || res.Choice != Deny || res.By != ByPolicy || asked || !strings.Contains(res.Note, "withheld by the policy's row no-shell") {
		t.Fatalf("Check = %+v, %v (asked %v)", res, err, asked)
	}
	// A withhold holds whatever its place: a row above it that allows the call does not bring
	// the verb back.
	above := Policy{
		{Match: on("commands", "run", "command", "ls"), Then: Then{Mode: ThenAllow}},
		{Name: "no-shell", Match: on("commands", ""), Then: withhold},
		{Then: Then{Mode: ThenAllow}},
	}
	res, err = NewSupervisor(above, resolverFunc(func(GateQuestion) Resolution { asked = true; return Resolution{Choice: Proceed, Proceed: true} })).
		Check(context.Background(), ToolUse{Name: "run_bash", Input: map[string]any{"command": "ls"}})
	if err != nil || res.Proceed || res.Choice != Deny || asked || !strings.Contains(res.Note, "withheld by the policy's row no-shell") {
		t.Fatalf("an allow above the withhold: Check = %+v, %v (asked %v)", res, err, asked)
	}
	if !p.Withholds(StepCommandsRun) || !p.Withholds(StepSlackReact) || p.Withholds(StepSlackPost) || p.Withholds(StepFilesWrite) {
		t.Errorf("withholds: commands.run %v, slack.react %v, slack.post %v, files.write %v",
			p.Withholds(StepCommandsRun), p.Withholds(StepSlackReact), p.Withholds(StepSlackPost), p.Withholds(StepFilesWrite))
	}
	if got := strings.Join(p.Withheld(), " "); got != "commands slack.react" {
		t.Errorf("withheld: %q", got)
	}
	same := Policy{{Match: on("slack", "react"), Then: withhold}, {Match: on("commands", ""), Then: withhold}, {Then: Then{Mode: ThenAllow}}}
	if err := same.KeepsWithheld(p); err != nil {
		t.Errorf("the same set, in another order and under another dial: %v", err)
	}
	for _, bad := range []Policy{
		{{Match: on("commands", ""), Then: withhold}, {Then: Then{Mode: ThenAllow}}},
		{{Match: on("commands", ""), Then: withhold}, {Match: on("slack", "react"), Then: withhold}, {Match: on("agent", ""), Then: withhold}, {Then: Then{Mode: ThenAllow}}},
		{{Then: Then{Mode: ThenAllow}}},
	} {
		if err := bad.KeepsWithheld(p); err == nil || !strings.Contains(err.Error(), "fixed when the run starts") {
			t.Errorf("%v: %v, want it refused", bad.Withheld(), err)
		}
	}
	if err := (Policy{}).KeepsWithheld(Policy{}); err != nil {
		t.Errorf("nothing withheld on either side: %v", err)
	}
}

// A REPLACEMENT RE-DECIDES THE VERBS THE TASK STARTED WITH, AND ADDS NONE. A row naming a system
// names each of its verbs and the default row names none, so a replacement may narrow, re-decide
// or drop what the policy named, and may not name more.
func TestAReplacementNamesNoVerbItsPolicyDidNot(t *testing.T) {
	allow, deny := Then{Mode: ThenAllow}, Then{Mode: ThenDeny}
	p := Policy{{Match: on("git", "push"), Then: deny}, {Match: on("commands", ""), Then: Then{Mode: ThenAsk}}, {Then: Then{Mode: ThenAsk}}}
	if got := strings.Join(p.Verbs(), " "); got != "commands.run git.push" {
		t.Errorf("verbs: %q", got)
	}
	for _, ok := range []Policy{
		{{Then: allow}},
		{{Match: &Match{System: "git", Verb: "push", Params: map[string]string{"ref": "refs/heads/main"}}, Then: deny}, {Match: on("git", "push"), Then: allow}, {Then: allow}},
		{{Match: on("commands", "run"), Then: allow}, {Then: Then{Mode: ThenAsk}}},
	} {
		if err := ok.KeepsVerbs(p); err != nil {
			t.Errorf("%v: %v, want it kept", ok.Verbs(), err)
		}
	}
	for _, bad := range []Policy{
		{{Match: on("git", "fetch"), Then: deny}, {Then: allow}},
		{{Match: on("git", ""), Then: deny}, {Then: allow}},
		{{Match: &Match{Verb: "push"}, Then: deny}, {Then: allow}}, // git-issue.push too
	} {
		if err := bad.KeepsVerbs(p); err == nil || !strings.Contains(err.Error(), "adds none") {
			t.Errorf("%v: %v, want it refused", bad.Verbs(), err)
		}
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
	var p Policy
	if err := codec.Unmarshal([]byte("["+src+", {then: allow}]"), &p, codec.Tony); err == nil {
		t.Errorf("read as %+v, want the misspelling refused", p[0].Then.Ask)
	}
}

// A WITHHELD VERB IS NOT POSSIBLE, wherever its row stands: the brief does not tell an agent its
// work leaves by a push it has no tool for, and nothing is held back for one.
func TestAWithheldVerbIsNotPossible(t *testing.T) {
	push := Step{Name: StepGitPush, Fields: map[string]string{"repo": "github.com/acme/app"}}
	for _, p := range []Policy{
		{{Match: on("git", ""), Then: Then{Mode: ThenWithhold}}, {Then: Then{Mode: ThenAllow}}},
		{{Match: on("git", "push"), Then: Then{Mode: ThenAllow}}, {Match: on("git", "push"), Then: Then{Mode: ThenWithhold}}, {Then: Then{Mode: ThenAllow}}},
	} {
		if p.Possible(push) {
			t.Errorf("%+v: a push is possible, want it refused by the withhold", p)
		}
	}
	if !(Policy{{Match: on("commands", ""), Then: Then{Mode: ThenWithhold}}, {Match: on("git", ""), Then: Then{Mode: ThenAllow}}, {Then: Then{Mode: ThenAllow}}}).Possible(push) {
		t.Error("withholding commands refuses a push")
	}
}
