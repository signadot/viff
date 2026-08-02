// Package runner provides the HTTP client of the verse-facing process-control
// contract: an httpRunner drives a remote processd-shaped service at a base URL, so the
// engine can run a process on another host, k8s, or a broker exactly as it runs one in
// process. It is the mirror of versed's localRunner — same process.Runner seam,
// HTTP transport. See docs/process-control.md.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/process"
)

// HTTP returns a Runner that drives the runner at baseURL.
func HTTP(baseURL string) process.Runner {
	return &httpRunner{base: strings.TrimRight(baseURL, "/"), cl: http.DefaultClient}
}

type httpRunner struct {
	base string
	cl   *http.Client
}

// Spawn POSTs /processes carrying the caller's id (so the remote uses it and it matches
// the process:instance entity). content is not forwarded — a remote runtime resolves
// files its own way; federating a remote read_file is a later concern.
func (p *httpRunner) Spawn(l process.Launch, _ process.ContentReader) process.Handle {
	req := map[string]any{"id": l.ID, "task": l.Task, "driver": l.Driver}
	// Send only what the spawn actually said. An empty frame or an absent grant means "the
	// remote's own default", and writing them out empty would be a claim rather than an
	// absence — the grant especially, since the zero Grant permits nothing, so sending it
	// unconditionally would refuse every action of every process nobody meant to bound.
	if l.Frame != "" {
		req["frame"] = l.Frame
	}
	if len(l.Tools) > 0 {
		req["tools"] = l.Tools
	}
	if l.Answer != nil {
		req["answer"] = l.Answer
	}
	if l.Vector != nil {
		req["vector"] = l.Vector
	}
	if l.Grant != nil {
		req["grant"] = l.Grant
	}
	body, _ := codec.Marshal(req, codec.Tony)
	if resp, err := p.cl.Post(p.base+"/processes", codec.MediaType(codec.Tony), bytes.NewReader(body)); err != nil {
		return process.FailedHandle(err.Error())
	} else {
		resp.Body.Close()
	}
	return &remoteHandle{base: p.base, id: l.ID, cl: p.cl}
}

// Attach re-attaches to an existing remote process (no spawn). remoteHandle.Subscribe
// replays from the start (?since=0), so a resumed mirror re-derives current state.
func (p *httpRunner) Attach(id string) process.Handle {
	return &remoteHandle{base: p.base, id: id, cl: p.cl}
}

// Ref is the base URL — how the process:instance entity records this runner so a restart
// can reconstruct it.
func (p *httpRunner) Ref() string { return p.base }

// ListProcesses / ProcessSnapshot let versed union a remote runner's processes into the
// unified `process list`/`get` (implementing the same lister interface a broker does). Each
// call is bounded by a short timeout so an unreachable remote yields nothing rather than
// hanging the list — those processes remain visible via the entity mirror. The verse-facing
// contract already serves GET /processes and GET /processes/{id}, so this is a thin client.
func (p *httpRunner) ListProcesses() []process.Instance {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/processes", nil)
	resp, err := p.cl.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out []process.Instance
	_ = decodeResp(resp, &out)
	return out
}

func (p *httpRunner) ProcessSnapshot(id string) (process.Instance, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/processes/"+id, nil)
	resp, err := p.cl.Do(req)
	if err != nil {
		return process.Instance{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return process.Instance{}, false
	}
	var m process.Instance
	if decodeResp(resp, &m) != nil {
		return process.Instance{}, false
	}
	return m, true
}

// remoteHandle is a Handle over the wire: the read plane is GET /events (SSE), the
// control plane the matching POSTs.
type remoteHandle struct {
	base string
	id   string
	cl   *http.Client
}

// Subscribe replays the whole retained window then streams live, so the mirror sees the
// process from as far back as the remote still holds.
func (h *remoteHandle) Subscribe() (<-chan process.Frame, func()) {
	return h.SubscribeSince(0)
}

// SubscribeSince resumes from a cursor. It is the same contract the local bus and the broker
// honour, carried over HTTP by handing the cursor to the remote's own `?since=` rather than
// asking for everything and discarding — the remote is the one that knows what it still has,
// and it is the one that can say EvGap when the cursor is below its window.
func (h *remoteHandle) SubscribeSince(since int64) (<-chan process.Frame, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan process.Frame, 256)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		h.url("/events?since="+strconv.FormatInt(since, 10)), nil)
	resp, err := h.cl.Do(req)
	if err != nil {
		close(out)
		cancel()
		return out, func() {}
	}
	// A rejected subscribe is not an empty one. Subscribe cannot return an error, and the
	// reader below keeps only "data: " lines, so a 4xx/5xx body would scan to nothing and
	// close the channel — leaving the mirror to conclude the remote process simply had
	// nothing to say. Report it on the plane the caller is already reading, the way a
	// cancel does below.
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		resp.Body.Close()
		out <- process.Frame{
			Kind: process.EvStatus,
			Text: string(process.Failed),
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
			var ev process.Frame
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

func (h *remoteHandle) Pause()               { h.post("/pause", nil) }
func (h *remoteHandle) Resume()              { h.post("/resume", nil) }
func (h *remoteHandle) Cancel(reason string) { h.post("/cancel", nil) } // remote cancel takes no reason
func (h *remoteHandle) Steer(instr, by string) {
	h.post("/say", map[string]string{"instruction": instr, "by": by})
}

func (h *remoteHandle) SetVector(v *process.Vector, by string) {
	b, _ := codec.Marshal(map[string]any{"vector": v, "by": by}, codec.Tony)
	req, _ := http.NewRequest(http.MethodPut, h.url("/vector"), bytes.NewReader(b))
	req.Header.Set("Content-Type", codec.MediaType(codec.Tony))
	if resp, err := h.cl.Do(req); err == nil {
		resp.Body.Close()
	}
}

// Resolve answers a gate the remote's process is blocked on, and reports whether the answer
// landed. It is what makes a remote-hosted process answerable at all: without it, this handle
// is not a gateResolver, and every route into it fails with a message about the question
// rather than about the runner.
//
// It speaks api.Resolve — the vocabulary every other answering surface uses — rather than the
// runtime's older {decisionId, proceed}. processd accepts both, so an old runner on the other
// end still understands this.
//
// The bool is the point, and it is why this cannot go through post(): an answer that did not
// land must not read as one that did, or the operator is told a blocked worker was released.
func (h *remoteHandle) Resolve(decision string, proceed bool, say, by string) bool {
	body := api.Resolve{ID: decision, Approve: proceed, By: by}
	if say != "" {
		body.Say = []string{say}
	}
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		return false
	}
	resp, err := h.cl.Post(h.url("/resolve"), codec.MediaType(codec.Tony), bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 300
}

// Err reads the terminal failure detail off the snapshot — the remote's process.Instance
// already carries it, so this is the same read as Result against a different field.
func (h *remoteHandle) Err() string {
	resp, err := h.cl.Get(h.url(""))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var snap struct {
		Err string `tony:"field=err"`
	}
	_ = decodeResp(resp, &snap)
	return snap.Err
}

// Result reads the terminal result off the snapshot.
func (h *remoteHandle) Result() string {
	resp, err := h.cl.Get(h.url(""))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var snap struct {
		Result string `tony:"field=result"`
	}
	_ = decodeResp(resp, &snap)
	return snap.Result
}

func (h *remoteHandle) url(suffix string) string { return h.base + "/processes/" + h.id + suffix }

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

func (h *remoteHandle) post(path string, body any) {
	var r io.Reader
	if body != nil {
		b, _ := codec.Marshal(body, codec.Tony)
		r = bytes.NewReader(b)
	}
	if resp, err := h.cl.Post(h.url(path), codec.MediaType(codec.Tony), r); err == nil {
		resp.Body.Close()
	}
}

var (
	_ process.Runner = (*httpRunner)(nil)
	_ process.Handle = (*remoteHandle)(nil)
)
