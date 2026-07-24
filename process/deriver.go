package process

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
		tu.Effects = append(tu.Effects, Effect{Verb: Exec, Resource: Resource{Kind: KindProcess, Ref: cmd}})
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
