// Package mcpclient is the runner's side of the mcp system: the client that calls remote MCP
// servers for a task. The runner holds each server's credential and makes each call; the task's
// process never reaches a server. One task has one session with each server it calls, opened at
// the first use and closed when the task's channel is done.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
)

const (
	// Timeout is how long a call of a server's tool has where the server names no `timeout`,
	// and TimeoutMost the longest one may name: the table's deadline for `mcp.call` is that,
	// and a call applies the server's own within it.
	Timeout     = time.Minute
	TimeoutMost = 10 * time.Minute
	// ListTimeout is how long a server has to answer its list of tools, at a task's start: a
	// slow server delays the start by this and no more.
	ListTimeout = 15 * time.Second
	// ReadMost is the most of one response the client reads, and a response past it fails the
	// call: what is passed on to the task is bounded smaller, by whoever passes it.
	ReadMost = 8 << 20
)

// Server is one remote MCP server the runner calls, as the configuration names it.
type Server struct {
	// URL is the server's streamable HTTP endpoint.
	URL string
	// Secret is the name, in the store, of the credential the server's requests carry; "" sends
	// none. It is read at each request, so a rotation holds for the next call of a running task.
	Secret string
	// Header is the header the credential goes in, as it is; "" is `Authorization: Bearer <value>`.
	Header string
	// Timeout is how long one call has; 0 is Timeout.
	Timeout time.Duration
}

// Servers is the servers the runner calls, by name.
type Servers map[string]Server

// Sessions is one task's sessions with the servers, one for each server, opened at the first
// use (Call, List) and closed together (Close). Several calls may use one session at once.
//
// A SESSION THE SERVER ENDED IS OPENED AGAIN, ONCE A CALL. A server may end an idle session
// (one does after 30 minutes with no request), and a task may wait longer than that between
// calls. Where the server answers the call's own POST that it does not know the session (404 on
// its id, which the SDK answers as mcp.ErrSessionMissing), the server took nothing of the call:
// a new session is opened and the call sent on it, once. Several calls finding the session gone
// at once open one new session between them, under the server's lock.
//
// A CALL IS NEVER SENT TWICE. A response cut after the call was sent is resumed where the
// server gives event ids: the SDK's transport asks for the rest with `Last-Event-ID`, a GET that
// does not send the call again. Where it gives none, or the resumption fails, the call fails,
// and whether the server did it is not known. The SDK fails the whole connection on a 404, and
// every call in flight on it hears mcp.ErrSessionMissing, so that alone does not say whose POST
// was refused: each call marks its own (mark), through the request's context, and only a call
// whose own POST was refused is sent again.
type Sessions struct {
	servers Servers
	store   *secretstore.Store
	log     io.Writer
	// transport is the one HTTP transport under every session, closed with them.
	transport *http.Transport
	// readMost is ReadMost, which a test lowers.
	readMost int64

	mu     sync.Mutex
	open   map[string]*session
	closed bool
}

// session is one task's session with one server: cs is nil until opened, and replaced where
// the server ended it. mu holds the opening, so two calls finding none open one.
type session struct {
	mu sync.Mutex
	cs *mcp.ClientSession
}

// New is a task's sessions with servers, each credential read from store at each request, none
// open yet. log takes what goes wrong, and never a credential's value.
func New(servers Servers, store *secretstore.Store, log io.Writer) *Sessions {
	if log == nil {
		log = io.Discard
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	return &Sessions{servers: servers, store: store, log: log, transport: t, readMost: ReadMost, open: map[string]*session{}}
}

// Call calls tool of server with arguments, a JSON object, within the server's timeout. What is
// sent is arguments as they are. The result is the server's, an error of the server's included
// (CallToolResult.IsError); an error is the call not made, or made and its answer not read.
func (s *Sessions) Call(ctx context.Context, server, tool string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	srv, ok := s.servers[server]
	if !ok {
		return nil, fmt.Errorf("this runner calls no server %s", server)
	}
	timeout := srv.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var result *mcp.CallToolResult
	err := s.with(ctx, server, func(ctx context.Context, cs *mcp.ClientSession) error {
		var err error
		result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// List is the tools server offers, every page, and the server's instructions (what it said at
// `initialize`), within ListTimeout.
func (s *Sessions) List(ctx context.Context, server string) (tools []*mcp.Tool, instructions string, err error) {
	if _, ok := s.servers[server]; !ok {
		return nil, "", fmt.Errorf("this runner calls no server %s", server)
	}
	ctx, cancel := context.WithTimeout(ctx, ListTimeout)
	defer cancel()
	err = s.with(ctx, server, func(ctx context.Context, cs *mcp.ClientSession) error {
		tools, instructions = nil, ""
		for t, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			tools = append(tools, t)
		}
		if r := cs.InitializeResult(); r != nil {
			instructions = r.Instructions
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return tools, instructions, nil
}

// mark is one call's, carried in its context to the requests the SDK makes for it: whether
// the server refused the call's own POST as of a session it does not know, which is the one
// case the call is known not to have been taken.
type mark struct{ refused atomic.Bool }

type markKey struct{}

// with runs f on the task's session with server, opening one where there is none; where the
// server answers the call's own request that it does not know the session, on a new one, once.
// Where the session ended under the call with its request taken, the call fails, saying that
// whether it was performed is not known, and the session is let go for the next call to open
// another.
func (s *Sessions) with(ctx context.Context, server string, f func(context.Context, *mcp.ClientSession) error) error {
	cs, err := s.session(ctx, server, nil)
	if err != nil {
		return err
	}
	m := &mark{}
	ctx = context.WithValue(ctx, markKey{}, m)
	err = f(ctx, cs)
	if !errors.Is(err, mcp.ErrSessionMissing) {
		return err
	}
	if !m.refused.Load() {
		s.drop(server, cs)
		return fmt.Errorf("%s: the server ended the session with the call in flight, and whether it performed the call is not known: %w", server, err)
	}
	fmt.Fprintf(s.log, "mcp: %s: the server ended the session; opening another\n", server)
	cs, err = s.session(ctx, server, cs)
	if err != nil {
		return err
	}
	return f(ctx, cs)
}

// drop lets go of cs, a session the server ended, where it is still the task's with server:
// the next call opens another.
func (s *Sessions) drop(server string, cs *mcp.ClientSession) {
	s.mu.Lock()
	st, ok := s.open[server]
	s.mu.Unlock()
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cs == cs {
		cs.Close()
		st.cs = nil
	}
}

// session is the task's session with server, opened where there is none. gone is a session the
// server ended, which is replaced: where another call replaced it already, that call's is the
// answer, so several calls finding it gone open one between them.
func (s *Sessions) session(ctx context.Context, server string, gone *mcp.ClientSession) (*mcp.ClientSession, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("the task's sessions are closed")
	}
	st, ok := s.open[server]
	if !ok {
		st = &session{}
		s.open[server] = st
	}
	s.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cs != nil && st.cs != gone {
		return st.cs, nil
	}
	if st.cs != nil {
		st.cs.Close() // the server knows nothing of it; this frees the client's side
		st.cs = nil
	}
	cs, err := s.connect(ctx, server)
	if err != nil {
		return nil, err
	}
	st.cs = cs
	return cs, nil
}

// connect opens a session with server: the client declares no capability (no roots, no
// sampling, no elicitation), follows no redirect, reads no more than readMost of a response, and
// keeps no standalone stream. The credential is set on each request as it goes (authorized).
func (s *Sessions) connect(ctx context.Context, server string) (*mcp.ClientSession, error) {
	srv := s.servers[server]
	hc := &http.Client{
		Transport: &authorized{rt: s.transport, server: server, srv: srv, store: s.store, readMost: s.readMost, log: s.log},
		// A server does not choose where the credential goes: a redirect is the call's failure.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "verse-runner", Version: api.Version()},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	t := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, MaxRetries: 5,
		DisableStandaloneSSE: true, MaxEventSize: int(s.readMost)}
	cs, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: opening a session: %w", server, err)
	}
	return cs, nil
}

// Close ends every session, telling each server, and closes the connections under them. A call
// after it fails. The servers are told at once, since a server slow to end a session holds its
// telling for the SDK's bound (5s), and a call still in flight on the server's side is given up
// by whoever made it, not waited on here.
func (s *Sessions) Close() error {
	s.mu.Lock()
	s.closed = true
	open := s.open
	s.open = map[string]*session{}
	s.mu.Unlock()
	var (
		wg   sync.WaitGroup
		errs = make([]error, 0, len(open))
		mu   sync.Mutex
	)
	for name, st := range open {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.mu.Lock()
			defer st.mu.Unlock()
			if st.cs == nil {
				return
			}
			if err := st.cs.Close(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				mu.Unlock()
			}
			st.cs = nil
		}()
	}
	wg.Wait()
	s.transport.CloseIdleConnections()
	return errors.Join(errs...)
}

// authorized is the round tripper under a server's requests: it sets the credential on each,
// read from the store as the request goes, and bounds what is read of each response. The
// request is sent as it is otherwise.
type authorized struct {
	rt       http.RoundTripper
	server   string
	srv      Server
	store    *secretstore.Store
	readMost int64
	log      io.Writer
}

func (a *authorized) RoundTrip(req *http.Request) (*http.Response, error) {
	if a.srv.Secret != "" {
		value, err := a.store.Get(a.srv.Secret)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.server, err)
		}
		req = req.Clone(req.Context())
		if a.srv.Header != "" {
			req.Header.Set(a.srv.Header, value)
		} else {
			req.Header.Set("Authorization", "Bearer "+value)
		}
	}
	resp, err := a.rt.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// A call's own POST refused as of a session the server does not know: the call was not
	// taken, which the call reads (mark).
	if req.Method == http.MethodPost && resp.StatusCode == http.StatusNotFound && req.Header.Get("Mcp-Session-Id") != "" {
		if m, ok := req.Context().Value(markKey{}).(*mark); ok {
			m.refused.Store(true)
		}
	}
	resp.Body = &bounded{rc: resp.Body, most: a.readMost, left: a.readMost, server: a.server, log: a.log}
	return resp, nil
}

// bounded is a response's body read up to a bound, and an error past it: one byte past is read
// to tell a response of the bound's size from one over it. The SDK reads the error as the
// response's end and fails the call without saying why, so the log says.
type bounded struct {
	rc         io.ReadCloser
	most, left int64
	server     string
	log        io.Writer
	said       bool
}

func (b *bounded) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, b.over()
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n - 1, b.over()
	}
	return n, err
}

func (b *bounded) over() error {
	err := fmt.Errorf("%s: the response is over the %d bytes the runner reads of one, and the call fails", b.server, b.most)
	if !b.said {
		b.said = true
		fmt.Fprintf(b.log, "mcp: %v\n", err)
	}
	return err
}

func (b *bounded) Close() error { return b.rc.Close() }
