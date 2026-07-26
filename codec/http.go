package codec

import (
	"fmt"
	"io"
	"net/http"

	"github.com/signadot/tony-format/go-tony/ir"
)

// HTTP content negotiation.
//
// PRECEDENCE IS `?format=` → HEADER → DEFAULT, and the query parameter is not decoration.
// An SSE endpoint must answer `Accept: text/event-stream`, so the header is unavailable to
// say anything about the payload inside the frames; the parameter is the only channel that
// works uniformly across streaming and non-streaming routes. It is also the lever a person
// reaches for with curl.
//
// The default is whatever the process set with SetDefault — JSON until the flip, so an
// existing client that sends no headers keeps getting exactly what it got before.

// FormatParam is the query parameter that names a format, on any route.
const FormatParam = "format"

// RequestFormat is the format a request BODY is written in.
func RequestFormat(r *http.Request) Format {
	if f, ok := paramFormat(r); ok {
		return f
	}
	if f, ok := FormatForMedia(r.Header.Get("Content-Type")); ok {
		return f
	}
	return Default()
}

// ResponseFormat is the format a response body should be written in.
func ResponseFormat(r *http.Request) Format {
	if f, ok := paramFormat(r); ok {
		return f
	}
	if a := r.Header.Get("Accept"); a != "" {
		return Negotiate(a, Default())
	}
	return Default()
}

// paramFormat reads ?format=. An unparseable value is ignored rather than failing the
// request: it is a rendering preference, and a 400 on `?format=tny` would be a worse
// answer than the default rendering.
func paramFormat(r *http.Request) (Format, bool) {
	v := r.URL.Query().Get(FormatParam)
	if v == "" {
		return Default(), false
	}
	f, err := Parse(v)
	if err != nil {
		return Default(), false
	}
	return f, true
}

// Write renders v as the response body in the format the request asked for.
//
// An encoding failure after the status line is unrecoverable — the header is already sent —
// so it is written into the body rather than swallowed, which at least leaves a trace at the
// point of failure instead of a truncated document.
func Write(w http.ResponseWriter, r *http.Request, code int, v any) {
	node, err := ToNode(v)
	if err != nil {
		HTTPError(w, r, http.StatusInternalServerError, "encoding the response: "+err.Error())
		return
	}
	WriteNode(w, r, code, node)
}

// WriteNode writes an already-built document.
func WriteNode(w http.ResponseWriter, r *http.Request, code int, n *ir.Node) {
	f := ResponseFormat(r)
	body, err := NodeBytes(n, f)
	if err != nil {
		HTTPError(w, r, http.StatusInternalServerError, "encoding the response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", MediaType(f))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// Read decodes a request body into v, in the format the request declared.
//
// AN EMPTY BODY IS NOT AN ERROR: several handlers POST with no body at all and rely on the
// decode being a no-op, so this returns nil and leaves v alone. Malformed input is still an
// error — the distinction is "nothing was sent" versus "something unreadable was".
func Read(r *http.Request, v any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading the request body: %w", err)
	}
	node, err := ParseNodeOrNil(data, RequestFormat(r))
	if err != nil {
		return fmt.Errorf("decoding the request body: %w", err)
	}
	if node == nil {
		return nil // no body
	}
	return FromNode(node, v)
}

// ReadNode reads a request body as a document, for a handler that forwards it rather than
// decoding it into a type.
func ReadNode(r *http.Request) (*ir.Node, error) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the request body: %w", err)
	}
	return ParseNode(data, RequestFormat(r))
}

// HTTPError writes an error response.
//
// The body is PLAIN TEXT, deliberately. Callers already read a non-2xx body as a message
// (see the CLI), a human running curl is better served by a sentence than by a document,
// and making errors negotiated would be an API-compatibility surface of its own. Routing
// every error through here means that decision is in one place if it ever changes.
func HTTPError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	http.Error(w, msg, code)
}
