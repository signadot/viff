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
	"strings"
	"time"

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
func (p *httpRunner) Spawn(id, task, driver string, _ process.ContentReader) process.Handle {
	body, _ := codec.Marshal(map[string]string{"id": id, "task": task, "driver": driver}, codec.Tony)
	if resp, err := p.cl.Post(p.base+"/processes", codec.MediaType(codec.Tony), bytes.NewReader(body)); err != nil {
		return failedHandle{err.Error()}
	} else {
		resp.Body.Close()
	}
	return &remoteHandle{base: p.base, id: id, cl: p.cl}
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

func (h *remoteHandle) Subscribe() (<-chan process.Frame, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan process.Frame, 256)
	// since=0: replay the whole retained window then stream live, so the mirror sees the
	// process from the start (and, later, a resume passes the last mirrored cursor).
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.url("/events?since=0"), nil)
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

// failedHandle stands in when a remote spawn could not be issued: it reports a terminal
// status so the mirror doesn't strand the entity as "running".
type failedHandle struct{ err string }

func (f failedHandle) Subscribe() (<-chan process.Frame, func()) {
	ch := make(chan process.Frame, 1)
	ch <- process.Frame{Kind: process.EvStatus, Text: "cancelled", Err: f.err}
	close(ch)
	return ch, func() {}
}
func (failedHandle) Pause()                            {}
func (failedHandle) Resume()                           {}
func (failedHandle) Cancel(string)                     {}
func (failedHandle) Steer(string, string)              {}
func (failedHandle) SetVector(*process.Vector, string) {}
func (failedHandle) Result() string                    { return "" }

var (
	_ process.Runner = (*httpRunner)(nil)
	_ process.Handle = (*remoteHandle)(nil)
	_ process.Handle = failedHandle{}
)
