package launch

import (
	"testing"

	tony "github.com/signadot/tony-format/go-tony"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

func TestBindExpandsSubject(t *testing.T) {
	s := Spec{
		Doer:   runner.Doer{Agent: "verse", Script: ""},
		Prompt: "review $subject and report",
		Runner: "verse.runners.$subject",
		Policy: task.Policy{{Then: task.Then{Mode: task.ThenAllow}}},
	}
	got := s.Bind(Binding{"subject": `demo.pr."1"`})

	if got.Prompt != `review demo.pr."1" and report` {
		t.Fatalf("Prompt = %q", got.Prompt)
	}
	if got.Runner != `verse.runners.demo.pr."1"` {
		t.Fatalf("Runner = %q", got.Runner)
	}
	if got.Doer.Agent != "verse" || got.Policy.Default() != task.ThenAllow {
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
// environment, bound there, and the script's own `$b` is the shell's. The doer's annotations and
// the autonomy are words from fixed sets, chosen by the charter: not expanded either.
func TestBindLeavesTheScriptAndTheWordsAndBindsTheEnvironment(t *testing.T) {
	const script = `for b in x; do verse-call-tool git-push -branch "$BRANCH"; echo "$b"; done`
	s := Spec{Doer: runner.Doer{Script: script,
		With:        runner.With{Env: map[string]string{"BRANCH": "$subject.head"}, Model: "$model"},
		Annotations: map[string]string{"verse.portkey/virtual-key": "$sneaky"}},
		Policy: task.Policy{{Match: &task.Match{System: "git", Verb: "push", Params: map[string]string{"ref": "$sneaky"}}, Then: task.Then{Mode: task.ThenAsk, Ask: &task.Ask{Answerers: []string{"$sneaky"}}}}, {Then: task.Then{Mode: task.ThenAsk}}}}
	got := s.Bind(Binding{"subject.head": "x;curl evil", "b": "BOUND", "sneaky": "auto", "model": "large"})
	if got.Doer.Script != script {
		t.Fatalf("Script = %q, want the text as written", got.Doer.Script)
	}
	if got.Doer.With.Env["BRANCH"] != "x;curl evil" {
		t.Fatalf("env = %v, want the value bound into the variable", got.Doer.With.Env)
	}
	if got.Doer.With.Model != "large" {
		t.Fatalf("model = %q, want it bound", got.Doer.With.Model)
	}
	if got.Doer.Annotations["verse.portkey/virtual-key"] != "$sneaky" || got.Policy[0].Match.Params["ref"] != "$sneaky" ||
		got.Policy[0].Then.Ask.Answerers[0] != "$sneaky" {
		t.Fatalf("a word was expanded: %+v", got)
	}
	got.Doer.Annotations["verse.portkey/virtual-key"] = "changed"
	if s.Doer.Annotations["verse.portkey/virtual-key"] != "$sneaky" {
		t.Fatal("Bind aliased the receiver's annotations")
	}
}

func TestAnswerContractIsADocumentWithItsOperators(t *testing.T) {
	const src = "doer: {agent: verse}\nanswer:\n  severity: !or [low, high]\n  note: !irtype string\n"
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
