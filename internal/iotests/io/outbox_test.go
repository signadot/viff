package io_test

import (
	"go.viff.dev/viff/internal/viffd/viffdtest"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/client"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	viffio "go.viff.dev/viff/pkg/io"
)

// AN OUT HALF READS THE OUTBOXES PACKAGES DECLARE FOR IT, as it reads its own place: a package at
// demo.announce sends from `out` to this io's demo.out, asks there, and the pass delivers it,
// handing the sink the io's own place as where it was asked and writing the status beside the
// input, inside the package.
func TestAnOutHalfReadsTheOutboxesSendingToIt(t *testing.T) {
	store := entitytest.Store(t)
	srv := viffdtest.Server(t, runnerdtest.Scripted, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	const as = "agent:demo-iod"
	entitytest.Set(t, store, viffio.Ref("demo"), entity.Obj{
		"name": "demo", "root": "demo-iod",
		"out": entity.Obj{"places": []any{"demo.out"}, "as": as},
	}.Payload())

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/charter",
		strings.NewReader(`{at: demo.announce, sends: [{from: out, to: demo.out}], rules: []}`))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, "user:alice@example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("install: %s", resp.Status)
	}
	entitytest.Set(t, store, entity.RefAt("demo", "announce", "out", "deploy-7"),
		entity.Obj{"to": "#viff-dev", "text": "deployed"}.Payload())

	sink := &noted{fields: entity.Obj{"posted": "yes"}}
	viff := client.New(strings.TrimPrefix(ts.URL, "http://"), as)
	out := viffio.NewOut(sink, entity.RefAt("demo", "out"), viff, nil)
	if err := out.Pass(t.Context()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("delivered %d, want the outbox's one input", sink.count())
	}
	in := sink.seen[0]
	if in.Place.String() != "demo.out" || in.At.String() != "demo.announce.out.deploy-7" || in.To != "#viff-dev" {
		t.Fatalf("the sink was handed %+v, want it asked at demo.out, standing in the outbox", in)
	}
	e, ok, _ := store.Get(entity.RefAt("demo", "announce", "out", "deploy-7"+viffio.StatusSuffix))
	if !ok || entity.Str(e.Payload, "posted") != "yes" {
		t.Fatalf("no status beside the input in the package's outbox: %v", e.Payload)
	}
}
