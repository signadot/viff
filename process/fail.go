package process

import (
	"context"
	"errors"
)

// NewFailed builds a process that terminates immediately with msg as its error — used
// when a spawn cannot be honored (e.g. an `llm` driver requested with no creds). The
// point is that *that spawn* errors while the engine keeps running: it is hosted like
// any process (visible via `process get`, status error), rather than a nil that
// vanishes or a panic that stops the loop.
func NewFailed(id string, reg Resolver, msg string) *Process {
	sup := NewSupervisor(&Vector{Default: Auto}, reg)
	return NewProcess(id, sup, failDriver{msg: msg}, nopExec{})
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
