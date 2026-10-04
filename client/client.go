// Package client is how a program outside the verse talks to one: the operator API over HTTP, in
// the wire format, stamped with who is asking and which contract it speaks.
//
// The `verse` CLI is one such program and so is a connector writing back what it did, a runner or
// a resolver; each builds a Client with the address it was given and the principal it acts as, and
// every request it makes goes through the same URL building, format negotiation and stamping.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
)

// Client is one verse's operator API, seen from outside it.
type Client struct {
	// Addr is the verse's host:port, the spelling VERSE_ADDR and `-addr` take.
	Addr string
	// Scope, when set, is folded into every request: a copy-on-write federation overlay isolated
	// from baseline.
	Scope string
	// Wire is the format request bodies are RENDERED in, and what the format parameter tells the
	// server to read and answer in. The zero value is tony, the one format that carries a tagged
	// document through unflattened; a format verse cannot render (json) falls back to it.
	Wire codec.Format
	// Accept is the response format asked for, with tony as the fallback. The zero value is tony.
	Accept codec.Format
	// HTTP sends the requests. Its transport is where the version and principal are stamped (see
	// Transport); nil is http.DefaultClient, which stamps neither.
	HTTP *http.Client
}

// New is the Client for a program acting as principal against the verse at addr, stamping this
// build's contract version and that principal on every request.
func New(addr, principal string) *Client {
	return &Client{
		Addr: addr,
		HTTP: &http.Client{Transport: Transport{Version: api.Version(), Principal: Static(principal)}},
	}
}

// BearerFunc is how a program proves who it is: a token, asked for on every request so that one
// its runtime rotates is read fresh. nil proves nothing, and the program names itself instead.
type BearerFunc func(ctx context.Context) (string, error)

// NewAs is the Client for a program that proves itself with bearer where it has one, and names
// principal where it has none: an io with its workload token, or without.
func NewAs(addr, principal string, bearer BearerFunc) *Client {
	return &Client{Addr: addr, HTTP: &http.Client{Transport: Transport{
		Version: api.Version(), Principal: Static(principal), Bearer: bearer,
	}}}
}

// NewRelay is the Client for a verified program acting for a person it identified: it carries the
// program's bearer, the person, and the person's account in the system the program speaks for,
// `<provider>:<id>`, and versed admits the request as the person through that account's binding.
// With no bearer the person and the account are asserted, which a verse that verifies people
// refuses. With no account there is nothing to relay through, so the program asserts the
// principal and sends no bearer, as a program with none does: a bearer beside a bare name would
// make the request the program's own.
func NewRelay(addr, principal, account string, bearer BearerFunc) *Client {
	if account == "" {
		return New(addr, principal)
	}
	return &Client{Addr: addr, HTTP: &http.Client{Transport: Transport{
		Version: api.Version(), Principal: Static(principal), Bearer: bearer, For: Static(account),
	}}}
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

func (c *Client) wire() codec.Format {
	if codec.Writable(c.Wire) {
		return c.Wire
	}
	return codec.Tony
}

// URL builds a request URL for path, with the scope and the wire format folded into q.
//
// The format is said explicitly on every request because the parameter governs how the server
// READS a body as well as how it writes a response, so it must be the format the body is written
// in and nothing else.
//
// The URL is built rather than concatenated, so the path gets ESCAPED. Ids reach here from wherever
// a caller got them, so they may contain anything. A "#" is the one that punishes concatenation: it
// starts a fragment, which silently truncates the path and takes the query string with it, and the
// request then succeeds against the WRONG route, with no scope and no format. Escaped, it is %23
// and the server 404s, which is what a bad id should do. It is also why query parameters come in
// q and never in path: a "?" in the path arrives as %3F.
func (c *Client) URL(path string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	if c.Scope != "" {
		q.Set("scope", c.Scope)
	}
	q.Set(codec.FormatParam, c.wire().String())
	u := url.URL{Scheme: "http", Host: c.Addr, Path: path}
	u.RawQuery = q.Encode()
	return u.String()
}

// Do sends a request the caller built.
func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.http().Do(req) }

// Get reads path.
func (c *Client) Get(path string, q url.Values) (*http.Response, error) {
	return c.http().Get(c.URL(path, q))
}

// Post sends body, rendered in the wire format.
func (c *Client) Post(path string, q url.Values, body any) (*http.Response, error) {
	b, err := codec.Marshal(body, c.wire())
	if err != nil {
		return nil, err
	}
	return c.send(http.MethodPost, c.URL(path, q), b)
}

// PostNode sends a document already built, so one parsed from a file goes to the server AS itself
// rather than rendered back to bytes in some intermediate format: a tagged charter arrives as tags.
func (c *Client) PostNode(path string, q url.Values, node *ir.Node) (*http.Response, error) {
	b, err := codec.NodeBytes(node, c.wire())
	if err != nil {
		return nil, err
	}
	return c.send(http.MethodPost, c.URL(path, q), b)
}

// PostNodes sends SEVERAL documents in one request, `---` separated: the multi-document spelling a
// charter file is written in, and what codec.ReadNodes reads back.
//
// Not an array of them, and the difference is not cosmetic: a definition at document top level is
// the node the file wrote, where making it an element of a list re-renders it one indent in, and a
// comment beside a flow map does not survive that trip. A charter carries its author's reasons, so
// the shape that keeps them is the shape to send.
func (c *Client) PostNodes(path string, q url.Values, nodes []*ir.Node) (*http.Response, error) {
	var body bytes.Buffer
	for i, n := range nodes {
		if i > 0 {
			body.WriteString("---\n")
		}
		b, err := codec.NodeBytes(n, c.wire())
		if err != nil {
			return nil, err
		}
		body.Write(b)
		if !bytes.HasSuffix(b, []byte("\n")) {
			body.WriteByte('\n') // a separator on the same line as a value would be part of it
		}
	}
	return c.send(http.MethodPost, c.URL(path, q), body.Bytes())
}

// Put replaces what stands at path with body.
func (c *Client) Put(path string, body any) (*http.Response, error) {
	b, err := codec.Marshal(body, c.wire())
	if err != nil {
		return nil, err
	}
	return c.send(http.MethodPut, c.URL(path, nil), b)
}

// Patch sends body as a change to what stands at path.
func (c *Client) Patch(path string, body any) (*http.Response, error) {
	b, err := codec.Marshal(body, c.wire())
	if err != nil {
		return nil, err
	}
	return c.send(http.MethodPatch, c.URL(path, nil), b)
}

// Delete removes what stands at path.
func (c *Client) Delete(path string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, c.URL(path, q), nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// PostURL sends body to an ABSOLUTE url: a task's broker face, which lives on a sibling port rather
// than at Addr.
func (c *Client) PostURL(u string, body any) (*http.Response, error) {
	b, err := codec.Marshal(body, c.wire())
	if err != nil {
		return nil, err
	}
	return c.send(http.MethodPost, u, b)
}

func (c *Client) send(method, u string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.SetFormatHeaders(req)
	return c.Do(req)
}

// SetFormatHeaders declares what is being sent and what is wanted back. The Accept lists tony as a
// fallback, so a server that cannot produce the preferred format still answers with something this
// client reads rather than failing the negotiation. It never names json, the one rendering verse
// refuses.
func (c *Client) SetFormatHeaders(req *http.Request) {
	req.Header.Set("Content-Type", codec.MediaType(c.wire()))
	accept := c.Accept
	if !codec.Writable(accept) {
		accept = codec.Tony
	}
	req.Header.Set("Accept", codec.MediaType(accept)+", "+codec.MediaTony+";q=0.5")
}

// StatusError is the refusal a request came back with, in the words the server used.
//
// A verse refusal is a SENTENCE — "stratum X still holds 3 rules, removing it would leave them
// installed and unchecked" — written into the response body on purpose, and reporting only
// `400 Bad Request` throws away the whole of what the server said. The status is the fallback, for
// a server that refused without saying anything.
func StatusError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("%s: %w", what, answered(resp, body))
}

// OKBody reads a response body, turning a 4xx/5xx into the error it carries.
func OKBody(resp *http.Response) ([]byte, error) {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, answered(resp, body)
	}
	return body, nil
}

// Error is what a server answered in place of what was asked for: its status, and its words.
//
// It READS as the words alone, since the sentence is what a person acts on. The status is for
// the program: the three answers a read of one thing has besides the thing are three different
// next steps — look somewhere else, ask whoever owns it, ask again in a moment — and a caller
// that has only the sentence can choose between them only by matching text.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func answered(resp *http.Response, body []byte) *Error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return &Error{Status: resp.StatusCode, Message: msg}
}

// statusOf is the status err carries, and 0 for an error that is not a server's answer — a
// connection refused, a body that did not decode.
func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// NotInView reports whether err is the answer for what the asker's view does not hold: it is
// not there, or it is not theirs to read. One answer, which says neither (404).
func NotInView(err error) bool { return statusOf(err) == http.StatusNotFound }

// Refused reports whether err is the permission check turning the request away (403).
func Refused(err error) bool { return statusOf(err) == http.StatusForbidden }

// Unavailable reports whether err is a request that was not carried out because something
// behind the server did not answer — its store, a runner (502, 503, 504). Nothing is known to
// be missing, and asking again is the next step.
func Unavailable(err error) bool {
	switch statusOf(err) {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ResponseFormat is the format a response declares: a fact about the server, read off the reply
// rather than assumed. An unlabelled body is read as tony, which covers a JSON-speaking server too,
// since JSON is parsed by the tony parser.
func ResponseFormat(resp *http.Response) codec.Format {
	if f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type")); ok {
		return f
	}
	return codec.Tony
}

// Decode reads a response into v in the format it declared, without closing it.
func Decode(resp *http.Response, v any) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return codec.Unmarshal(data, v, ResponseFormat(resp))
}

// DecodeClose is Decode, closing the body.
func DecodeClose(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return Decode(resp, v)
}
