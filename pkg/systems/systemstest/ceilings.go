// Package systemstest is ceilings for tests.
//
// A runner's ceilings close every system they name none for (systems.Beyond), so a supervisor
// given none refuses each step of a system. A test about what a task does within its
// environment, rather than about the runner's line, says the runner leaves that environment
// open.
package systemstest

import "go.viff.dev/viff/pkg/systems"

// Open is ceilings that leave open each system of the environments given (systems.Open), as a
// runner whose operator writes its tasks would: what a step may do is then the environment's
// and the autonomy's.
func Open(envs ...systems.Environment) map[string]systems.Ceiling {
	out := map[string]systems.Ceiling{}
	for _, e := range envs {
		for name := range e.Systems {
			out[name] = systems.Open()
		}
	}
	return out
}

// Opened is s given ceilings that leave the environments open (Open): a supervisor, as a test
// builds one inline.
func Opened[S interface {
	SetCeilings(map[string]systems.Ceiling)
}](s S, envs ...systems.Environment) S {
	s.SetCeilings(Open(envs...))
	return s
}
