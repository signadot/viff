// Package controlpolicy provides the HTTP client of the verse-facing controlPolicy
// contract: an httpPolicy drives a remote processd-shaped service at a base URL, so the
// engine can run a process on another host, k8s, or a broker exactly as it runs one in
// process. It is the mirror of versed's localPolicy — same process.ControlPolicy seam,
// HTTP transport. See docs/control-policy.md.
package controlpolicy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/signadot/verse/process"
)

// HTTP returns a ControlPolicy that drives the controlPolicy at baseURL.
func HTTP(baseURL string) process.ControlPolicy {
	return &httpPolicy{base: strings.TrimRight(baseURL, "/"), cl: http.DefaultClient}
}

type httpPolicy struct {
	base string
	cl   *http.Client
}

// Spawn POSTs /processes carrying the caller's id (so the remote uses it and it matches
// the process:instance entity). content is not forwarded — a remote runtime resolves
// files its own way; federating a remote read_file is a later concern.
func (p *httpPolicy) Spawn(id, task, driver string, _ process.ContentReader) process.ProcHandle {
	body, _ := json.Marshal(map[string]string{"id": id, "task": task, "driver": driver})
	if resp, err := p.cl.Post(p.base+"/processes", "application/json", bytes.NewReader(body)); err != nil {
		return failedHandle{err.Error()}
	} else {
		resp.Body.Close()
	}
	return &remoteHandle{base: p.base, id: id, cl: p.cl}
}

// Attach re-attaches to an existing remote process (no spawn). remoteHandle.Subscribe
// replays from the start (?since=0), so a resumed mirror re-derives current state.
func (p *httpPolicy) Attach(id string) process.ProcHandle {
	return &remoteHandle{base: p.base, id: id, cl: p.cl}
}

// Ref is the base URL — how the process:instance entity records this policy so a restart
// can reconstruct it.
func (p *httpPolicy) Ref() string { return p.base }

// ListProcesses / ProcessSnapshot let versed union a remote policy's processes into the
// unified `process list`/`get` (implementing the same lister interface a broker does). Each
// call is bounded by a short timeout so an unreachable remote yields nothing rather than
// hanging the list — those processes remain visible via the entity mirror. The verse-facing
// contract already serves GET /processes and GET /processes/{id}, so this is a thin client.
func (p *httpPolicy) ListProcesses() []map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/processes", nil)
	resp, err := p.cl.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (p *httpPolicy) ProcessSnapshot(id string) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/processes/"+id, nil)
	resp, err := p.cl.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var m map[string]any
	if json.NewDecoder(resp.Body).Decode(&m) != nil {
		return nil, false
	}
	return m, true
}

// remoteHandle is a ProcHandle over the wire: the read plane is GET /events (SSE), the
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
			if json.Unmarshal([]byte(data), &ev) != nil {
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

func (h *remoteHandle) SetVector(v *process.Vector) {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest(http.MethodPut, h.url("/vector"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
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
		Result string `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	return snap.Result
}

func (h *remoteHandle) url(suffix string) string { return h.base + "/processes/" + h.id + suffix }

func (h *remoteHandle) post(path string, body any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	if resp, err := h.cl.Post(h.url(path), "application/json", r); err == nil {
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
func (failedHandle) Pause()                    {}
func (failedHandle) Resume()                   {}
func (failedHandle) Cancel(string)             {}
func (failedHandle) Steer(string, string)      {}
func (failedHandle) SetVector(*process.Vector) {}
func (failedHandle) Result() string            { return "" }

var (
	_ process.ControlPolicy = (*httpPolicy)(nil)
	_ process.ProcHandle    = (*remoteHandle)(nil)
	_ process.ProcHandle    = failedHandle{}
)
