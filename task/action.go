package task

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ToolUse is one action a task is about to take at the inter-action boundary: a tool, by name,
// and its arguments. The step it is, if any (StepOf), is what the task's gates ask about.
type ToolUse struct {
	ID        string            // boundary id, for attribution and the read surface
	Name      string            // the tool
	Input     map[string]any    // its arguments
	CallAgent *CallAgentRequest // set when this action delegates to a sub-agent
	// Decision is the id of the decision that let this use through (the question Supervisor.Ask
	// posed, `<task>~d<n>`), set by the loop before the executor runs it; empty for a use no
	// gate decided. A tool the runner performs carries it, so the runner can match the act to
	// the answer it relayed by id rather than by the call's fields.
	Decision string
}

// CallAgentRequest is a delegation: a sub-agent, by name, given a prompt. The sub-agent runs
// concurrently, under the task's own gates, and its result folds in at a later boundary.
type CallAgentRequest struct {
	Agent  string // which agent to invoke
	Prompt string // the subtask
}

// describeMax bounds Describe: a record stays a record, and a command that runs to a screen is
// still recognisable by its first line and a bit.
const describeMax = 240

// Describe is the action in one line, for a person deciding about it. A gate's question says
// which rule asked and what verb the action derives to; neither says WHAT the action is, and
// that is the thing an answerer needs — the command a shell would run, the path a write would
// touch, where a push would land. It reads the self-reported input, because that is where the
// action says what it is: the keys the runtime's own tools use first, by name, then everything
// else compactly. Bounded, and "" for an action that says nothing about itself.
func (tu ToolUse) Describe() string {
	if tu.CallAgent != nil && strings.TrimSpace(tu.CallAgent.Prompt) != "" {
		return describeTrunc(tu.CallAgent.Agent + ": " + oneLine(tu.CallAgent.Prompt))
	}
	// A push says where: the destination, and the ref it resolves to when the action knows.
	if dest := str(tu.Input, "destination"); dest != "" {
		if to := str(tu.Input, "to"); to != "" {
			return describeTrunc(dest + " → " + to)
		}
		return describeTrunc(dest)
	}
	for _, k := range []string{"command", "path", "file", "file_path", "question", "prompt", "text"} {
		if v := str(tu.Input, k); v != "" {
			return describeTrunc(oneLine(v))
		}
	}
	// Nothing by name: the scalar inputs, and lists of records as their values, in key order.
	keys := make([]string, 0, len(tu.Input))
	for k := range tu.Input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if v := describeValue(tu.Input[k]); v != "" {
			parts = append(parts, k+": "+v)
		}
	}
	return describeTrunc(strings.Join(parts, "; "))
}

// describeValue renders one input value: a scalar as itself, a list of scalars joined, a list
// of records as each record's scalar values in key order. Nested maps and empty values are
// left out rather than rendered as structure.
func describeValue(v any) string {
	switch x := v.(type) {
	case string:
		return oneLine(x)
	case bool:
		return strconv.FormatBool(x)
	case int, int64, float64:
		return fmt.Sprint(x)
	case []any:
		var items []string
		for _, e := range x {
			switch e := e.(type) {
			case map[string]any:
				ks := make([]string, 0, len(e))
				for k := range e {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				var fields []string
				for _, k := range ks {
					switch f := e[k].(type) {
					case string:
						if f != "" {
							fields = append(fields, oneLine(f))
						}
					case bool, int, int64, float64:
						fields = append(fields, fmt.Sprint(f))
					}
				}
				if len(fields) > 0 {
					items = append(items, strings.Join(fields, " "))
				}
			default:
				if s := describeValue(e); s != "" {
					items = append(items, s)
				}
			}
		}
		return strings.Join(items, ", ")
	case []string:
		return strings.Join(x, ", ")
	}
	return ""
}

// oneLine is a string on one line: the first line, marked when there were more.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func describeTrunc(s string) string {
	if len(s) <= describeMax {
		return s
	}
	return s[:describeMax] + "…"
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}
