package launch

import (
	"strings"
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

func TestBindExpandsSubject(t *testing.T) {
	s := Spec{
		Agent:    &runner.Agent{Name: "verse"},
		Prompt:   "review $subject and report",
		Autonomy: task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}},
	}
	got := s.Bind(Binding{"subject": `demo.pr."1"`})

	if got.Prompt != `review demo.pr."1" and report` {
		t.Fatalf("Prompt = %q", got.Prompt)
	}
	if got.Agent.Name != "verse" || got.Autonomy.Default() != task.ThenAllow {
		t.Fatalf("plain fields changed: %+v", got)
	}
}

func TestBindLeavesUnboundVarsAndIsPure(t *testing.T) {
	s := Spec{Prompt: "do $subject for $owner"}
	got := s.Bind(Binding{"subject": "x.y.1"}) // no "owner" binding
	if got.Prompt != "do x.y.1 for $owner" {
		t.Fatalf("unbound var not preserved: %q", got.Prompt)
	}
	if s.Prompt != "do $subject for $owner" {
		t.Fatalf("Bind mutated the receiver: %q", s.Prompt)
	}
}

func TestBindLongestKeyFirst(t *testing.T) {
	s := Spec{Prompt: "$subject vs $sub"}
	got := s.Bind(Binding{"sub": "SHORT", "subject": "LONG"})
	if got.Prompt != "LONG vs SHORT" {
		t.Fatalf("prefix collision: %q", got.Prompt)
	}
}

// A SCRIPT'S TEXT IS NOT BOUND: it is a program, and a value spliced into it would be part of
// the program — a branch named `x;curl …` would run. The value reaches the script as its
// variables, bound there, and the script's own `$b` is the shell's. The agent's annotations, the
// shapes and the autonomy are words from fixed sets, chosen by the charter: not expanded either;
// nor is anything of the network.
func TestBindLeavesTheScriptAndTheWordsAndBindsTheEnvironment(t *testing.T) {
	const script = `for b in x; do verse-call-tool git-push -branch "$BRANCH"; echo "$b"; done`
	s := Spec{Script: script,
		Agent: &runner.Agent{Name: "verse", Settings: map[string]string{"model": "$model"}, Annotations: map[string]string{"verse.portkey/virtual-key": "$sneaky"}},
		Environment: systems.Environment{
			Vars: map[string]string{"BRANCH": "$subject.head"},
			Systems: systems.Systems{
				"git":     {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{"push": {{"ref": ir.FromString("refs/heads/$sneaky")}}}}},
				"network": {{Attributes: map[string]string{"mode": "$sneaky"}, Verbs: map[string][]systems.Shape{"tcp-client": {{"host": ir.FromString("$sneaky")}}}}},
			},
		},
		Autonomy: task.Autonomy{{Match: &task.Match{System: "git", Verb: "push", Params: map[string]*ir.Node{"ref": ir.FromString("$sneaky")}}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Answerers: []string{"$sneaky"}}}}, {Then: task.Then{Mode: task.ThenAsk}}}}
	got := s.Bind(Binding{"subject.head": "x;curl evil", "b": "BOUND", "sneaky": "auto", "model": "large"})
	if got.Script != script {
		t.Fatalf("Script = %q, want the text as written", got.Script)
	}
	if got.Environment.Vars["BRANCH"] != "x;curl evil" {
		t.Fatalf("vars = %v, want the value bound into the variable", got.Environment.Vars)
	}
	if got.Agent.Model() != "large" {
		t.Fatalf("model = %q, want it bound", got.Agent.Model())
	}
	net := got.Environment.Systems["network"][0]
	if got.Agent.Annotations["verse.portkey/virtual-key"] != "$sneaky" || systems.Words(got.Autonomy[0].Match.Params["ref"]) != "$sneaky" ||
		got.Autonomy[0].Then.Ask.Answerers[0] != "$sneaky" || systems.Words(got.Environment.Systems["git"][0].Verbs["push"][0]["ref"]) != "refs/heads/$sneaky" ||
		net.Attributes["mode"] != "$sneaky" || systems.Words(net.Verbs["tcp-client"][0]["host"]) != "$sneaky" {
		t.Fatalf("a word was expanded: %+v", got)
	}
	got.Agent.Annotations["verse.portkey/virtual-key"] = "changed"
	got.Environment.Systems["git"][0].Verbs["push"][0]["ref"] = ir.FromString("changed")
	if s.Agent.Annotations["verse.portkey/virtual-key"] != "$sneaky" || systems.Words(s.Environment.Systems["git"][0].Verbs["push"][0]["ref"]) != "refs/heads/$sneaky" {
		t.Fatal("Bind aliased the receiver's annotations or shapes")
	}
}

func TestAnswerContractIsADocumentWithItsOperators(t *testing.T) {
	const src = "agent: {name: verse}\nanswer:\n  severity: !or [low, high]\n  note: !irtype string\n"
	var s Spec
	if err := codec.Unmarshal([]byte(src), &s, codec.Tony); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !matches(t, s.Answer, `{severity: low, note: "x"}`) {
		t.Fatal("the contract does not accept a document it describes")
	}
	if matches(t, s.Answer, `{severity: nope, note: "x"}`) {
		t.Fatal("the contract accepts a severity outside its !or")
	}

	out, err := codec.Marshal(&s, codec.Tony)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var round Spec
	if err := codec.Unmarshal(out, &round, codec.Tony); err != nil {
		t.Fatalf("re-decode %q: %v", out, err)
	}
	if !matches(t, round.Answer, `{severity: high, note: "x"}`) || matches(t, round.Answer, `{severity: nope}`) {
		t.Fatalf("round trip lost the operator: %q", out)
	}
}

func TestBindCarriesTheAnswerContract(t *testing.T) {
	pat, _ := codec.ParseNode([]byte("severity: !or [low, high]"), codec.Tony)
	got := Spec{Answer: pat}.Bind(Binding{"subject": `demo.pr."1"`})
	if !matches(t, got.Answer, `{severity: low}`) || matches(t, got.Answer, `{severity: nope}`) {
		t.Fatalf("Answer = %v, want the pattern as written", got.Answer)
	}
}

func matches(t *testing.T, pattern *ir.Node, doc string) bool {
	t.Helper()
	d, err := codec.ParseNode([]byte(doc), codec.Tony)
	if err != nil {
		t.Fatalf("parse %q: %v", doc, err)
	}
	ok, err := tony.Match(d, pattern)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	return ok
}

// A SPAWN WRITTEN BY HAND BINDS NOTHING: a `$` in a field that names something is refused, since
// nothing fires to bind it; in the prompt and the vars, prose and shell values, it is left as
// written; and what only a firing sets is refused, the first of them in a fixed order.
func TestASpawnWrittenByHand(t *testing.T) {
	git := func(repo string) systems.Environment {
		return systems.Environment{Systems: systems.Systems{"git": {{Attributes: map[string]string{"repo": repo}}}}}
	}
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Prompt: "fix `echo $HOME` in the README; it costs $5"}, ""},
		{Spec{Script: "echo \"$P\"", Environment: systems.Environment{Vars: map[string]string{"P": "$HOME"}}}, ""},
		{Spec{Prompt: "go", Subject: "$subject"}, "subject: a `$`"},
		{Spec{Prompt: "go", Agent: &runner.Agent{Settings: map[string]string{"model": "$model"}}}, "agent.model: a `$`"},
		{Spec{Prompt: "go", Environment: git("$subject.value.repo")}, "environment.systems.git[0].repo: a `$`"},
		{Spec{Prompt: "go", Chain: "review", Passed: []Passed{{How: PassedAuto}}}, "chain: a firing's"},
	} {
		err := c.spec.CheckByHand()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v, want %q", c.spec, err, c.want)
		}
	}
}
