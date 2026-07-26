package codec

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
)

// HeaderFormat names the format of an SSE stream's frame payloads.
//
// It exists because Content-Type is spoken for: an SSE response must be
// text/event-stream, which says what the FRAMING is and nothing about what is inside a
// frame. A reader that wants to decode frames rather than print them needs to be told,
// and guessing is exactly what this package exists to avoid.
const HeaderFormat = "X-Verse-Format"

// SSE writes a server-sent event stream whose frames carry documents in the format the
// request asked for.
type SSE struct {
	w   http.ResponseWriter
	fl  http.Flusher
	f   Format
	err error
}

// NewSSE sets the stream headers and returns a writer, or an error if the
// ResponseWriter cannot stream. It does NOT write a status — the caller may still want to
// fail the request after inspecting something.
func NewSSE(w http.ResponseWriter, r *http.Request) (*SSE, error) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	// A stream's frames carry the same rendering an ordinary response body would, so a
	// format verse cannot write is refused here too — before any header goes out, while
	// the caller can still turn it into a status.
	f, err := ResponseFormat(r)
	if err != nil {
		return nil, err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set(HeaderFormat, f.String())
	return &SSE{w: w, fl: fl, f: f}, nil
}

// Format is the format frames are written in.
func (s *SSE) Format() Format { return s.f }

// Err reports the first write or encoding failure, if any.
func (s *SSE) Err() error { return s.err }

// Send writes one value as a frame.
func (s *SSE) Send(v any) error {
	node, err := ToNode(v)
	if err != nil {
		return s.fail(err)
	}
	return s.SendNode(node)
}

// SendNode writes one document as a frame.
//
// The document is rendered in WIRE form — compact, single line — so one frame is one
// `data:` line and a reader can stay line-oriented. YAML is the exception: it has no
// single-line form, so its frames are split across several `data:` lines, which the SSE
// spec joins back with newlines. Handling that here rather than at each call site is the
// reason this type exists at all.
func (s *SSE) SendNode(n *ir.Node) error {
	if s.err != nil {
		return s.err
	}
	body, err := NodeBytesWire(n, s.f)
	if err != nil {
		return s.fail(err)
	}
	text := strings.TrimRight(string(body), "\n")
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n') // the blank line that ends a frame
	if _, err := s.w.Write([]byte(b.String())); err != nil {
		return s.fail(err)
	}
	s.fl.Flush()
	return nil
}

// Flush pushes buffered output — used to open the stream before the first event, so a
// client knows it is connected.
func (s *SSE) Flush() { s.fl.Flush() }

func (s *SSE) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return err
}
