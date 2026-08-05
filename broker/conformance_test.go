package broker_test

import (
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/broker"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/task/tasktest"
)

// The broker joins the task.Instance conformance suite. Everything it reports is
// projected from a dial-in worker's reports, which is the seam every drift so far has been
// on: this drives a worker through the real HTTP protocol and asks what verse sees.
func TestConformance(t *testing.T) {
	// The broker and its worker-facing server live for the WHOLE suite. Standing them up
	// lazily inside the first property would register their cleanup on that subtest's t,
	// closing the server before the next property runs.
	b := broker.New()
	srv := httptest.NewServer(b.WorkerHandler())
	defer srv.Close()
	tasktest.Run(t, &brokerProducer{b: b, srv: srv})
}

type brokerProducer struct {
	b   *broker.Broker
	srv *httptest.Server
	n   int
	// epoch is the fence from the most recent claim; RaiseGate reports under it.
	epoch int64
}

func (p *brokerProducer) Name() string { return "broker" }

// A worker reports a terminal status with free text and raises gates carrying a reason, so
// every capability is available on this leg.
func (p *brokerProducer) Caps() tasktest.Caps {
	return tasktest.Caps{FailureReason: true, CompletionAnswer: true, ExecutorGates: true}
}

func (p *brokerProducer) Start(t *testing.T, spec tasktest.Spec) string {
	t.Helper()
	p.n++
	id := "p" + itoa(int64(p.n))
	p.b.Spawn(task.Launch{ID: id, Prompt: "a task", Driver: spec.Driver}, nil)

	// A worker must claim before it may report — the epoch it gets back is the fence on
	// every subsequent write.
	var claim struct {
		Epoch int64 `tony:"field=epoch"`
	}
	postJSON(t, p.srv.Client(), p.srv.URL+"/tasks/"+id+"/claim", `{"identity":"human:conformance"}`, &claim)
	p.epoch = claim.Epoch

	if spec.Outcome != "" {
		postJSON(t, p.srv.Client(), p.srv.URL+"/tasks/"+id+"/report",
			`{"epoch":`+itoa(claim.Epoch)+`,"events":[{"kind":"status","text":"`+string(spec.Outcome)+`"}],`+
				`"result":"`+spec.Text+`"}`, nil)
	}
	return id
}

func (p *brokerProducer) RaiseGate(t *testing.T, id, gate, reason string) string {
	t.Helper()
	did := gate + "-1"
	postJSON(t, p.srv.Client(), p.srv.URL+"/tasks/"+id+"/report",
		`{"epoch":`+itoa(p.epoch)+`,"events":[{"kind":"pending","id":"`+did+`","gate":"`+gate+`",`+
			`"text":"`+reason+`"}]}`, nil)
	return did
}

func (p *brokerProducer) Snapshot(t *testing.T, id string) (task.Instance, bool) {
	t.Helper()
	// The harness asks a yes/no question, and for an in-memory registry the only error is
	// "not here" — so this narrowing loses nothing the producer could act on.
	inst, err := p.b.TaskSnapshot(id)
	return inst, err == nil
}
