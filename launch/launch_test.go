package launch

import "testing"

func TestBindExpandsSubject(t *testing.T) {
	s := Spec{
		Driver:      "harness",
		Task:        "review $subject and report",
		Environment: "sandbox-$subject",
		Policy:      "supervised",
	}
	got := s.Bind(Binding{"subject": "demo:pr:1"})

	if got.Task != "review demo:pr:1 and report" {
		t.Fatalf("Task = %q", got.Task)
	}
	if got.Environment != "sandbox-demo:pr:1" {
		t.Fatalf("Environment = %q", got.Environment)
	}
	// fields without a $var are untouched
	if got.Driver != "harness" || got.Policy != "supervised" {
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
