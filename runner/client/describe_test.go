package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/client"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/systems/systemstest"
	"github.com/signadot/verse/task"
)

// A RUNNER IS WHAT DESCRIBES ITSELF. The probe reads GET /describe, and what it reads is what
// the runner offers; a url that answers without describing itself is not a runner, and one
// nothing answers is unreachable.
func TestProbeReadsWhatARunnerOffers(t *testing.T) {
	d := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewTask(l.ID, systemstest.Opened(task.NewSupervisor(l.Environment, l.Autonomy, reg), l.Environment), &noDriver{}, noExec{})
	})
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{
		{Agent: &runner.AgentOffer{Name: "codex", Settings: map[string]runner.Setting{"model": {Default: true}}}}, {Script: true}}})
	face := httptest.NewServer(d.HTTPHandler())
	defer face.Close()

	reach, desc, err := client.Probe(face.URL)
	if err != nil || reach != client.IsRunner {
		t.Fatalf("probe = %s, %v; want a runner", reach, err)
	}
	if len(desc.Offers) != 2 || desc.Offers[0].Name() != "codex" || !desc.Offers[1].Script || !desc.Offers[0].Agent.Settings["model"].Default {
		t.Fatalf("offers = %+v; want what the runner described", desc.Offers)
	}

	other := httptest.NewServer(http.NewServeMux()) // answers, and describes nothing
	defer other.Close()
	if reach, _, err := client.Probe(other.URL); reach != client.NotRunner || !errors.Is(err, task.ErrUnsupported) {
		t.Fatalf("probe of a non-runner = %s, %v; want not a runner", reach, err)
	}

	dead := httptest.NewServer(http.NewServeMux())
	url := dead.URL
	dead.Close()
	if reach, _, err := client.Probe(url); reach != client.Unreachable || !errors.Is(err, task.ErrUnreachable) {
		t.Fatalf("probe of nothing = %s, %v; want unreachable", reach, err)
	}
}

type noDriver struct{}

func (noDriver) Next(_ context.Context, _ task.StepResult) (task.ToolUse, bool, error) {
	return task.ToolUse{}, false, nil
}

type noExec struct{}

func (noExec) Execute(_ context.Context, tu task.ToolUse) (task.StepResult, []task.ToolUse, error) {
	return task.StepResult{Use: tu}, nil, nil
}
