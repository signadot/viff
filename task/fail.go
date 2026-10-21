package task

import (
	"context"
	"errors"

	"github.com/signadot/verse/systems"
)

// NewFailed builds a task that terminates immediately with msg as its error — used
// when a launch cannot be honoured (e.g. the agent, on a runner with no model key). The
// point is that *that launch* fails while the engine keeps running: it is hosted like
// any task (visible via `task get`, status error), rather than a nil that
// vanishes or a panic that stops the loop.
func NewFailed(id string, reg Resolver, msg string) *Task {
	sup := NewSupervisor(systems.Environment{}, Autonomy{{Then: Then{Mode: ThenAllow}}}, reg) // nothing to start, so nothing to ask
	return NewTask(id, sup, failDriver{msg: msg}, nopExec{})
}

// failDriver returns its message as a driver error on the first turn, so Run tears
// down and returns that error.
type failDriver struct{ msg string }

func (d failDriver) Next(context.Context, StepResult) (ToolUse, bool, error) {
	return ToolUse{}, false, errors.New(d.msg)
}

// nopExec never runs (failDriver errors before any action executes).
type nopExec struct{}

func (nopExec) Execute(context.Context, ToolUse) (StepResult, []ToolUse, error) {
	return StepResult{}, nil, nil
}
