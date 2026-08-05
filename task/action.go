package task

import (
	"reflect"
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
