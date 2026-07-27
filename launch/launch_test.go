package launch

import "testing"

func TestBindExpandsSubject(t *testing.T) {
	s := Spec{
		Driver:      "harness",
		Task:        "review $subject and report",
		Environment: "sandbox-$subject",
		Vector:      "supervised",
	}
	got := s.Bind(Binding{"subject": "demo:pr:1"})

	if got.Task != "review demo:pr:1 and report" {
		t.Fatalf("Task = %q", got.Task)
	}
	if got.Environment != "sandbox-demo:pr:1" {
		t.Fatalf("Environment = %q", got.Environment)
	}
	// fields without a $var are untouched
	if got.Driver != "harness" || got.Vector != "supervised" {
		t.Fatalf("plain fields changed: %+v", got)
	}
}

func TestBindLeavesUnboundVarsAndIsPure(t *testing.T) {
	s := Spec{Task: "do $subject for $owner"}
	got := s.Bind(Binding{"subject": "x:y:1"}) // no "owner" binding
	if got.Task != "do x:y:1 for $owner" {
		t.Fatalf("unbound var not preserved: %q", got.Task)
	}
	if s.Task != "do $subject for $owner" {
		t.Fatalf("Bind mutated the receiver: %q", s.Task)
	}
}

// Longer variable names bind first, so a short name is not a prefix-hazard.
func TestBindLongestKeyFirst(t *testing.T) {
	s := Spec{Task: "$subject vs $sub"}
	got := s.Bind(Binding{"sub": "SHORT", "subject": "LONG"})
	if got.Task != "LONG vs SHORT" {
		t.Fatalf("prefix collision: %q", got.Task)
	}
}

// A frame is a template like the task it frames: a charter that says "answer about
// $subject" needs the binding, and a frame that could not carry one would push every
// per-firing detail back into the task.
func TestBindExpandsFrame(t *testing.T) {
	s := Spec{Frame: "You review $subject. Answer with a severity and nothing else."}
	got := s.Bind(Binding{"subject": "demo:pr:1"})
	if got.Frame != "You review demo:pr:1. Answer with a severity and nothing else." {
		t.Fatalf("Frame = %q", got.Frame)
	}
}

// Tools are deliberately NOT expanded, and that asymmetry is the interesting one. Every
// other string here is a template because a charter has reason to build it out of the
// firing — but a tool name comes from a fixed set the driver defines, so a $var in one
// could only name a tool that does not exist, or name one chosen by whoever last wrote the
// entity being matched. The second is the widening-by-binding risk authority.md flags, one
// field away from the grant.
func TestBindDoesNotExpandTools(t *testing.T) {
	s := Spec{Tools: []string{"read_file", "$sneaky"}}
	got := s.Bind(Binding{"sneaky": "run_bash"})
	if len(got.Tools) != 2 || got.Tools[1] != "$sneaky" {
		t.Fatalf("Tools = %v, want the authored list verbatim", got.Tools)
	}
}

// A bound spec must not alias the charter's slice, or one firing could rewrite the tool
// list every later firing reads.
func TestBindCopiesTools(t *testing.T) {
	s := Spec{Tools: []string{"read_file"}}
	got := s.Bind(Binding{})
	got.Tools[0] = "run_bash"
	if s.Tools[0] != "read_file" {
		t.Fatalf("Bind aliased the receiver's tools: %v", s.Tools)
	}
}
