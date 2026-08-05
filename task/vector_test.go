package task

import (
	"context"
	"strings"
	"testing"
)

// derive is the classification step the loop runs before Evaluate.
func derive(tu ToolUse) ToolUse {
	BasicDeriver{}.Derive(&tu)
	return tu
}

// Gate keyed on the self-reported tool, the derived files, and git branch.
func TestVectorToolsAndFiles(t *testing.T) {
	v := &Vector{
		Default: Auto,
		Gates: []Gate{
			{Name: "iac", Mode: Ask, Match: GateMatch{
				Paths: []PathPred{{Name: ".tf", Type: "extension"}},
			}},
			{Name: "shell-on-release", Mode: Park, Match: GateMatch{
				Tools:    []string{"Bash"},
				Branches: []string{"release/*"},
			}},
		},
	}

	// Edit of a terraform file -> derived File(write) -> ASK
	tu := derive(ToolUse{Name: "Edit", Input: map[string]any{"file_path": "infra/main.tf"}})
	if d := v.Evaluate(tu); d.Mode != Ask || d.Gate != "iac" {
		t.Fatalf("want ASK/iac, got %v/%q (%s)", d.Mode, d.Gate, d.Reason)
	}
	// Edit of a plain code file -> AUTO (default)
	tu = derive(ToolUse{Name: "Edit", Input: map[string]any{"file_path": "app/server.go"}})
	if d := v.Evaluate(tu); d.Mode != Auto {
		t.Fatalf("want AUTO, got %v", d.Mode)
	}
	// Bash on a release branch -> PARK (most restrictive wins)
	tu = derive(ToolUse{
		Name: "Bash", Input: map[string]any{"command": "kubectl apply -f x.yaml"},
		Repo: &RepoContext{Branch: "release/1.2"},
	})
	if d := v.Evaluate(tu); d.Mode != Park || d.Gate != "shell-on-release" {
		t.Fatalf("want PARK/shell-on-release, got %v/%q", d.Mode, d.Gate)
	}
}

// Gate keyed on the derived typed capability (verb), not the self-reported name.
func TestVectorVerbCapability(t *testing.T) {
	v := &Vector{Default: Auto, Gates: []Gate{
		{Name: "no-silent-exec", Mode: Ask, Match: GateMatch{Verbs: []Verb{Exec}}},
	}}
	tu := derive(ToolUse{Name: "Bash", Input: map[string]any{"command": "echo hi"}})
	if d := v.Evaluate(tu); d.Mode != Ask || d.Gate != "no-silent-exec" {
		t.Fatalf("want ASK/no-silent-exec, got %v/%q", d.Mode, d.Gate)
	}
	// a read does not carry an Exec effect -> AUTO
	tu = derive(ToolUse{Name: "Read", Input: map[string]any{"path": "README.md"}})
	if d := v.Evaluate(tu); d.Mode != Auto {
		t.Fatalf("want AUTO, got %v", d.Mode)
	}
}

// The push / promotion boundary (42's push-target and next) is a gate.
func TestVectorOnPush(t *testing.T) {
	v := &Vector{Default: Auto, Gates: []Gate{
		{Name: "promote", Mode: Ask, Match: GateMatch{OnPush: true}},
	}}
	tu := ToolUse{
		Name:    "git",
		Effects: []Effect{{Verb: Write, Resource: GitRef("refs/ci/next")}},
		Repo:    &RepoContext{NextRef: "refs/ci/next"},
	}
	if d := v.Evaluate(tu); d.Mode != Ask || d.Gate != "promote" {
		t.Fatalf("want ASK/promote, got %v/%q", d.Mode, d.Gate)
	}
	if d := v.Evaluate(derive(ToolUse{Name: "Read", Input: map[string]any{"path": "x"}})); d.Mode != Auto {
		t.Fatalf("want AUTO, got %v", d.Mode)
	}
}

// Delegation: a spawn is a gated boundary, and the child grant must be ⊆ parent.
func TestCallAgentAndGrantSubset(t *testing.T) {
	// calling another task is itself a gated action
	v := &Vector{Default: Auto, Gates: []Gate{
		{Name: "delegate", Mode: Ask, Match: GateMatch{Verbs: []Verb{CallAgent}}},
	}}
	child := Grant{Allow: []GateMatch{{Verbs: []Verb{Read}, Resources: []ResourcePred{{Kind: KindFile, Pattern: "docs/**"}}}}}
	tu := derive(ToolUse{Name: "task", CallAgent: &CallAgentRequest{Agent: "claude", Prompt: "summarize docs", Grant: child}})
	if d := v.Evaluate(tu); d.Mode != Ask || d.Gate != "delegate" {
		t.Fatalf("want ASK/delegate, got %v/%q", d.Mode, d.Gate)
	}

	// child ⊆ parent: identical predicate is within; a broader (wildcard) child is not
	parent := Grant{Allow: []GateMatch{{Verbs: []Verb{Read}, Resources: []ResourcePred{{Kind: KindFile, Pattern: "docs/**"}}}}}
	if !parent.Subset(child) {
		t.Fatalf("identical child should be a subset")
	}
	wideChild := Grant{Allow: []GateMatch{{}}} // wildcard = wants everything
	if parent.Subset(wideChild) {
		t.Fatalf("wildcard child must NOT be a subset of a bounded parent")
	}
	// a wildcard parent covers anything
	if !(Grant{Allow: []GateMatch{{}}}).Subset(wideChild) {
		t.Fatalf("wildcard parent should cover a wildcard child")
	}
}

// Autonomy is data: slide a running task from ASK to AUTO without restart.
func TestSlideAutonomyNoRestart(t *testing.T) {
	iac := func(m Mode) *Vector {
		return &Vector{Default: Auto, Gates: []Gate{
			{Name: "iac", Mode: m, Match: GateMatch{Paths: []PathPred{{Name: ".tf", Type: "extension"}}}},
		}}
	}
	sup := NewSupervisor(iac(Ask), AutoResolver{Who: "auto"})
	tu := derive(ToolUse{Name: "Edit", Input: map[string]any{"file_path": "infra/main.tf"}})

	if d := sup.Vector().Evaluate(tu); d.Mode != Ask {
		t.Fatalf("want ASK before slide, got %v", d.Mode)
	}
	sup.SetVector(iac(Auto)) // slide autonomy up on the live task
	if d := sup.Vector().Evaluate(tu); d.Mode != Auto {
		t.Fatalf("want AUTO after slide, got %v", d.Mode)
	}
}

// The resolver is late-bound and substitutable: the same gate is answered by a
// task (autonomous) or the present human (in-the-loop) through one interface.
func TestResolverSubstitution(t *testing.T) {
	v := &Vector{Default: Auto, Gates: []Gate{
		{Name: "promote", Mode: Ask, Match: GateMatch{OnPush: true}},
	}}
	tu := ToolUse{Name: "promote", Repo: &RepoContext{NextRef: "refs/ci/next"}}

	// agent resolver auto-proceeds (autonomous loop)
	sup := NewSupervisor(v, AutoResolver{Who: "agent:ci"})
	if res, err := sup.Check(context.Background(), tu); err != nil || !res.Proceed || res.By != "agent:ci" {
		t.Fatalf("agent resolver: proceed=%v by=%q err=%v", res.Proceed, res.By, err)
	}

	// swap in the present human, who poses-and-denies
	out := make(chan GateQuestion, 1)
	in := make(chan bool)
	sup.SetResolver(ChannelResolver{Who: "human:scott", Out: out, In: in})
	done := make(chan Resolution, 1)
	go func() { r, _ := sup.Check(context.Background(), tu); done <- r }()
	if posed := <-out; posed.Gate != "promote" {
		t.Fatalf("want promote decision posed, got %q", posed.Gate)
	}
	in <- false
	if r := <-done; r.Proceed || r.By != "human:scott" {
		t.Fatalf("human deny: proceed=%v by=%q", r.Proceed, r.By)
	}
}

// A gate id must split back into the id of whatever posed it, on GateSep and nowhere else
// — that split is how versed routes a `gate resolve` to the owning task, and how the CLI
// tells a gate id from the task id it belongs to. Nothing else pins it: producer
// (Supervisor.Check) and splitters (versed, cmd/verse) live in three packages, so a
// delimiter changed in one and not the others compiles, passes every other test, and fails
// only at resolve time with "unknown or already-answered decision".
func TestGateIDSplitsOnGateSepToItsOwner(t *testing.T) {
	const origin = "review-demo:pr:1" // a real spawnID shape: hyphens and colons, no GateSep
	sup := NewSupervisor(&Vector{Default: Auto}, AutoResolver{Who: "agent"})
	sup.SetOrigin(origin)

	res, err := sup.Check(context.Background(), ToolUse{Name: "x"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	id := res.Question.ID
	owner, rest, ok := strings.Cut(id, GateSep)
	if !ok {
		t.Fatalf("gate id %q carries no GateSep %q — a resolve cannot find its owner", id, GateSep)
	}
	if owner != origin {
		t.Errorf("owner = %q, want %q — resolve would route %q to the wrong task", owner, origin, id)
	}
	if strings.Contains(rest, GateSep) {
		t.Errorf("gate id %q has a second GateSep in %q — the split is ambiguous", id, rest)
	}
}
