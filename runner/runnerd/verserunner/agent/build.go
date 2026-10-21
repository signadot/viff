// Package agent assembles a real LLM agent on top of the verse task loop: an
// OpenAI-compatible Chat Completions driver (served via Portkey), a filesystem
// executor confined to a working directory, under the launch's gates. Build wires them into a Task
// with a call_agent DelegateFunc.
//
// It is an injectable driver: the daemon and the demo both build tasks via a
// Builder, so nothing in the task substrate depends on this package.
package agent

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
)

// defaultFrame is the frame used when a launch does not write one — and it is DERIVED, from
// the tools actually offered and the autonomy actually installed, because two things it says
// are things a constant cannot get right.
//
// The final assistant message is what Result() returns, which is what a completed task's
// `result` carries, which is what a charter's `$result` derefs. trigger/deref.go resolves
// $result.findings[0].severity structurally, so a frame that asked for a closing summary in
// prose would get prose where a structure is read. So it says the final message is the answer
// itself, and a launch wanting a particular shape states it in its own frame, which is what the
// field is for.
//
// What it says about each tool follows the autonomy. Advice to prefer call_agent, under an autonomy
// that asks before every delegation, recommends the one action guaranteed to stop and wait for
// a person, which for an unattended firing means park. The autonomy is per launch and replaceable
// while the task runs, and a constant is neither, so below each tool is described as it will
// actually behave.
func defaultFrame(tds []toolDef, p task.Autonomy, converse bool, extra map[string]Tool) string {
	var free, gated []string
	asks := false
	for _, td := range tds {
		name := td.Function.Name
		if name == askTool {
			asks = true
		}
		if name == yieldTool || name == askTool {
			continue // not actions on the world; each is described on its own terms
		}
		if mayAsk(name, p, extra) {
			gated = append(gated, name)
			continue
		}
		free = append(free, name)
	}

	var b strings.Builder
	b.WriteString("You are an autonomous agent working through a supervised loop.\n")
	b.WriteString("You operate inside a sandboxed working directory; all paths are relative to it.\n")
	if len(free) > 0 {
		fmt.Fprintf(&b, "These tools run without interruption: %s.\n", strings.Join(free, ", "))
	}
	if len(gated) > 0 {
		// Naming the cost is the point. An agent told that a tool suspends it until a human
		// answers reaches for it when the task needs it and not otherwise.
		fmt.Fprintf(&b, "These tools STOP the loop until a supervisor approves each use, and may be "+
			"denied: %s. Use them where the task requires it, not by preference.\n", strings.Join(gated, ", "))
	}
	fmt.Fprintf(&b, "A tool result that says DENIED is a decision, not a transient error: do not retry "+
		"the same action — take another approach, or hand control back with %s.\n", yieldHint)
	if converse {
		// A conversation: the loop hands the person the turn whenever the model stops, so the
		// model is told to stop when it has said its piece, and that ending is the person's.
		fmt.Fprintf(&b, "This task is a CONVERSATION with the person it runs for. When you stop "+
			"talking they are given the turn, and what they say arrives as your next message; "+
			"the conversation ends when they end it, or when you call %s with reason %q. So say "+
			"what you have to say and stop — you need not call %s to hand over the turn, though "+
			"you may use it to put a specific question or a choice to them.\n",
			yieldTool, yieldDone, askTool)
	} else if asks {
		// The one tool whose wait is a PERSON's, said in terms of what it does to the turn, and
		// said BEFORE the sentence about the final message: a model told first that its last
		// message is the answer, and then that it may ask, answered a "let's chat" once and
		// stopped, which ended the task with the person's reply arriving at nothing. A stop is
		// read as finished, so ending has to be the deliberate act and asking the default
		// whenever a person is on the other end.
		fmt.Fprintf(&b, "%s puts a question to the person this task runs for and waits for their "+
			"answer, which is its result; your turn pauses until it comes, and that may take a "+
			"while. Use it whenever you need a decision or information only they have. If the "+
			"task is a conversation, or the person may want to respond to what you say, DO NOT "+
			"STOP after answering: call %s with your message to hand them the turn, every time, "+
			"and end only when they say they are done. Stopping without asking ENDS the task.\n",
			askTool, askTool)
	}
	if converse {
		b.WriteString("When the conversation ends, your last message is this task's result.")
		return b.String()
	}
	b.WriteString("Your final message ENDS this task and IS its result. It is read by whatever asked " +
		"for the work — often another part of the system rather than a person — so make it the " +
		"answer itself. Do not append a summary of what you did or how you did it. If the task " +
		"states a shape for the answer, produce exactly that shape and nothing else.")
	return b.String()
}

// contractHeading opens the clause the contract adds to the frame. It is a constant because
// withoutContract cuts on it: the parent's frame is handed to children, and text describing
// a check the child does not run has to come back off exactly where it went on.
const contractHeading = "ANSWER CONTRACT."

// contractClause tells the agent, up front, the shape its final message must have and what
// happens if it does not — including the consequence, because "you will be asked to fix it,
// and a run that never matches fails" is what makes the shape worth the effort of the first
// attempt rather than the third.
func contractClause(c *answer.Contract) string {
	var b strings.Builder
	b.WriteString(contractHeading + " Your final message must be a document in this shape and " +
		"nothing else — no preamble, no commentary after it:\n\n")
	for _, line := range strings.Split(strings.TrimRight(c.Source(), "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	fmt.Fprintf(&b, "\nIt is checked the moment you stop. A mismatch comes back to you naming "+
		"exactly what is wrong, and a run that never satisfies it FAILS. If a value is not "+
		"something you were able to determine, call %s with the reason — do not invent one to "+
		"fit the shape.", yieldHint)
	return b.String()
}

// withoutContract removes the clause contractClause added, leaving the frame as it was.
func withoutContract(frame string) string {
	if i := strings.Index(frame, contractHeading); i >= 0 {
		return strings.TrimRight(frame[:i], "\n")
	}
	return frame
}

// mayAsk reports whether a tool may be stopped before it runs: it is a step, and the autonomy's
// row that would decide it is not an allow. Whether it is stopped this time depends on the
// fields, so the frame says it may.
func mayAsk(tool string, p task.Autonomy, extra map[string]Tool) bool {
	tu := task.ToolUse{Name: tool}
	if tool == "call_agent" {
		tu.CallAgent = &task.CallAgentRequest{}
	}
	st, ok := task.StepOf(tu)
	if t, isExtra := extra[tool]; isExtra {
		st, ok = task.Step{Name: t.Step}, t.Step != ""
	}
	if !ok {
		return false
	}
	return p.MayAsk(st.Name)
}

// A Tool is one action a RUNTIME adds to the agent's vocabulary, beyond the four this package
// implements: the runtime offering the AGENT something only the runtime knows how to do.
//
// The case it exists for is a coding runner's push. The runner holds the credential, the
// destinations are its environment's to declare, and neither is anything this package can know
// about — but the action has to be an ACTION, derived and gated and narrated like every other,
// or it is a capability outside the gates. Wiring it as a fifth case in fsExec would put git in
// a package that has no business knowing what git is; wiring it as a private channel would put
// it outside the loop, where no gate sees it.
//
// It is offered to CHILDREN too, on the same argument the tool set is: a sub-agent should not be
// handed a capability its parent's launch withheld, and it should not lose one its parent had.
type Tool struct {
	Name        string
	Description string
	// Params are the arguments as JSON-schema properties, and Required names the ones that must
	// be present. An argument whose values are enumerable should say so — `enum` is how the
	// vocabulary a runtime will accept becomes the vocabulary the model can write, rather than
	// something it discovers by being refused.
	Params   map[string]any
	Required []string
	// Step, when the tool is one, is the step each use of it is (git.push), and Fields that
	// use's fields: what its gates match and what a question about it says. Fields reports false
	// for a use refused before any gate, which Do then says. A tool with no Step is no step, and
	// nothing asks before it.
	Step   string
	Fields func(input map[string]any) (map[string]string, bool)
	// Do performs it once the gate has approved. out is what the model reads and what the read
	// plane narrates.
	//
	// blocked is for a REFUSAL — the far side saying no — as against the action failing. The
	// distinction is the loop's own (StepResult.Blocked: "a gate/scope refusal so the driver can
	// re-plan rather than treating it as an error") and it matters most where a refusal is the
	// ORDINARY outcome: a remote enforcing its own rules about its own history is exercising an
	// authority verse deliberately does not hold, and routing that through err would fill the
	// record with alarm about the thing working as intended.
	Do func(ctx context.Context, tu task.ToolUse) (out string, blocked bool, err error)
}

// ownSteps is the step each of the agent's own tools is: a tool is offered only where the
// environment gives its verb (systems.Environment.Gives), so the model never proposes one it lacks.
var ownSteps = map[string]string{"write_file": task.StepFilesWrite, "run_bash": task.StepCommandsRun, "call_agent": task.StepAgentCallSubAgent}

// lessUngiven is the tool definitions less the agent's own tools whose verb env does not give.
// Its tools that are no verb (`read_file`, `yield`) stay.
func lessUngiven(tds []toolDef, env systems.Environment) []toolDef {
	return slices.DeleteFunc(tds, func(td toolDef) bool {
		st, own := ownSteps[td.Function.Name]
		return own && !env.Gives(st)
	})
}

// given is the extra tools whose verb env gives; the agent's own are dropped by lessUngiven
// after selectTools.
func given(env systems.Environment, extra []Tool) []Tool {
	return slices.DeleteFunc(slices.Clone(extra), func(t Tool) bool {
		system, verb, ok := systems.SplitTool(t.Name)
		return ok && !env.Gives(systems.StepName(system, verb))
	})
}

func byName(extra []Tool) map[string]Tool {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]Tool, len(extra))
	for _, t := range extra {
		out[t.Name] = t
	}
	return out
}

// Config parameterizes Build.
type Config struct {
	ID       string        // task id
	Prompt   string        // the top-level user task (seeds the driver)
	Root     string        // working directory the executor is confined to
	Resolver task.Resolver // how ASK gates are answered (stdin, auto, Registry, …)
	Client   completer     // model client; nil → built from the LLM creds below (or env)
	Log      io.Writer     // local narration/exec log; nil → io.Discard
	// Env names the variables run_bash passes through from this process; everything else is
	// unset, and empty passes nothing. It is the caller's to declare because the caller knows
	// what this process holds — see task.ShellEnv and task.DefaultShellEnv.
	Env []string
	// Set is the variables run_bash is given outright, each `NAME=value`, beside what Env passes
	// through: what whoever built this task has to tell its shell, such as the launch's
	// `with.env` and where its credential file is.
	Set []string
	// ChildSet is what a delegated sub-agent's shell is given outright instead: the launch's
	// `with.env`, and not the credential, which is the task's alone.
	ChildSet []string

	// Frame is the agent's system prompt; empty → defaultFrame, derived from the tools
	// offered and the gates installed. Tools narrows the offered set; empty → all of
	// them. The launch carries neither, so a runner leaves both empty.
	Frame string
	Tools []string
	// Answer is this task's answer contract as a tony match pattern; nil → no contract,
	// and the run ends wherever the model stops. See package answer.
	Answer *ir.Node
	// Environment is the launch's: what the task may do at all, so which tools it is offered and
	// which steps are refused before the autonomy is read.
	Environment systems.Environment
	// Autonomy is the launch's autonomy. It is here rather than applied by the caller afterwards
	// because the frame is derived from it, so the agent is told which of its tools may be stopped.
	Autonomy task.Autonomy
	// Ceilings are the runner's, by system, read at each step after the autonomy
	// (task.Supervisor.SetCeilings).
	Ceilings map[string]systems.Ceiling

	// Extra are tools the RUNTIME adds to this agent's vocabulary, beyond the ones this
	// package implements. See Tool.
	Extra []Tool
	// Converse: the run is a conversation (the launch's `converse`), so the frame says the person
	// is handed the turn whenever the model stops, and `question-ask` is not how to hand it over.
	Converse bool

	// LLM creds for the model client, RESOLVED BY THE CALLER — versed reads them from verse
	// config (file ← env) and passes them here. Empty base URL and model take package
	// defaults; an empty key or virtual key is sent as an empty header, which the gateway
	// reports as "Invalid API Key" rather than as the missing configuration it is.
	//
	// Nothing falls back to the environment: clientFrom defaults only the two fields that have a
	// sensible default. A local caller that set LLMModel and left the keys empty spent a while
	// being told its key was invalid.
	LLMBaseURL    string
	LLMKey        string
	LLMVirtualKey string
	LLMModel      string
}

// Build assembles a Task (real LLM driver + filesystem executor, under the launch's autonomy). A
// call_agent action starts a concurrent sub-agent built the same way, under the same autonomy
// (NewChild).
func Build(cfg Config) *task.Task {
	if cfg.Client == nil {
		cfg.Client = clientFrom(cfg.LLMBaseURL, cfg.LLMKey, cfg.LLMVirtualKey, cfg.LLMModel)
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	// The harness's own fifth tool, offered on the runtime tools' terms: narrowable by a
	// launch's tools, handed to children, dispatched through the same seam. See askTool.
	cfg.Extra = append(append([]Tool{}, cfg.Extra...), Ask(cfg.Resolver))
	extra := byName(cfg.Extra)
	tools, err := selectTools(cfg.Tools, given(cfg.Environment, cfg.Extra))
	if err != nil {
		// A task that named a tool this build does not have gets a FAILED task, not a
		// quietly smaller toolset. The charter asked for a specific set; running with a
		// different one and saying nothing is how `grant: read-only` came to mean nothing.
		return task.NewFailed(cfg.ID, cfg.Resolver, "launch tools: "+err.Error())
	}
	tools = lessUngiven(tools, cfg.Environment)
	// A contract that cannot be parsed fails the task, for the reason an unknown tool
	// name does: the charter asked for something specific, and running without it while
	// saying nothing is how a field comes to mean nothing. It is parsed here as well as on
	// the engine side because a task can be launched straight at a runner (`task
	// run`, a remote POST) without ever passing through a charter.
	var contract *answer.Contract
	if cfg.Answer != nil {
		var err error
		if contract, err = answer.Parse(cfg.Answer); err != nil {
			return task.NewFailed(cfg.ID, cfg.Resolver, "answer contract: "+err.Error())
		}
	}
	sup := task.NewSupervisor(cfg.Environment, cfg.Autonomy, cfg.Resolver)
	sup.SetCeilings(cfg.Ceilings)
	sup.SetStepOf(func(tu task.ToolUse) (task.Step, bool) { return stepOf(tu, extra) })

	// The frame is derived from THIS task's tools and gates when the launch did not
	// write one, so what the agent is told matches what it will actually meet.
	frame := cfg.Frame
	if frame == "" {
		frame = defaultFrame(tools, cfg.Autonomy.OrDefault(), cfg.Converse, extra)
	}
	// The contract is stated in the frame even when the launch wrote the frame itself, and
	// appending to an author's text is otherwise not this function's business. It is
	// warranted here because this is not advice about the task: it is a fact about how the
	// loop will behave — the final message is checked and a failure comes back — and an
	// agent that meets the check for the first time as a rejection wastes a repair
	// attempt learning what it could have been told at the start.
	if contract != nil {
		frame = strings.TrimRight(frame, "\n") + "\n\n" + contractClause(contract)
	}

	driver := &llmDriver{client: cfg.Client, tools: tools, frame: frame, prompt: cfg.Prompt,
		log: cfg.Log}
	ex := &fsExec{root: cfg.Root, log: cfg.Log, extra: extra, env: cfg.Env, set: cfg.Set}
	p := task.NewTask(cfg.ID, sup, driver, ex)
	p.SetLog(cfg.Log)
	// What decides when a task is OVER is installed on the loop rather than wrapped around the
	// driver, where the only thing visible was "the model stopped talking" — which with a child
	// outstanding means the opposite of finished. See task.AnswerCheck.
	if contract != nil {
		p.SetAnswerCheck(&answer.Checker{C: contract, FailHint: yieldHint})
	}
	driver.emit = p.Emit // narration onto this task's read plane

	var kid int
	p.SetDelegate(func(parent *task.Task, req *task.CallAgentRequest) (*task.Task, error) {
		kid++
		childID := fmt.Sprintf("%s.%s%s%d", parent.ID, req.Agent, task.GateSep, kid)
		// A child inherits the parent's tool set and frame. Inheriting the tools is the
		// conservative reading of delegation — a sub-agent should not be handed a
		// capability its parent's launch withheld — and inheriting the frame keeps the
		// answer shape the charter asked for, since a child's result folds into the
		// parent's conversation and thence into what $result reads.
		//
		// It does NOT inherit the answer contract, and the asymmetry is the point. The
		// contract describes THIS task's answer — the value a charter derefs — while a
		// child answers a subtask its parent chose the wording of, and its result arrives
		// as one more tool result for the parent to read. Forcing the parent's shape onto
		// it would demand a verdict from an agent that was asked to go read a file, and
		// would fail runs over a document nothing downstream reads. A child that needs a
		// contract is a task of its own.
		//
		// (frame carries the parent's contract clause. It is text about a check the child
		// does not run, so it is trimmed rather than left to mislead.)
		childDriver := &llmDriver{client: cfg.Client, tools: tools, frame: withoutContract(frame),
			prompt: req.Prompt, log: cfg.Log}
		childExec := &fsExec{root: cfg.Root, log: cfg.Log, extra: extra, env: cfg.Env, set: cfg.ChildSet}
		child := parent.NewChild(childID, req, childDriver, childExec)
		childDriver.emit = child.Emit // child narration onto the child's read plane
		return child, nil
	})
	return p
}

// StepOf is how a supervisor classifies the uses of tools as steps (Supervisor.SetStepOf), for a
// task whose actions are those tools' uses: a script's calls are the same steps the agent's uses
// of the same tools are, so the same gates match them.
func StepOf(tools []Tool) func(task.ToolUse) (task.Step, bool) {
	extra := byName(tools)
	return func(tu task.ToolUse) (task.Step, bool) { return stepOf(tu, extra) }
}

// stepOf is the step an action is: one of the verse tools' (task.StepOf), or a runtime tool's.
func stepOf(tu task.ToolUse, extra map[string]Tool) (task.Step, bool) {
	t, ok := extra[tu.Name]
	if !ok {
		return task.StepOf(tu)
	}
	if t.Step == "" {
		return task.Step{}, false
	}
	fields, ok := t.Fields(tu.Input)
	if !ok {
		return task.Step{}, false
	}
	return task.Step{Name: t.Step, Fields: fields}, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
