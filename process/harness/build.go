// Package harness assembles a real LLM agent on top of the verse process loop: an
// OpenAI-compatible Chat Completions driver (served via Portkey), a filesystem
// executor confined to a working directory, the tool→capability deriver, and the
// gate vector. Build wires them into a Process with a call_agent SpawnFunc.
//
// It is an injectable driver: the daemon and the demo both build processes via a
// Builder, so nothing in the process substrate depends on this package.
package harness

import (
	"fmt"
	"io"

	"github.com/example/infra/verse/process"
)

const systemPrompt = `You are an autonomous coding agent working through a supervised loop.
Tools: read_file(path), write_file(path, content), run_bash(command), list_dir(path),
call_agent(agent, prompt) — delegate an independent subtask to a concurrent sub-agent.
You operate inside a sandboxed working directory; all paths are relative to it.
Some actions are gated by a human supervisor and may be DENIED. If a tool result says
DENIED, do not retry the same action — find another approach or explain why you cannot
proceed. For independent subtasks, prefer call_agent to run them in parallel; each
sub-agent's result comes back when it finishes. Complete the user's task, then stop with
a one-paragraph summary.`

// Vector is the demo gate vector: read freely; ASK before writing a file, running
// a shell command, or delegating to a sub-agent.
func Vector() *process.Vector {
	return &process.Vector{
		Default: process.Auto,
		Gates: []process.Gate{
			{Name: "write", Mode: process.Ask, Match: process.GateMatch{Verbs: []process.Verb{process.Write}}},
			{Name: "shell", Mode: process.Ask, Match: process.GateMatch{Verbs: []process.Verb{process.Exec}}},
			{Name: "delegate", Mode: process.Ask, Match: process.GateMatch{Verbs: []process.Verb{process.CallAgent}}},
		},
	}
}

// Config parameterizes Build.
type Config struct {
	ID       string           // process id
	Task     string           // the top-level user task (seeds the driver)
	Root     string           // working directory the executor is confined to
	Resolver process.Resolver      // how ASK gates are answered (stdin, auto, Registry, …)
	Client   completer            // model client; nil → built from the LLM creds below (or env)
	Log      io.Writer            // local narration/exec log; nil → io.Discard
	Content  process.ContentReader // read_file source; nil → local filesystem under Root

	// LLM creds for the model client (resolved from verse config: file ← env). Any
	// empty field falls back to its default (base URL, model) or the environment.
	LLMBaseURL    string
	LLMKey        string
	LLMVirtualKey string
	LLMModel      string
}

// Build assembles a Process (real LLM driver + filesystem executor + gate vector)
// and returns it alongside an accessor for the executor's ledger. A call_agent
// action spawns a concurrent sub-process built the same way (child scope ⊆ parent
// via NewChild; supervision from the ASK answer).
func Build(cfg Config) (*process.Process, func() []ExecRecord) {
	if cfg.Client == nil {
		cfg.Client = clientFrom(cfg.LLMBaseURL, cfg.LLMKey, cfg.LLMVirtualKey, cfg.LLMModel)
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	sup := process.NewSupervisor(Vector(), cfg.Resolver)
	sup.SetDeriver(demoDeriver{})

	driver := &llmDriver{client: cfg.Client, tools: toolDefs(), prompt: cfg.Task, log: cfg.Log}
	ex := &fsExec{root: cfg.Root, log: cfg.Log, content: cfg.Content}
	p := process.NewProcess(cfg.ID, sup, driver, ex)
	p.SetLog(cfg.Log)
	driver.emit = p.Emit // narration onto this process's read plane

	var kid int
	p.SetSpawn(func(parent *process.Process, mode process.ChildMode, req *process.CallAgentRequest) (*process.Process, error) {
		kid++
		childID := fmt.Sprintf("%s.%s#%d", parent.ID, req.Agent, kid)
		childDriver := &llmDriver{client: cfg.Client, tools: toolDefs(), prompt: req.Prompt, log: cfg.Log}
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
