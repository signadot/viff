// Package client is verse's HTTP client of a runner: an httpRunner drives a runner at a base URL
// over the Runner API (package runner), so the engine can run a task on another host or k8s
// exactly as it runs one in process. It is the mirror of versed's localRunner — same
// task.Runner seam, HTTP transport. See docs/task-control.md.
package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// HTTP returns a Runner that drives the runner at baseURL.
// ControlTimeout bounds every non-streaming call to a runner. A remote that accepts the
// connection and then never answers is indistinguishable from a slow one, and without a
// deadline the caller waits on that distinction forever — which is not a hypothetical: it is
// how a spawn, a cancel and an answer each hang, holding whatever goroutine issued them.
//
// Generous rather than tight. It is a backstop against a hung peer, not a latency budget.
const ControlTimeout = 30 * time.Second

func HTTP(baseURL string) task.Runner { return HTTPOver(baseURL, nil) }

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
func HTTPOver(baseURL string, tr http.RoundTripper) task.Runner {
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

// Spawn POSTs /tasks carrying the caller's id (so the remote uses it and it matches
// the task:instance entity).
func (p *httpRunner) Spawn(l task.Launch) task.Handle {
	// The launch as it crosses (task.SpawnRequest): every field of it but the two that are
	// verse's own, each absent where the spawn said nothing, which is the runner's default.
	body, err := codec.Marshal(l.Wire(), codec.Tony)
	if err != nil {
		return task.FailedHandle(fmt.Sprintf("spawn on %s: the launch does not render: %v", p.base, err))
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
		return task.FailedHandle(fmt.Sprintf("spawn refused by %s: %s", p.base, err))
	}
	return &remoteHandle{base: p.base, id: l.ID, cl: p.cl, stream: p.stream}
}

// Attach re-attaches to an existing remote task (no spawn). remoteHandle.Subscribe
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
// that was simply short. The same shape as the discarded spawn status above, on the read plane
// (git-issue 6mnmxj6mh12krz3ae1n0).
func (p *httpRunner) ListTasks() ([]task.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The error is READ, and that is not defensive tidying: NewRequest returns a nil request
	// with it, and `Do(nil)` panics rather than failing. A base is built by whoever installed
	// the runner — and by the coderunner out of a task id — so an unparseable one is a thing a
	// caller can cause, and it arrived as a panic serving HTTP.
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
	resp, err := h.stream.Do(req)
	if err != nil {
		close(out)
		cancel()
		return out, func() {}
	}
	// A rejected subscribe is not an empty one. Subscribe cannot return an error, and the
	// reader below keeps only "data: " lines, so a 4xx/5xx body would scan to nothing and
	// close the channel — leaving the mirror to conclude the remote task simply had
	// nothing to say. Report it on the plane the caller is already reading, the way a
	// cancel does below.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		resp.Body.Close()
		// WHY the subscribe failed and why the TASK failed are different facts, and only
		// one of them is worth a reader's time. A task that never started has no stream and
		// no child to forward to, so 404 is the honest answer to the question that was
		// asked — but reported as the run's outcome it says "404 page not found" where the
		// record says "the task process exited before it served anything (exit status 1) —
		// its output is on this runner's log, tagged <id>". The second is the one that ends
		// the investigation; the first cost three trips to `kubectl logs` to get past
		// (git-issue ykpt9g3eh12kss9wfhn0).
		//
		// So ask the record before reporting. Only a TERMINAL status with a reason wins:
		// a task the remote still considers live has no better account of itself than the
		// transport error, and overwriting a live task's frame with a stale record would
		// be the same mistake pointed the other way.
		if snap, ok := h.snapshot(); ok && snap.Status.Terminal() && snap.Err != "" {
			out <- task.Event{Kind: task.EventStatus, Text: string(snap.Status), Err: snap.Err}
			close(out)
			cancel()
			return out, func() {}
		}
		out <- task.Event{
			Kind: task.EventStatus,
			Text: string(task.Failed),
			Err:  fmt.Sprintf("subscribe %s: %s", h.id, msg),
		}
		close(out)
		cancel()
		return out, func() {}
	}
	// Frames come in whatever format the stream declares — Content-Type is spoken for by
	// text/event-stream, so the format rides its own header. Assuming JSON here made every
	// frame fail to parse the moment the remote's default changed, and because an
	// unparseable frame is skipped rather than reported, the symptom was a stream that
	// simply never delivered anything.
	frameFmt := codec.Tony
	if f, err := codec.Parse(resp.Header.Get(codec.HeaderFormat)); err == nil &&
		resp.Header.Get(codec.HeaderFormat) != "" {
		frameFmt = f
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue // blank line / comment
			}
			var ev task.Event
			if codec.Unmarshal([]byte(data), &ev, frameFmt) != nil {
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

func (h *remoteHandle) Pause() error  { return h.post("/pause", nil) }
func (h *remoteHandle) Resume() error { return h.post("/resume", nil) }

// Cancel ends the task, and says why: the reason is what the task's charters see, so it
// crosses with the cancel.
func (h *remoteHandle) Cancel(reason string) error {
	return h.post("/cancel", runner.CancelRequest{Reason: reason})
}

func (h *remoteHandle) Steer(instr, by, ask string) error {
	return h.post("/say", runner.SayRequest{Instruction: instr, By: by, Ask: ask})
}

func (h *remoteHandle) SetVector(v *task.Vector, by string) error {
	if v == nil {
		return fmt.Errorf("%s: no vector to set", h.id)
	}
	b, err := codec.Marshal(task.VectorRequest{Vector: *v, By: by}, codec.Tony)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, h.url("/vector"), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", codec.MediaType(codec.Tony))
	resp, err := h.cl.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", task.ErrUnreachable, h.id, err)
	}
	defer resp.Body.Close()
	return statusErr(resp)
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
	resp, err := h.cl.Get(h.url(""))
	if err != nil {
		return task.Snapshot{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return task.Snapshot{}, false
	}
	var snap task.Snapshot
	if err := decodeResp(resp, &snap); err != nil {
		return task.Snapshot{}, false
	}
	return snap, true
}

func (h *remoteHandle) Err() string {
	snap, _ := h.snapshot()
	return snap.Err
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
	var r io.Reader
	if body != nil {
		b, err := codec.Marshal(body, codec.Tony)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	resp, err := h.cl.Post(h.url(path), codec.MediaType(codec.Tony), r)
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
	case http.StatusNotImplemented:
		return fmt.Errorf("%w: %s", task.ErrUnsupported, detail)
	}
	return errors.New(detail)
}

var (
	_ task.Runner = (*httpRunner)(nil)
	_ task.Handle = (*remoteHandle)(nil)
	// Asserted here so a change to Lister is a compile error in this file rather than a
	// type assertion that quietly starts reporting false in versed.
	_ task.Lister = (*httpRunner)(nil)
)
