// Package task is the governed-task substrate for verse: a running unit of work whose actions
// pass an inter-action boundary, where the steps a gate names (StepOf) ask a person first, through
// a late-bound, substitutable resolver. What decides is the task's Policy, which can be replaced
// while the task runs.
package task

//go:generate go tool tony-codegen -dir .
