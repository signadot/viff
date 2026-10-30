package stock

import (
	"errors"
	"reflect"
	"testing"
)

// AN AGENT'S PROGRAM IS BOUND INTO ITS TASK'S SANDBOX where the runner finds it: a person's
// `claude` under ~/.local/bin is outside the system directories the sandbox binds, and a task
// that ran it would find nothing there. A program not found is left out, as start refuses it.
func TestAnAgentsProgramIsBoundWhereTheRunnerFindsIt(t *testing.T) {
	found := map[string]string{"claude": "/home/ana/.local/bin/claude", "/opt/codex/codex": "/opt/codex/codex"}
	lookPath := func(p string) (string, error) {
		if f, ok := found[p]; ok {
			return f, nil
		}
		return "", errors.New("not found")
	}
	c := Config{Agents: map[string]Agent{"claude": {}, "codex": {Bin: "/opt/codex/codex"}, AgentName: {}}}
	if got, want := c.agentPrograms(lookPath), []string{"/opt/codex/codex", "/home/ana/.local/bin/claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bound programs = %v, want %v", got, want)
	}
	if got := (Config{Agents: map[string]Agent{"claude": {Bin: "missing"}}}).agentPrograms(lookPath); len(got) != 0 {
		t.Fatalf("a program not found is bound: %v", got)
	}
	if got := (Config{Script: &Script{}}).agentPrograms(lookPath); len(got) != 0 {
		t.Fatalf("a runner with no agent of its own binds %v", got)
	}
}
