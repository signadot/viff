package task

import (
	"path/filepath"
	"strings"
)

// Deriver turns a self-reported action into typed effects. It is the trusted
// classification step: the loop calls Derive before Evaluate, and gates match on
// the result, never on Name/Input directly. Fidelity varies by tool — explicit
// for file tools, best-effort for shell — which is exactly the doc's
// capability-derivation frontier, made a pluggable seam here rather than hidden.
type Deriver interface {
	Derive(tu *ToolUse)
}

// BasicDeriver handles common, agent-API-agnostic shapes. An adapter for a
// specific agent maps that agent's tool schema onto ToolUse.Name/Input; this
// then derives effects from conventional argument names. It is a floor, not a
// ceiling: richer derivers can replace it without touching gate logic.
//
// It is also where the verse's own four tools are classified — run_bash, read_file,
// list_dir, write_file — because every driver proposes them: a model through the harness, a
// call list, and a scripted run, which is one run_bash under this package's supervisor. A
// deriver that adds a runtime's own tools hands the rest here, so a call meets the same
// envelope and the same gates whichever driver proposed it.
type BasicDeriver struct{}

func (BasicDeriver) Derive(tu *ToolUse) {
	if len(tu.Effects) > 0 {
		return // already classified by a richer deriver or adapter
	}
	if tu.CallAgent != nil {
		tu.Effects = append(tu.Effects, Effect{Verb: CallAgent, Resource: AgentRes(tu.CallAgent.Agent)})
		return
	}
	switch strings.ToLower(tu.Name) {
	case "resolve":
		// Answering a question is an action like any other, so it derives an effect and a
		// grant can bound it. The resource is the question, not the task it belongs to:
		// "may answer these questions" is the sentence a resolver's envelope needs, and the
		// id carries the origin already.
		if id := strArg(tu.Input, "id", "question", "gate_id"); id != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Resolve, Resource: GateRes(id)})
		}
	case "run_bash":
		// The verse's shell tool. Exec alone, without the guessed files `bash` carries: a
		// gate over `write` is about the file tools, and a command whose tokens look like
		// paths is not a write the agent reported.
		tu.Effects = append(tu.Effects, Effect{Verb: Exec, Resource: Resource{Kind: KindTask, Ref: strArg(tu.Input, "command")}})
	case "read_file", "list_dir":
		if p := strArg(tu.Input, "path"); p != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Read, Resource: File(p)})
		}
	case "write_file":
		if p := strArg(tu.Input, "path"); p != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Write, Resource: File(p)})
		}
	case "read", "cat":
		if p := strArg(tu.Input, "path", "file", "file_path"); p != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Read, Resource: File(p)})
		}
	case "write", "edit":
		if p := strArg(tu.Input, "path", "file", "file_path"); p != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Write, Resource: File(p)})
		}
	case "create":
		if p := strArg(tu.Input, "path", "file", "file_path"); p != "" {
			tu.Effects = append(tu.Effects, Effect{Verb: Create, Resource: File(p)})
		}
	case "bash", "sh", "exec":
		cmd := strArg(tu.Input, "command", "cmd")
		tu.Effects = append(tu.Effects, Effect{Verb: Exec, Resource: Resource{Kind: KindTask, Ref: cmd}})
		// Best-effort: shell is opaque, so file effects here are self-reported,
		// not derived. This is the low-fidelity corner the frontier names.
		for _, f := range guessFiles(cmd) {
			tu.Effects = append(tu.Effects, Effect{Verb: Write, Resource: File(f)})
		}
	}
}

func strArg(in map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := in[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// guessFiles pulls path-looking tokens out of a shell command. Deliberately
// crude and clearly best-effort — a real deriver would parse the command.
func guessFiles(cmd string) []string {
	var out []string
	for _, tok := range strings.Fields(cmd) {
		tok = strings.TrimRight(tok, ";|&")
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if strings.Contains(tok, "/") || filepath.Ext(tok) != "" {
			out = append(out, tok)
		}
	}
	return out
}
