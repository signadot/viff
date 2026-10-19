package runnerd_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/task"
)

// GET /describe answers what the daemon was told the runner offers, beside the task contract.
func TestDescribeAnswersTheOffers(t *testing.T) {
	d := runnerd.NewInProcessHandler(func(l runner.Launch, reg *task.Registry) *task.Task {
		return task.NewFailed(l.ID, reg, "no tasks here")
	})
	d.Describe(runner.Description{Offers: []runner.Offer{{Agent: "verse",
		With: map[string]runner.Accepts{"model": runner.AcceptsAny}}, {Script: true}}})
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
	if len(got.Offers) != 2 || got.Offers[0].Agent != "verse" || !got.Offers[0].With["model"].Any ||
		!got.Offers[1].Script {
		t.Fatalf("described %+v", got)
	}
}
