// Package harness assembles a real LLM agent on top of the verse task loop: an
// OpenAI-compatible Chat Completions driver (served via Portkey), a filesystem
// executor confined to a working directory, the tool→capability deriver, and the
// gate vector. Build wires them into a Task with a call_agent SpawnFunc.
//
// It is an injectable driver: the daemon and the demo both build tasks via a
// Builder, so nothing in the task substrate depends on this package.
package harness

import (
	"fmt"
	"io"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/answer"
	"github.com/signadot/verse/task"
)

// defaultFrame is the frame used when a spawn does not write one — and it is DERIVED, from
// the tools actually offered and the vector actually installed, rather than being the
// package constant it replaces.
//
// That constant had two defects, and both are of a kind a constant cannot avoid.
//
// It ended "Complete the user's task, then stop with a one-paragraph summary." The final
// assistant message is what Result() returns, which is what a completed task's `result`
// carries, which is what a charter's `$result` derefs. trigger/deref.go is built to resolve
// $result.findings[0].severity structurally — against a prompt instructing prose. The two
// were in direct contradiction and the prompt won: structured answers came back as
// paragraphs. So this says the opposite, and a spawn wanting a particular shape states it
// in its own frame, which is what the field is for.
//
// It also said "prefer call_agent to run them in parallel" while Vector(), a few lines
// below, ASK-gates the call-agent verb. Two package constants, adjacent, disagreeing: the
// prompt recommended the one action guaranteed to stop and wait for a human, which for an
// unattended trigger firing means park. Rewording the constant would not fix it, because
// the vector is per-spawn and slidable while the constant is neither. Deriving the advice
// from the vector does: below, each tool is described as it will actually behave.
func defaultFrame(tds []toolDef, vec *task.Vector) string {
	var free, gated []string
	for _, td := range tds {
		name := td.Function.Name
		if name == failTool {
			continue // not an action; described on its own terms at the end
		}
		if gateModeFor(name, vec).Blocks() {
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
		// answers reaches for it when the task needs it and not otherwise — the behaviour
		// the old "prefer call_agent" advice inverted.
		fmt.Fprintf(&b, "These tools STOP the loop until a supervisor approves each use, and may be "+
			"denied: %s. Use them where the task requires it, not by preference.\n", strings.Join(gated, ", "))
	}
	fmt.Fprintf(&b, "A tool result that says DENIED is a decision, not a transient error: do not retry "+
		"the same action — take another approach, or call %s.\n", failTool)
	b.WriteString("A result saying an action is outside your grant is final. Nobody present can " +
		"approve it and no rephrasing makes it permitted.\n")
	b.WriteString("Your final message IS this task's result. It is read by whatever asked for the " +
		"work — usually another part of the system rather than a person — so make it the answer " +
		"itself. Do not append a summary of what you did or how you did it. If the task states a " +
		"shape for the answer, produce exactly that shape and nothing else.")
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
		"fit the shape.", failTool)
	return b.String()
}

// withoutContract removes the clause contractClause added, leaving the frame as it was.
func withoutContract(frame string) string {
	if i := strings.Index(frame, contractHeading); i >= 0 {
		return strings.TrimRight(frame[:i], "\n")
	}
	return frame
}

// gateModeFor evaluates one tool against the vector the way the loop will, using a probe
// action carrying representative arguments.
//
// The arguments matter. demoDeriver classifies read_file/write_file by their `path` and
// run_bash by its `command`, and an action with no derived effect matches no verb gate — so
// probing with empty input would report every tool as ungated, quietly reintroducing the
// exact disagreement this function exists to prevent.
func gateModeFor(tool string, vec *task.Vector) task.Mode {
	if vec == nil {
		return task.Auto
	}
	tu := task.ToolUse{Name: tool, Input: map[string]any{"path": "probe", "command": "probe"}}
	if tool == "call_agent" {
		tu.CallAgent = &task.CallAgentRequest{Agent: "probe"}
	}
	demoDeriver{}.Derive(&tu)
	return vec.Evaluate(tu).Mode
}

// Vector is the demo gate vector: read freely; ASK before writing a file, running
// a shell command, or delegating to a sub-agent.
func Vector() *task.Vector {
	return &task.Vector{
		Default: task.Auto,
		Gates: []task.Gate{
			{Name: "write", Mode: task.Ask, Match: task.GateMatch{Verbs: []task.Verb{task.Write}}},
			{Name: "shell", Mode: task.Ask, Match: task.GateMatch{Verbs: []task.Verb{task.Exec}}},
			{Name: "delegate", Mode: task.Ask, Match: task.GateMatch{Verbs: []task.Verb{task.CallAgent}}},
		},
	}
}

// Config parameterizes Build.
type Config struct {
	ID       string             // task id
	Prompt   string             // the top-level user task (seeds the driver)
	Root     string             // working directory the executor is confined to
	Resolver task.Resolver      // how ASK gates are answered (stdin, auto, Registry, …)
	Client   completer          // model client; nil → built from the LLM creds below (or env)
	Log      io.Writer          // local narration/exec log; nil → io.Discard
	Content  task.ContentReader // read_file source; nil → local filesystem under Root

	// Frame is the agent's system prompt; empty → defaultFrame, derived from the tools
	// offered and the vector installed. Tools narrows the offered set; empty → all of
	// them. Both come from the spawn (launch.Spec.frame / .tools), which is the point:
	// see task.Launch.
	Frame string
	Tools []string
	// Answer is this spawn's answer contract as a tony match pattern; nil → no contract,
	// and the run ends wherever the model stops. See package answer.
	Answer *ir.Node
	// Vector overrides the demo gate vector. It is here rather than applied by the caller
	// afterwards because the frame is derived from it, and a vector installed after Build
	// would be described by a frame written against the wrong one.
	Vector *task.Vector

	// Finish is the RUNTIME's own closing act, composed onto the end of this task's loop: a
	// coding runner's push, and anything else a runtime does with the work rather than to it.
	// nil → nothing is composed and the loop ends where the driver does.
	//
	// It is a Config field rather than something the caller wraps afterwards because Build
	// returns a finished Task and exposes neither driver nor executor — so there was nothing to
	// compose onto, and a runner that wanted its delivery gated had to reimplement the harness
	// to get one. See task.WithFinisher for what it buys.
	//
	// The agent is not told about it. It is not a tool, it is not in the frame, and it is
	// proposed after the driver has stopped.
	Finish task.Finisher

	// LLM creds for the model client (resolved from verse config: file ← env). Any
	// empty field falls back to its default (base URL, model) or the environment.
	LLMBaseURL    string
	LLMKey        string
	LLMVirtualKey string
	LLMModel      string
}

// Build assembles a Task (real LLM driver + filesystem executor + gate vector)
// and returns it alongside an accessor for the executor's ledger. A call_agent
// action spawns a concurrent sub-task built the same way (child scope ⊆ parent
// via NewChild; supervision from the ASK answer).
func Build(cfg Config) (*task.Task, func() []ExecRecord) {
	if cfg.Client == nil {
		cfg.Client = clientFrom(cfg.LLMBaseURL, cfg.LLMKey, cfg.LLMVirtualKey, cfg.LLMModel)
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	tools, err := selectTools(cfg.Tools)
	if err != nil {
		// A spawn that named a tool this build does not have gets a FAILED task, not a
		// quietly smaller toolset. The charter asked for a specific set; running with a
		// different one and saying nothing is how `grant: read-only` came to mean nothing.
		return task.NewFailed(cfg.ID, cfg.Resolver, "launch tools: "+err.Error()), func() []ExecRecord { return nil }
	}
	// A contract that cannot be parsed fails the spawn, for the reason an unknown tool
	// name does: the charter asked for something specific, and running without it while
	// saying nothing is how a field comes to mean nothing. It is parsed here as well as on
	// the engine side because a task can be spawned straight at a runtime (`task
	// run`, a remote POST) without ever passing through a charter.
	var contract *answer.Contract
	if cfg.Answer != nil {
		var err error
		if contract, err = answer.Parse(cfg.Answer); err != nil {
			return task.NewFailed(cfg.ID, cfg.Resolver, "answer contract: "+err.Error()), func() []ExecRecord { return nil }
		}
	}
	vec := cfg.Vector
	if vec == nil {
		vec = Vector()
	}
	sup := task.NewSupervisor(vec, cfg.Resolver)
	sup.SetDeriver(demoDeriver{})

	// The frame is derived from THIS task's tools and vector when the spawn did not
	// write one, so what the agent is told matches what it will actually meet.
	frame := cfg.Frame
	if frame == "" {
		frame = defaultFrame(tools, vec)
	}
	// The contract is stated in the frame even when the spawn wrote the frame itself, and
	// appending to an author's text is otherwise not this function's business. It is
	// warranted here because this is not advice about the task: it is a fact about how the
	// loop will behave — the final message is checked and a failure comes back — and an
	// agent that meets the check for the first time as a rejection wastes a repair
	// attempt learning what it could have been told at the start.
	if contract != nil {
		frame = strings.TrimRight(frame, "\n") + "\n\n" + contractClause(contract)
	}

	driver := &llmDriver{client: cfg.Client, tools: tools, frame: frame, prompt: cfg.Prompt,
		log: cfg.Log, contract: contract}
	ex := &fsExec{root: cfg.Root, log: cfg.Log, content: cfg.Content}
	// The runtime's closing act, if it has one. Composed here rather than left to the caller
	// because the pieces are unexported: everything below wires this driver and this executor
	// into one task, and a caller reaching in to add a step would have to rebuild all of it.
	var d task.Driver = driver
	var x task.Executor = ex
	if cfg.Finish != nil {
		d, x = task.WithFinisher(driver, ex, cfg.Finish)
	}
	p := task.NewTask(cfg.ID, sup, d, x)
	p.SetLog(cfg.Log)
	driver.emit = p.Emit // narration onto this task's read plane

	var kid int
	p.SetSpawn(func(parent *task.Task, mode task.ChildMode, req *task.CallAgentRequest) (*task.Task, error) {
		kid++
		childID := fmt.Sprintf("%s.%s%s%d", parent.ID, req.Agent, task.GateSep, kid)
		// A child inherits the parent's tool set and frame. Inheriting the tools is the
		// conservative reading of delegation — a sub-agent should not be handed a
		// capability its parent's spawn withheld — and inheriting the frame keeps the
		// answer shape the charter asked for, since a child's result folds into the
		// parent's conversation and thence into what $result reads.
		//
		// It does NOT inherit the answer contract, and the asymmetry is the point. The
		// contract describes THIS task's answer — the value a charter derefs — while a
		// child answers a subtask its parent chose the wording of, and its result arrives
		// as one more tool result for the parent to read. Forcing the parent's shape onto
		// it would demand a verdict from an agent that was asked to go read a file, and
		// would fail runs over a document nothing downstream reads. A child that needs a
		// contract is a spawn of its own.
		//
		// (frame carries the parent's contract clause. It is text about a check the child
		// does not run, so it is trimmed rather than left to mislead.)
		childDriver := &llmDriver{client: cfg.Client, tools: tools, frame: withoutContract(frame),
			prompt: req.Prompt, log: cfg.Log}
		childExec := &fsExec{root: cfg.Root, log: cfg.Log, content: cfg.Content}
		child, err := parent.NewChild(childID, mode, req, childDriver, childExec)
		if err == nil {
			childDriver.emit = child.Emit // child narration onto the child's read plane
		}
		return child, err
	})
	return p, ex.Records
}

// NewClient builds the default Portkey/OpenAI Chat Completions client from the
// environment (PORTKEY_API_KEY / PORTKEY_VIRTUAL_KEY / OPENAI_MODEL).
func NewClient() completer { return newLLMClient() }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
