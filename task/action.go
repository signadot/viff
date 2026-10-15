package task

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ToolUse is one action a task is about to take at the inter-action boundary
// — the unit the gate vector evaluates. It deliberately separates what the
// task *asked for* (Name/Input, self-reported and untrusted for
// classification) from what it *actually does* (Effects, derived by the
// platform). Gates and scope match on Effects; making derivation a first-class
// step is the model's answer to "classify by the real action, never the
// self-report."
type ToolUse struct {
	ID        string            // boundary id, for attribution and the read surface
	Name      string            // self-reported action name
	Input     map[string]any    // self-reported arguments (opaque until derived)
	Effects   []Effect          // derived typed capabilities — the match surface
	Repo      *RepoContext      // git-relative context (optional)
	CallAgent *CallAgentRequest // set when this action synchronously invokes a sub-task (Verb CallAgent)
}

// CallAgentRequest models a *synchronous* sub-task invocation — "call another
// task one-shot with a prompt and permissions." It is a function call: it
// opens a defer frame; the child runs concurrently and its result folds in at a
// later boundary. Our loop gates the call boundary (before) and the returned
// result (after), and hands down a Grant that must be a subset of our own scope.
// The child may be a foreign one-shot (opaque leaf) or a recursive invocation of
// our own loop (transparent frame).
type CallAgentRequest struct {
	Agent  string // which task/runtime to invoke
	Prompt string // the one-shot task
	Grant  Grant  // the scope envelope conferred on the child
}

// Grant is a scope envelope — the hard authority axis (not gate-mode). In a
// delegation tree a child Grant must be ⊆ the parent's (authority is
// non-increasing down the chain). v0 models it as the capability predicates the
// child may exercise.
//
// It has a wire form because a spawn now carries a resolved envelope to whatever runtime
// hosts the task, local or remote (see task.Launch) — a name would let the two ends
// disagree about what it meant.
//
//tony:schemagen=verse-grant,notag
type Grant struct {
	Allow []GateMatch `tony:"field=allow,omitzero"` // capability predicates the child may exercise
}

// Subset reports whether child is within parent — the monotonicity check a
// CallAgent boundary enforces before delegating. This v0 is deliberately *sound but
// incomplete*: it accepts only a wildcard parent or predicate-for-predicate
// identity. True predicate subsumption is an open constraint (see the doc's
// capability-derivation frontier); erring toward refusal is the safe direction.
func (parent Grant) Subset(child Grant) bool {
	if parent.allowsAll() {
		return true
	}
	for _, c := range child.Allow {
		if isWildcard(c) {
			return false // child asks for everything; parent does not grant it
		}
		if !containsMatch(parent.Allow, c) {
			return false
		}
	}
	return true
}

func (g Grant) allowsAll() bool {
	for _, m := range g.Allow {
		if isWildcard(m) {
			return true
		}
	}
	return false
}

func isWildcard(m GateMatch) bool {
	return len(m.Tools) == 0 && len(m.Verbs) == 0 && len(m.Resources) == 0 &&
		len(m.Paths) == 0 && len(m.Branches) == 0 && !m.OnPush
}

func containsMatch(ms []GateMatch, x GateMatch) bool {
	for _, m := range ms {
		if reflect.DeepEqual(m, x) {
			return true
		}
	}
	return false
}

// RepoContext is the git-repo-relative state a gate can match against — the
// "gate vector wrt a git repo," lifted from 42's push-target + enforced-planning
// context: changed files vs a base ref, and the push / promotion targets a
// task may advance.
type RepoContext struct {
	Root         string
	Branch       string
	Base         string
	ChangedFiles []string
	PushTarget   string // 42's push target: the ref a task pushes changesets to
	NextRef      string // 42's "next": the promotion ref advanced on success
	Tools        []string
}

// FilesTouched returns the file resources across the action's effects — the input
// to PathPred (file) matching, so 42's file-path model keeps working over the
// richer effect model.
func (tu ToolUse) FilesTouched() []string {
	var fs []string
	for _, e := range tu.Effects {
		if e.Resource.Kind == KindFile {
			fs = append(fs, e.Resource.Ref)
		}
	}
	return fs
}

// isPush reports whether the action targets the push / promotion boundary — the
// edge 42 governs (dev agent → CI target → origin).
func (tu ToolUse) isPush() bool {
	if tu.Repo != nil {
		for _, e := range tu.Effects {
			if e.Resource.Kind == KindGitRef &&
				(e.Resource.Ref == tu.Repo.PushTarget || e.Resource.Ref == tu.Repo.NextRef) {
				return true
			}
		}
	}
	switch strings.ToLower(tu.Name) {
	case "push", "git-push", "git_push", "promote":
		return true
	}
	return false
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
	// A push says where: the repository and the ref it resolves to when the action knows, else
	// the branch as the task named it. A delete says so.
	if to := str(tu.Input, "to"); to != "" {
		what := to
		if repo := str(tu.Input, "repo"); repo != "" {
			what = repo + " → " + to
		}
		if del, _ := tu.Input["delete"].(bool); del {
			what = "delete " + what
		}
		return describeTrunc(what)
	}
	if branch := str(tu.Input, "branch"); branch != "" {
		return describeTrunc(branch)
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
