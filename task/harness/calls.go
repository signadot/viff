package harness

import (
	"fmt"
	"io"
	"strings"

	"github.com/signadot/verse/task"
)

// Calls builds a task that runs a fixed list of calls with THIS package's executor: the same
// four tools, the same runtime extras, the same deriver, the same closing act.
//
// That sameness is the whole of it. A `push` proposed by a call list arrives at the gate as the
// action a `push` proposed by the model arrives as — same derived effect, same vector, same
// question on the docket — because the executor and the deriver do not know which driver was
// upstream of them. It is why mechanical work can stop being an LLM without stopping being
// supervised, and why this function is twenty lines rather than a second runtime.
//
// What it does NOT wire is the half that belongs to a model: no answer check (a contract fails
// the spawn — see task.NewCallsTaskFrom), no frame, no conversation, and no spawn factory. A
// list naming `call_agent` fails when it reaches it, which is the honest outcome: delegation is
// a decision made from what the last result said, and a list decided everything in advance.
func Calls(cfg Config, calls []task.Call) *task.Task {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	extra := byName(cfg.Extra)
	tools, err := selectTools(cfg.Tools, cfg.Extra)
	if err != nil {
		return task.NewFailed(cfg.ID, cfg.Resolver, "launch tools: "+err.Error())
	}
	// The tool floor, checked against the LIST rather than against a model's imagination. The
	// llm driver enforces it by simply not offering what a spawn narrowed away; a call list has
	// nobody to withhold a tool from, so the equivalent is to refuse the spawn — before
	// anything runs, rather than three calls in when the fourth turns out not to exist.
	offered := make(map[string]bool, len(tools))
	for _, td := range tools {
		offered[td.Function.Name] = true
	}
	for i, c := range calls {
		if !offered[c.Tool] {
			return task.NewFailed(cfg.ID, cfg.Resolver, fmt.Sprintf(
				"call %d of %d: no tool %q here (have %s)",
				i+1, len(calls), c.Tool, strings.Join(toolNames(tools), ", ")))
		}
	}
	l := task.Launch{ID: cfg.ID, Driver: task.DriverCalls, Vector: cfg.Vector, Answer: cfg.Answer}
	p := task.NewCallsTaskFrom(l, calls, cfg.Resolver, &fsExec{
		root: cfg.Root, log: cfg.Log, content: cfg.Content, extra: extra,
	})
	p.SetLog(cfg.Log)
	p.SetDeriver(demoDeriver{extra: extra})
	if cfg.Finish != nil {
		p.SetFinisher(cfg.Finish)
	}
	return p
}
