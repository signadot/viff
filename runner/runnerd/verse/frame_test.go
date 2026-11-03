package verse

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// The defect this file exists for, stated as a test.
//
// The old package-level prompt said "prefer call_agent to run them in parallel", while the demo
// vector ten lines below it in the same file ASK-gated the call-agent verb. So the standing
// instruction recommended the one action guaranteed to suspend the loop until a human
// answered — which for an unattended trigger firing means park, indefinitely.
//
// The fix is not better wording, because a constant cannot track a per-launch gating. The frame
// is derived from the gating it will actually run under, so the two cannot disagree: whatever a
// gate may stop is described as stopping.
func TestDefaultFrameNeverRecommendsWhatAGateStops(t *testing.T) {
	vec := gatesOnEveryStep()
	tds, err := selectTools(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, vec, false, nil)

	if strings.Contains(strings.ToLower(frame), "prefer call_agent") {
		t.Error("the frame recommends call_agent while a gate stops it")
	}
	for _, name := range toolNames(tds) {
		if name == yieldTool || !mayAsk(name, vec, nil) {
			continue
		}
		// A gated tool must be described as gated. Being absent from the frame would also
		// avoid the contradiction, so assert the positive: the agent is told the cost.
		if !strings.Contains(frame, name) {
			t.Errorf("%s is gated but the frame does not mention it at all", name)
		}
		idx := strings.Index(frame, "STOP the loop")
		if idx < 0 || !strings.Contains(frame[idx:], name) {
			t.Errorf("%s is gated but is not listed among the tools that stop the loop", name)
		}
	}
}

// The mirror of the above: with no gates nothing is described as stopping the
// loop. Without this, a frame that called every tool gated would pass the test above while
// being just as wrong in the other direction.
func TestDefaultFrameDescribesNoGatesAsUngated(t *testing.T) {
	tds, err := selectTools(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, task.Autonomy{{Then: task.Then{Mode: task.ThenAllow}}}, false, nil)
	if strings.Contains(frame, "STOP the loop") {
		t.Error("no tool is gated, so nothing should be described as stopping the loop")
	}
	for _, name := range toolNames(tds) {
		if name != yieldTool && !strings.Contains(frame, name) {
			t.Errorf("%s runs freely but is not named in the frame", name)
		}
	}
}

// The other half of the complaint. The old prompt ended "stop with a one-paragraph
// summary" — while trigger/deref.go is built to resolve `.[result.findings[0].severity]`
// structurally out of that same final message. The prompt won, and structured answers came
// back as prose. The default must not ask for a summary, and must say what the last
// message is FOR, so a task that names a shape is not competing with the frame.
func TestDefaultFrameDoesNotMandateAProseSummary(t *testing.T) {
	tds, _ := selectTools(nil, nil)
	frame := defaultFrame(tds, gatesOnEveryStep(), false, nil)
	low := strings.ToLower(frame)

	for _, banned := range []string{"one-paragraph summary", "paragraph summary", "summarize what you did"} {
		if strings.Contains(low, banned) {
			t.Errorf("the frame still asks for %q, which is what $result has to parse", banned)
		}
	}
	if !strings.Contains(low, "final message") {
		t.Error("the frame must say what the final message is for — it is the task's result")
	}
	if !strings.Contains(low, "shape") {
		t.Error("the frame must defer to a shape the task states, or a charter cannot ask for one")
	}
}

// A frame must describe the tools this launch actually carries. The old constant listed a
// fixed five, so narrowing the tool set would have left the agent reaching for tools that
// were not in the request — the same class of defect as the gate disagreement.
func TestDefaultFrameNamesOnlyTheToolsOnOffer(t *testing.T) {
	tds, err := selectTools([]string{"read_file", "list_dir"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, gatesOnEveryStep(), false, nil)
	for _, withheld := range []string{"write_file", "run_bash", "call_agent"} {
		if strings.Contains(frame, withheld) {
			t.Errorf("the frame names %s, which this launch does not offer", withheld)
		}
	}
	for _, offered := range []string{"read_file", "list_dir"} {
		if !strings.Contains(frame, offered) {
			t.Errorf("the frame omits %s, which this launch does offer", offered)
		}
	}
}

// fail is never withheld, however narrow the requested set. It is the only structural way
// an agent reports it could not do the work — a driver that simply stops reads as SUCCESS
// downstream — so a launch that dropped it would convert every failure into a completed
// task carrying an excuse where the charter expected a value.
func TestSelectToolsAlwaysKeepsFail(t *testing.T) {
	tds, err := selectTools([]string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(tds)
	if len(names) != 2 || names[0] != "read_file" {
		t.Fatalf("tools = %v, want the requested one plus yield", names)
	}
	var found bool
	for _, n := range names {
		found = found || n == yieldTool
	}
	if !found {
		t.Errorf("tools = %v, missing %q", names, yieldTool)
	}
}

// A tool name the driver does not have is an error, not a quietly smaller toolset: the
// charter asked for a specific set, and running with a different one while saying nothing
// is precisely how `grant: read-only` came to mean nothing.
func TestSelectToolsRejectsUnknownNames(t *testing.T) {
	if _, err := selectTools([]string{"read_file", "rm_rf"}, nil); err == nil {
		t.Fatal("an unknown tool name must fail the selection")
	}
}

// And that failure must reach the task rather than being swallowed by Build's
// two-value signature.
func TestBuildFailsTheTaskOnAnUnknownTool(t *testing.T) {
	p := Build(Config{ID: "t", Prompt: "go", Tools: []string{"nope"}, Resolver: task.AutoResolver{}})
	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("run err = %v, want a failure naming the unknown tool", err)
	}
}

// The point of the whole field: a launch's frame is what the agent is actually sent. The
// old driver read a package constant, so nothing a charter wrote could reach the model.
func TestTheFrameIsWhatTheModelReceives(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		{msg: chatMessage{Role: "assistant", Content: "done"}, finish: "stop"},
	}}
	d := &llmDriver{client: fake, frame: "ANSWER WITH A TONY OBJECT", prompt: "review it", log: io.Discard}

	if _, more, err := d.Next(context.Background(), task.StepResult{}); err != nil || more {
		t.Fatalf("next: more=%v err=%v", more, err)
	}
	msgs := fake.seen[0]
	if msgs[0].Role != "system" || msgs[0].Content != "ANSWER WITH A TONY OBJECT" {
		t.Fatalf("first message = %+v, want the launch's frame as the system message", msgs[0])
	}
}

// An empty frame sends no system message rather than a blank one. A blank system turn is a
// claim about the agent's standing instructions; having none is not the same as being told
// nothing in particular.
func TestEmptyFrameSendsNoSystemMessage(t *testing.T) {
	fake := &fakeCompleter{turns: []scriptedTurn{
		{msg: chatMessage{Role: "assistant", Content: "done"}, finish: "stop"},
	}}
	d := &llmDriver{client: fake, prompt: "go", log: io.Discard}

	if _, _, err := d.Next(context.Background(), task.StepResult{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range fake.seen[0] {
		if m.Role == "system" {
			t.Fatalf("an empty frame produced a system message: %+v", m)
		}
	}
}

// THE FRAME SAYS WHAT ASKING DOES TO THE TURN, when ask is offered, and says nothing of it when
// a launch narrowed it away. A model told only that a tool exists answers a chat once and stops,
// which is the task ending with the person's reply arriving at nothing.
func TestDefaultFrameExplainsAskWhenOffered(t *testing.T) {
	with, err := selectTools(nil, []Tool{Ask(task.AutoResolver{})})
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(with, gatesOnEveryStep(), false, nil)
	for _, want := range []string{"hand them the turn", "Stopping without asking ENDS the task"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not say %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "These tools run without interruption: "+askTool) ||
		strings.Contains(frame, ", "+askTool+",") || strings.Contains(frame, ", "+askTool+".") {
		t.Errorf("ask is listed as a tool that runs without interruption; it waits on a person:\n%s", frame)
	}
	without, err := selectTools([]string{"read_file"}, []Tool{Ask(task.AutoResolver{})})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(defaultFrame(without, gatesOnEveryStep(), false, nil), "hand them the turn") {
		t.Error("the frame describes ask to a task that was not offered it")
	}
}

// IN A CONVERSATION THE FRAME SAYS THE TURN IS HANDED OVER BY STOPPING, and not by ask — the
// loop hands the person the turn whenever the model goes quiet, so a model told to ask after
// every answer would put a question in front of a person who already has the turn.
func TestDefaultFrameDescribesAConversation(t *testing.T) {
	tds, err := selectTools(nil, []Tool{Ask(task.AutoResolver{})})
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, gatesOnEveryStep(), true, nil)
	for _, want := range []string{"CONVERSATION", "say what you have to say and stop", yieldDone} {
		if !strings.Contains(frame, want) {
			t.Errorf("the conversation frame does not say %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "hand them the turn") || strings.Contains(frame, "Your final message ENDS") {
		t.Errorf("the conversation frame still tells the model that stopping ends the task:\n%s", frame)
	}
}

// gatesOnEveryStep gates each step the agent's tools can take: writes, commands and delegation.
func gatesOnEveryStep() task.Autonomy {
	return task.Autonomy{{Name: "write", Match: &task.Match{System: "files", Verb: "write"}, Then: task.Then{Mode: task.ThenAsk}}, {Name: "shell", Match: &task.Match{System: "commands", Verb: "run"}, Then: task.Then{Mode: task.ThenAsk}}, {Name: "delegate", Match: &task.Match{System: "agent", Verb: "delegate"}, Then: task.Then{Mode: task.ThenAsk}}, {Then: task.Then{Mode: task.ThenAllow}}}
}

// Under the default policy every step asks, so the frame tells the model which of its tools
// wait for an answer, with no row written.
func TestUnderAskTheFrameSaysEveryStepStops(t *testing.T) {
	tds, err := selectTools(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := defaultFrame(tds, task.DefaultAutonomy(), false, nil)
	idx := strings.Index(frame, "STOP the loop")
	if idx < 0 {
		t.Fatalf("nothing is described as stopping under ask:\n%s", frame)
	}
	for _, name := range []string{"write_file", "run_bash", "call_agent"} {
		if !strings.Contains(frame[idx:], name) {
			t.Errorf("%s asks under ask but isn't listed among the tools that stop the loop", name)
		}
	}
	if strings.Contains(frame[idx:], "read_file") {
		t.Error("read_file is no step, so it never asks, but it is listed as stopping")
	}
}

// A TOOL IS OFFERED IFF THE ENVIRONMENT GIVES ITS VERB: the agent's own tools and a runtime's
// are left out of what the model sees where no block gives their verb, whatever the autonomy
// says, while the tools that are no verb stay, `read_file` and `yield` among them.
func TestAToolIsOfferedIffTheEnvironmentGivesItsVerb(t *testing.T) {
	env := systems.Environment{Systems: systems.Systems{
		"files": {{Verbs: map[string][]systems.Shape{"write": {{}}}}},
		"slack": {{Verbs: map[string][]systems.Shape{"read": {{}}}}},
		"git":   {{Attributes: map[string]string{"repo": "acme/app"}, Verbs: map[string][]systems.Shape{"push": {{}}}}},
	}}
	extra := []Tool{{Name: "slack-post"}, {Name: "slack-read"}, {Name: "slack-react"}, {Name: "git-push"}, {Name: "question-ask"}}
	left := given(env, extra)
	if names := fmt.Sprint(toolNamesOf(left)); names != "[slack-read git-push]" {
		t.Errorf("extra tools offered: %s", names)
	}
	tds, err := selectTools(nil, left)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(toolNames(lessUngiven(tds, env)), " ")
	for _, absent := range []string{"run_bash", "call_agent", "slack-post", "slack-react", "question-ask"} {
		if strings.Contains(got, absent) {
			t.Errorf("%s is offered in an environment that does not give it: %s", absent, got)
		}
	}
	for _, present := range []string{"read_file", "write_file", "slack-read", "git-push", "yield"} {
		if !strings.Contains(got, present) {
			t.Errorf("%s is missing: %s", present, got)
		}
	}
	// An empty environment gives no verb: only the tools that are no verb are left.
	none := strings.Join(toolNames(lessUngiven(selectToolsOrDie(t, given(systems.Environment{}, extra)), systems.Environment{})), " ")
	for _, absent := range []string{"run_bash", "write_file", "call_agent", "slack-read", "git-push", "question-ask"} {
		if strings.Contains(none, absent) {
			t.Errorf("%s is offered in an empty environment: %s", absent, none)
		}
	}
	if !strings.Contains(none, "read_file") || !strings.Contains(none, "yield") {
		t.Errorf("in an empty environment, the tools that are no verb are missing: %s", none)
	}
}

func toolNamesOf(tools []Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func selectToolsOrDie(t *testing.T, extra []Tool) []toolDef {
	t.Helper()
	tds, err := selectTools(nil, extra)
	if err != nil {
		t.Fatal(err)
	}
	return tds
}
