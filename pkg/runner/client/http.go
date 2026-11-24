// Package client is verse's HTTP client of a runner: an httpRunner drives a runner at a base URL
// over the Runner API (package runner), so the engine runs a task wherever a runner serves it:
// beside verse, on another host, on k8s. See docs/task-control.md.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner"
	"github.com/signadot/verse/pkg/task"
)

// ControlTimeout bounds every non-streaming call to a runner. A remote that accepts the
// connection and then never answers is indistinguishable from a slow one, and without a
// deadline the caller waits on that distinction forever — which is not a hypothetical: it is
// how a launch, a cancel and an answer each hang, holding whatever goroutine issued them.
//
// Generous rather than tight. It is a backstop against a hung peer, not a latency budget.
const ControlTimeout = 30 * time.Second

// Launcher is a runner verse can start work on: a task.Runner, and Launch. Launch is here rather
// than on task.Runner because the launch is the runner's type (package runner), which task does
// not import.
type Launcher interface {
	task.Runner
	// Launch POSTs the launch to /tasks, carrying the caller's id so the task's record and the
	// runner agree on it. A refusal is a failed handle with the runner's reason.
	Launch(l runner.Launch) task.Handle
}

// HTTP returns the runner at baseURL.
func HTTP(baseURL string) Launcher { return HTTPOver(baseURL, nil) }

// HTTPOver is HTTP over a transport of the caller's choosing — nil means the default one, so
// HTTP is this with nothing supplied.
//
// It exists because the contract is HTTP but the WIRE need not be TCP. A runner that hosts each
// task in its own contained process reaches that process over something the task itself cannot
// dial: a unix socket outside its mount namespace, an inherited listener with no name at all.
// Without this seam the only way to speak the contract to such a child is to re-implement the
// client beside it — which is how two readings of one protocol come to exist, and the SSE half
// is not small.
//
// The baseURL still matters even when the transport ignores its host: it is what Ref reports,
// and it is what the request line carries. A caller dialing a socket conventionally passes
// something that names the peer rather than a reachable address.
func HTTPOver(baseURL string, tr http.RoundTripper) Launcher {
	return &httpRunner{
		base: strings.TrimRight(baseURL, "/"),
		// Its OWN client, never http.DefaultClient: that one is shared task-wide, so a
		// timeout set on it would silently bound every other caller's requests too.
		cl: &http.Client{Timeout: ControlTimeout, Transport: tr},
		// The event stream is long-lived by design, so a whole-request deadline would cut
		// a healthy subscription. It is bounded by its context instead (Subscribe/cancel).
		stream: &http.Client{Transport: tr},
	}
}

type httpRunner struct {
	base   string
	cl     *http.Client // control: bounded by ControlTimeout
	stream *http.Client // the event plane: unbounded, cancelled by its context
}

// Launch POSTs /tasks carrying the caller's id (so the remote uses it and it matches
// the task:instance entity).
func (p *httpRunner) Launch(l runner.Launch) task.Handle {
	body, err := codec.Marshal(&l, codec.Tony)
	if err != nil {
		return task.FailedHandle(fmt.Sprintf("launch on %s: it does not render: %v", p.base, err))
	}
	resp, err := p.cl.Post(p.base+"/tasks", codec.MediaType(codec.Tony), bytes.NewReader(body))
	if err != nil {
		return task.FailedHandle(err.Error())
	}
	defer resp.Body.Close()
	// A refusal is not an acceptance. The status used to be discarded here, so a runner
	// answering 400, 403 or 501 got the same remote handle as one that took the work: versed
	// mirrored a task:instance for it and the flow waited on a task that was never
	// started, so the refusal surfaced as a hang. The runner deciding it cannot host this
	// launch is the DEFAULT way a runner declines — it is the whole reason a runner may refuse
	// rather than verse having to detect what it is — and that answer is inert unless it is
	// read (git-issue 6mnmxj6mh12krz3ae1n0).
	if err := statusErr(resp); err != nil {
		return task.FailedHandle(fmt.Sprintf("launch refused by %s: %s", p.base, err))
	}
	return &remoteHandle{base: p.base, id: l.ID, cl: p.cl, stream: p.stream}
}

// Attach re-attaches to an existing remote task (no launch). remoteHandle.Subscribe
// replays from the start (?since=0), so a resumed mirror re-derives current state.
func (p *httpRunner) Attach(id string) task.Handle {
	return &remoteHandle{base: p.base, id: id, cl: p.cl, stream: p.stream}
}

// Ref is the base URL — how the task:instance entity records this runner so a restart
// can reconstruct it.
func (p *httpRunner) Ref() string { return p.base }

// ListTasks / TaskSnapshot let versed union a remote runner's tasks into the
// unified `task list`/`get` (implementing task.Lister). Each call
// is bounded by a short timeout, so an unreachable remote fails fast rather than hanging the
// list. The verse-facing contract already serves GET /tasks and GET /tasks/{id}, so
// this is a thin client.
//
// It returns an error because nil could not carry one. It used to report nil for a runner with
// no tasks, for one that answered 500, and for one that never answered at all — and the
// comment here claimed the mirror covered the difference. It does not: versed reads the mirror
// as a scope-membership FILTER over what the runner reported, not as a fallback source, so an
// unreachable runner's tasks were absent from the list and the response was a clean 200
// that was simply short. The same shape as the discarded launch status above, on the read plane
// (git-issue 6mnmxj6mh12krz3ae1n0).
func (p *httpRunner) ListTasks() ([]task.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The error is READ, and that is not defensive tidying: NewRequest returns a nil request
	// with it, and `Do(nil)` panics rather than failing. A base is built by whoever installed
	// the runner, so an unparseable one is a thing a caller can cause, and it would arrive as a
	// panic serving HTTP.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/tasks", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a usable runner url: %w", task.ErrUnreachable, p.base, err)
	}
	resp, err := p.cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", task.ErrUnreachable, p.base, err)
	}
	defer resp.Body.Close()
	// Not statusErr: that reads a status against ONE task, where 404 means the far side
	// has never heard of that id. On the collection there is no id to be wrong about, so a
	// 404 says the same thing 501 does — whatever is at this URL, it does not serve the
	// verse-facing runner contract.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return nil, fmt.Errorf("%w: %s serves no task list", task.ErrUnsupported, p.base)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		detail := strings.TrimSpace(string(msg))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("listing tasks on %s: %s", p.base, detail)
	}
	var out []task.Snapshot
	if err := decodeResp(resp, &out); err != nil {
		// A body that does not decode is not an empty verse. Discarding this error is how a
		// format drift on either end reads as a runner that has nothing running.
		return nil, fmt.Errorf("%w: %s answered an undecodable task list: %w",
			task.ErrUnsupported, p.base, err)
	}
	return out, nil
}

// The status was already read here, so a refusal never passed for an answer — but every way
// of failing collapsed into the same `false`, which the caller could only report as "no such
// task". A task on a runner that did not answer is not a task that does not exist,
// and `task get` said it was (git-issue 6mnmxj6mh12krz3ae1n0).
func (p *httpRunner) TaskSnapshot(id string) (task.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/tasks/"+id, nil)
	if err != nil { // nil request, and Do panics on one — see ListTasks
		return task.Snapshot{}, fmt.Errorf("%w: %s is not a usable runner url: %w",
			task.ErrUnreachable, p.base, err)
	}
	resp, err := p.cl.Do(req)
	if err != nil {
		return task.Snapshot{}, fmt.Errorf("%w: %s: %w", task.ErrUnreachable, p.base, err)
	}
	defer resp.Body.Close()
	// Here a 404 IS about the id — this path names one — so statusErr's reading is the right
	// one, and ErrNoSuchTask is what it yields.
	if err := statusErr(resp); err != nil {
		return task.Snapshot{}, err
	}
	var m task.Snapshot
	if err := decodeResp(resp, &m); err != nil {
		return task.Snapshot{}, fmt.Errorf("%w: %s answered an undecodable task: %w",
			task.ErrUnsupported, p.base, err)
	}
	return m, nil
}

// remoteHandle is a Handle over the wire: the read plane is GET /events (SSE), the
// control plane the matching POSTs.
type remoteHandle struct {
	base   string
	id     string
	cl     *http.Client // control: bounded
	stream *http.Client // the event plane: unbounded, cancelled by its context
}

// noSuchTask is why a task ended whose runner answered that it has none by that id.
const noSuchTask = "the runner has no such task"

// Subscribe replays the whole retained window then streams live, so the mirror sees the
// task from as far back as the remote still holds.
func (h *remoteHandle) Subscribe() (<-chan task.Event, func()) {
	return h.SubscribeSince(0)
}

// SubscribeSince resumes from a cursor. It is the same contract the local bus
// honours, carried over HTTP by handing the cursor to the remote's own `?since=` rather than
// asking for everything and discarding — the remote is the one that knows what it still has,
// and it is the one that can say EventGap when the cursor is below its window.
func (h *remoteHandle) SubscribeSince(since int64) (<-chan task.Event, func()) {
	return h.subscribe(since, 0)
}

// SubscribeAccepted is SubscribeSince, saying first that the runner accepted the subscribe
// (task.Accepting): the runner answered the request, and with a stream, within the wait, which
// is ControlTimeout at most.
func (h *remoteHandle) SubscribeAccepted(since int64, within time.Duration) (<-chan task.Event, func()) {
	if within <= 0 || within > ControlTimeout {
		within = ControlTimeout
	}
	return h.subscribe(since, within)
}

// subscribe opens the task's stream from a cursor. FOR A READER COUNTING ACCEPTED SUBSCRIBES (a
// wait, answered), A REQUEST THAT GETS NO ANSWER WITHIN THE WAIT FAILS: the stream itself has no
// deadline, and its response does, so a subscribe into a network that takes the request and
// answers nothing closes as one that was refused does, for its reader to ask again. Any other
// reader waits for the answer as long as it likes, and ends the wait itself.
func (h *remoteHandle) subscribe(since int64, answered time.Duration) (<-chan task.Event, func()) {
	sayAccepted := answered > 0
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan task.Event, 256)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.url("/events?since="+strconv.FormatInt(since, 10)), nil)
	if err != nil { // nil request, and Do panics on one — see ListTasks
		out <- task.Event{
			Kind: task.EventStatus,
			Text: string(task.Failed),
			Err:  fmt.Sprintf("subscribe %s: %s is not a usable runner url: %v", h.id, h.base, err),
		}
		close(out)
		cancel()
		return out, func() {}
	}
	if sayAccepted {
		unanswered := time.AfterFunc(answered, cancel)
		defer unanswered.Stop()
	}
	resp, err := h.stream.Do(req)
	if err != nil {
		close(out)
		cancel()
		return out, func() {}
	}
	// A rejected subscribe is answered by what the runner says of the task, where it says
	// anything: a task that never started has no stream and no child to forward to, so 404 is
	// the honest answer to the question that was asked, while its record says "the task
	// process exited before it served anything (exit status 1) — its output is on this
	// runner's log, tagged <id>", which is the account that ends an investigation (git-issue
	// ykpt9g3eh12kss9wfhn0). Only a TERMINAL status is taken: a task the remote still considers
	// live has no account of its end to give.
	//
	// ONLY THE RUNNER FAILS A TASK, AND ON BOTH ANSWERS. The stream and the task are two
	// requests, so a 404 on the stream alone is not the runner's word on the task: asked a
	// moment apart, the runner may hold the task by the second. It has no such task when it
	// answers 404 for the task as well: the task is gone and nothing will ever say how it ended,
	// so it ends here, failed and orphaned. Each 404 is the runner's only where the runner gave
	// it (runner.AnswerHeader). Any other refusal, and a runner that could not be asked about the
	// task, says nothing about it: the stream closes with no frame, as a request that got no
	// response does, and whoever reads it asks again.
	if resp.StatusCode >= http.StatusBadRequest {
		resp.Body.Close()
		snap, said := h.ask()
		switch {
		case said == saidSnapshot && snap.Status.Terminal():
			out <- task.Event{Kind: task.EventStatus, Text: string(snap.Status), Err: snap.Err, Orphaned: snap.Orphaned}
		case said == saidNoTask && resp.StatusCode == http.StatusNotFound && runner.Answered(resp.Header):
			out <- task.Event{Kind: task.EventStatus, Text: string(task.Failed), Orphaned: true, Err: noSuchTask}
		}
		close(out)
		cancel()
		return out, func() {}
	}
	// AN ANSWER THAT IS NOT THE RUNNER'S IS NONE: a page served in its place accepts nothing, and
	// keeps no task's wait. The stream closes with no frame, for its reader to ask again.
	if !runner.Answered(resp.Header) {
		resp.Body.Close()
		close(out)
		cancel()
		return out, func() {}
	}
	// Frames come in whatever format the stream declares — Content-Type is spoken for by
	// text/event-stream, so the format rides its own header. Assuming JSON here made every
	// frame fail to parse the moment the remote's default changed, and because an
	// unparseable frame is skipped rather than reported, the symptom was a stream that
	// simply never delivered anything.
	if sayAccepted {
		out <- task.Event{Kind: task.EventAccepted}
	}
	frameFmt := codec.Tony
	if f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat)); err == nil &&
		resp.Header.Get(codec.HeaderFormat) != "" {
		frameFmt = f
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		frames := codec.NewFrames(resp.Body, 8<<20)
		for frames.Next() {
			var ev task.Event
			if codec.Unmarshal([]byte(frames.Text()), &ev, frameFmt) != nil {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel
}

func (h *remoteHandle) Pause(by string) error { return h.post("/pause", runner.PauseRequest{By: by}) }
func (h *remoteHandle) Resume(by string) error {
	return h.post("/resume", runner.ResumeRequest{By: by})
}

// Cancel ends the task, and says why: the reason is what the task's charters see, so it
// crosses with the cancel.
func (h *remoteHandle) Cancel(reason, by string) error {
	return h.post("/cancel", runner.CancelRequest{Reason: reason, By: by})
}

func (h *remoteHandle) Steer(instr, by, ask string) error {
	return h.post("/say", runner.SayRequest{Instruction: instr, By: by, Ask: ask})
}

func (h *remoteHandle) SetAutonomy(p task.Autonomy, by string) error {
	return h.send(http.MethodPut, "/autonomy", runner.AutonomyRequest{Autonomy: p, By: by})
}

// Resolve answers a gate the remote's task is blocked on, and reports whether the answer
// landed. It is what makes a remote-hosted task answerable at all: without it, this handle
// is not a gateResolver, and every route into it fails with a message about the question
// rather than about the runner.
//
// It sends the contract's ResolveRequest.
//
// The status is the point, and it is why this cannot go through post(): an answer that did
// not land must not read as one that did, or the operator is told a blocked worker was
// released.
func (h *remoteHandle) Resolve(decision, choice, say, by string) error {
	body := runner.ResolveRequest{ID: decision, Choice: choice, By: by}
	if say != "" {
		body.Say = []string{say}
	}
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		return err
	}
	resp, err := h.cl.Post(h.url("/resolve"), codec.MediaType(codec.Tony), bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", task.ErrUnreachable, h.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// A remote 404 on a resolve is about the DECISION, not the task: the request
		// reached a runtime that hosts this task and it did not recognise the id.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%w: %s", task.ErrNoSuchDecision, strings.TrimSpace(string(msg)))
	}
	return statusErr(resp)
}

// Err reads the terminal failure detail off the snapshot — the remote's task.Snapshot
// already carries it, so this is the same read as Result against a different field.
// snapshot reads the remote's own account of this task — GET /tasks/{id}, the shape both
// ends already speak. ok is false when the remote could not be asked or did not answer in
// that shape, which is a different thing from a task with nothing to report.
//
// It is one function rather than a field-at-a-time read because the three callers want three
// fields of ONE answer, and asking three times invites them to disagree: a task that ends
// between two of those calls would have its status read from before and its reason from
// after.
func (h *remoteHandle) snapshot() (task.Snapshot, bool) {
	snap, said := h.ask()
	return snap, said == saidSnapshot
}

// What a runner said when asked for a task's snapshot.
const (
	saidNothing  = iota // no answer, or none that reads: the runner was not reached
	saidSnapshot        // the task, as the runner holds it
	saidNoTask          // 404: the runner has no task by that id
)

// ask is snapshot with the runner's answer told apart: a runner that has no such task has said
// something about the task, and a runner that was not reached has not.
func (h *remoteHandle) ask() (task.Snapshot, int) {
	resp, err := h.cl.Get(h.url(""))
	if err != nil {
		return task.Snapshot{}, saidNothing
	}
	defer resp.Body.Close()
	// A 404 is the runner's word only where the runner gave it (runner.AnswerHeader): anything
	// in front of it answers 404 to a route it does not have.
	if resp.StatusCode == http.StatusNotFound && runner.Answered(resp.Header) {
		return task.Snapshot{}, saidNoTask
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return task.Snapshot{}, saidNothing
	}
	var snap task.Snapshot
	if err := decodeResp(resp, &snap); err != nil {
		return task.Snapshot{}, saidNothing
	}
	return snap, saidSnapshot
}

func (h *remoteHandle) Err() string {
	snap, _ := h.snapshot()
	return snap.Err
}

// Agent reads who ran it and with what off the snapshot.
func (h *remoteHandle) Agent() (string, map[string]string) {
	snap, _ := h.snapshot()
	return snap.Agent, snap.Settings
}

// Refused reads the refused connections off the snapshot.
func (h *remoteHandle) Refused() map[string]int {
	snap, _ := h.snapshot()
	return snap.Refused
}

// Result reads the terminal result off the snapshot.
func (h *remoteHandle) Result() string {
	snap, _ := h.snapshot()
	return snap.Result
}

func (h *remoteHandle) url(suffix string) string { return h.base + "/tasks/" + h.id + suffix }

// decodeResp reads a response in the format it DECLARES.
//
// Requests here send labelled tony, which is also what the remote renders. What still
// cannot be assumed is the REPLY: hard-coding a decoder breaks the moment the two ends
// differ — which is exactly what happened when the default moved. An unlabelled body
// falls back to tony, which reads a JSON one too.
func decodeResp(resp *http.Response, v any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Tony
	}
	return codec.Unmarshal(body, v, f)
}

// post issues one control call and says what became of it. It used to discard both the error
// and the status, so a control that never left the building — and one the remote refused —
// read exactly like one that was applied.
func (h *remoteHandle) post(path string, body any) error {
	return h.send(http.MethodPost, path, body)
}

func (h *remoteHandle) send(method, path string, body any) error {
	var r io.Reader
	if body != nil {
		b, err := codec.Marshal(body, codec.Tony)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.url(path), r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", codec.MediaType(codec.Tony))
	resp, err := h.cl.Do(req)
	if err != nil {
		// Nothing is known about whether the far side acted. That is the whole content of
		// ErrUnreachable, and it is the only one of the three outcomes worth retrying.
		return fmt.Errorf("%w: %s%s: %w", task.ErrUnreachable, h.id, path, err)
	}
	defer resp.Body.Close()
	return statusErr(resp)
}

// statusErr reads a control response as one of the three outcomes. A refusal is the remote
// answering, which is a different fact from not answering at all — so it never becomes
// ErrUnreachable, however unhelpful the body.
func statusErr(resp *http.Response) error {
	if resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	detail := strings.TrimSpace(string(msg))
	if detail == "" {
		detail = resp.Status
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", task.ErrNoSuchTask, detail)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", task.ErrAlreadyAnswered, detail)
	case http.StatusGone:
		return fmt.Errorf("%w: %s", task.ErrEnded, detail)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", task.ErrUnsteerable, detail)
	case http.StatusNotImplemented:
		return fmt.Errorf("%w: %s", task.ErrUnsupported, detail)
	}
	return errors.New(detail)
}

var (
	_ task.Runner = (*httpRunner)(nil)
	_ task.Handle = (*remoteHandle)(nil)
	// The handle a mirror keeps a task's lease through.
	_ task.Accepting = (*remoteHandle)(nil)
	// Asserted here so a change to Lister is a compile error in this file rather than a
	// type assertion that quietly starts reporting false in versed.
	_ task.Lister = (*httpRunner)(nil)
)
