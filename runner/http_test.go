package runner_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/process"
	"github.com/signadot/verse/process/processd"
	"github.com/signadot/verse/runner"
)

// a driver that takes `steps` no-op actions then completes.
type doneDriver struct{ steps int }

func (d *doneDriver) Next(context.Context, process.StepResult) (process.ToolUse, bool, error) {
	if d.steps <= 0 {
		return process.ToolUse{}, false, nil // complete
	}
	d.steps--
	return process.ToolUse{Name: "noop"}, true, nil
}

type okExec struct{}

func (okExec) Execute(_ context.Context, tu process.ToolUse) (process.StepResult, []process.ToolUse, error) {
	return process.StepResult{Use: tu}, nil, nil
}

// The engine drives a REMOTE processd entirely over HTTP: httpRunner.Spawn issues the
// spawn, remoteHandle.Subscribe streams the SSE read plane (cursored), and the process
// runs to a terminal status — no in-process handle. This is the Phase-3 proof of the
// base-URL decoupling.
func TestHTTPRunnerDrivesRemoteProcessd(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(process.Launch{ID: "t1", Task: "do a thing", Driver: "scripted"}, nil)

	ch, unsub := h.Subscribe()
	defer unsub()

	var sawCursor, sawExec, completed bool
	timeout := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			if ev.Cursor > 0 {
				sawCursor = true
			}
			if ev.Kind == process.EvExec {
				sawExec = true
			}
			if ev.Kind == process.EvStatus && ev.Text == "done" {
				completed = true
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for the remote process to complete")
		}
	}
	if !completed {
		t.Fatal("never saw a completed status over the wire")
	}
	if !sawCursor {
		t.Fatal("events carried no cursor")
	}
	if !sawExec {
		t.Fatal("never saw the action execute remotely")
	}
}

// A remote runner enumerates its processes so versed can union them into `process list`/
// `get`: ListProcesses/ProcessSnapshot are a thin client over the remote's GET /processes.
func TestHTTPRunnerListsRemoteProcesses(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn(process.Launch{ID: "r1", Task: "remote task", Driver: "scripted"}, nil)

	lister, ok := pol.(interface {
		ListProcesses() []process.Instance
		ProcessSnapshot(string) (process.Instance, bool)
	})
	if !ok {
		t.Fatal("httpRunner should implement the lister interface")
	}

	var found bool
	for _, m := range lister.ListProcesses() {
		if m.ID == "r1" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote process r1 not in the runner's ListProcesses")
	}

	snap, ok := lister.ProcessSnapshot("r1")
	if !ok || snap.Task != "remote task" {
		t.Fatalf("ProcessSnapshot(r1) = %+v, %v; want the remote task", snap, ok)
	}
	if _, ok := lister.ProcessSnapshot("nope"); ok {
		t.Fatal("ProcessSnapshot of an unknown id should be false")
	}
}

// Phase 4 resume: a process:instance left stuck "running" (verse crashed mid-run),
// tagged with a remote runner, is re-attached on restart — the replay from the remote
// (which kept running and finished) converges the entity to its terminal status. This is
// what versed.reconcileProcesses does: resume, not orphan, when the work outlived verse.
func TestResumeReAttachesToRemoteProcess(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Auto}, process.AutoResolver{})
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 2}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	pol.Spawn(process.Launch{ID: "t1", Task: "task", Driver: "scripted"}, nil) // runs on the remote to completion
	time.Sleep(300 * time.Millisecond)                                         // let it finish (retained in the ring)

	// "verse restart": a fresh store with a STALE running entity tagged with the runner.
	store := entitytest.Store(t)
	entitytest.Set(t, store, mirror.Ref("t1"), entity.Obj{"status": "running", "runner": srv.URL}.Payload())

	// resume: re-attach and re-mirror (the core of reconcileProcesses).
	mirror.Reflect(store, "t1", pol.Attach("t1"), mirror.Meta{Runner: srv.URL})

	deadline := time.After(5 * time.Second)
	for {
		e, _, _ := store.Get(mirror.Ref("t1"))
		if s, _ := entity.ToObj(e.Payload)["status"].(string); s == string(process.Done) {
			return // converged: resume recovered the terminal from the remote
		}
		select {
		case <-deadline:
			e, _, _ := store.Get(mirror.Ref("t1"))
			t.Fatalf("entity never converged to completed; status=%v", entity.ToObj(e.Payload)["status"])
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A gate on a REMOTE process is answerable. Without remoteHandle.Resolve this handle was not
// a gateResolver at all, so every route into it refused with a message about the question —
// a process on another host could be blocked on a decision that no operator could give.
//
// It also pins the vocabulary: the handle sends api.Resolve (`id`/`approve`), and the remote
// runtime understands it. When those two disagree the answer decodes to an empty id, answers
// nothing, and comes back as though the decision were unknown.
func TestHTTPRunnerAnswersARemoteGate(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		// Ask, and resolved by the daemon's own registry — so the process really blocks and
		// really waits for an answer to arrive over HTTP.
		sup := process.NewSupervisor(&process.Vector{Default: process.Ask}, reg)
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(process.Launch{ID: "t-gate", Task: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	rh, ok := h.(interface {
		Resolve(decision string, proceed bool, say, by string) error
	})
	if !ok {
		t.Fatal("a remote handle cannot answer a gate, so a remote process can block forever")
	}

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the process gated")
			}
			if ev.Kind == process.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote process never raised a gate")
		}
	}

	if err := rh.Resolve(decision, true, "go ahead", "human:scott"); err != nil {
		t.Fatalf("answering the remote gate %s = %v", decision, err)
	}
	// Reported success is not delivery. The process has to actually go on, and the answer has
	// to arrive attributed — an answer that lost its `by` on the wire is an unattributable
	// decision recorded as though it were attributed.
	timeout = time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended without a decision")
			}
			if ev.Kind == process.EvDecision && ev.ID == decision {
				if !ev.Proceed {
					t.Fatalf("the remote read a deny from an approve: %+v", ev)
				}
				if ev.By != "human:scott" {
					t.Fatalf("by = %q, want human:scott — attribution was dropped on the wire", ev.By)
				}
				return
			}
		case <-timeout:
			t.Fatal("the remote process never acted on the answer")
		}
	}
}

// And the older spelling still works, because things dial in against this face. A client
// sending {decisionId, proceed} must still be understood, or adding the new vocabulary would
// have been a break dressed as an addition.
func TestProcessdStillReadsTheOlderResolveSpelling(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Ask}, reg)
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(process.Launch{ID: "t-old", Task: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the process gated")
			}
			if ev.Kind == process.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote process never raised a gate")
		}
	}

	body, err := codec.Marshal(map[string]any{
		"decisionId": decision, "proceed": true, "by": "human:legacy",
	}, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/processes/t-old/resolve", codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the older spelling = %s, want 204", resp.Status)
	}
}

// The contract as documented mixes the two spellings — `{id, proceed}` — and things dial in
// against the document, not against the struct. Read as two fixed vocabularies that combination
// yields a DENY: the id is recognised and the verdict field beside it is not, so an approval
// arrives as a refusal and the process stops. It has to be read as an approval.
func TestProcessdReadsTheDocumentedMixedSpelling(t *testing.T) {
	build := func(l process.Launch, reg *process.Registry, _ process.ContentReader) *process.Process {
		sup := process.NewSupervisor(&process.Vector{Default: process.Ask}, reg)
		return process.NewProcess(l.ID, sup, &doneDriver{steps: 1}, okExec{})
	}
	srv := httptest.NewServer(processd.New(build).Handler())
	defer srv.Close()

	pol := runner.HTTP(srv.URL)
	h := pol.Spawn(process.Launch{ID: "t-mixed", Task: "do a thing", Driver: "scripted"}, nil)
	ch, unsub := h.Subscribe()
	defer unsub()

	var decision string
	timeout := time.After(5 * time.Second)
	for decision == "" {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended before the process gated")
			}
			if ev.Kind == process.EvPending {
				decision = ev.ID
			}
		case <-timeout:
			t.Fatal("the remote process never raised a gate")
		}
	}

	body, err := codec.Marshal(map[string]any{"id": decision, "proceed": true, "by": "human:doc"}, codec.Tony)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/processes/t-mixed/resolve", codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the documented spelling = %s, want 204", resp.Status)
	}

	timeout = time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("the stream ended without a decision")
			}
			if ev.Kind == process.EvDecision && ev.ID == decision {
				if !ev.Proceed {
					t.Fatal("`{id, proceed: true}` was read as a deny — an approval became a refusal")
				}
				return
			}
		case <-timeout:
			t.Fatal("the remote process never acted on the answer")
		}
	}
}
