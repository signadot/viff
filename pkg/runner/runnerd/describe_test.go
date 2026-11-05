package runnerd_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/runner/runnerd"
	"github.com/signadot/verse/pkg/task"
)

// GET /describe answers what the daemon was told the runner offers, beside the task contract.
func TestDescribeAnswersTheOffers(t *testing.T) {
	d := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewFailed(l.ID, reg, "no tasks here")
	})
	d.Describe(runner.Description{Version: runner.DescriptionVersion, Offers: []runner.Offer{
		{Agent: &runner.AgentOffer{Name: "verse", Settings: map[string]runner.Setting{"model": {Values: []string{"claude-*"}}}}}, {Script: true}}})
	srv := httptest.NewServer(d.HTTPHandler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/describe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got runner.Description
	if err := codec.Decode(resp.Body, &got, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if got.Version != runner.DescriptionVersion || len(got.Offers) != 2 || got.Offers[0].Name() != "verse" ||
		got.Offers[0].Agent.Settings["model"].Values[0] != "claude-*" || !got.Offers[1].Script {
		t.Fatalf("described %+v", got)
	}
}
