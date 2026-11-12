package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/runner/runnerd/secretstore"
	"github.com/signadot/verse/pkg/systems"
)

// seen is one request a test server saw: the JSON-RPC method of its body ("" for a GET or a
// DELETE), and the header the credential would be in.
type seen struct {
	method string
	auth   string
	header http.Header
}

// spy records each request a server sees, by its method, and passes it on whole.
type spy struct {
	mu  sync.Mutex
	saw []seen
}

func (s *spy) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		s.mu.Lock()
		s.saw = append(s.saw, seen{method: body.Method, auth: r.Header.Get("Authorization"), header: r.Header.Clone()})
		s.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

// count is how many requests of method the server saw.
func (s *spy) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.saw {
		if r.method == method {
			n++
		}
	}
	return n
}

// auths is the credential each request of method carried.
func (s *spy) auths(method string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.saw {
		if r.method == method {
			out = append(out, r.auth)
		}
	}
	return out
}

// echo is a server with the tool `echo`, which answers its arguments as text, counting its
// calls.
func echo(t *testing.T, calls *atomic.Int32, opts *mcp.ServerOptions) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, opts)
	srv.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if calls != nil {
				calls.Add(1)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
		})
	return srv
}

// serve is srv over streamable HTTP, behind each middleware in turn (the first outermost).
func serve(t *testing.T, srv *mcp.Server, opts *mcp.StreamableHTTPOptions, middleware ...func(http.Handler) http.Handler) string {
	t.Helper()
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, opts)
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

// store is a secret store with the secrets given, and a way to change one.
func store(t *testing.T, secrets map[string]string) (*secretstore.Store, func(name, value string)) {
	t.Helper()
	dir := t.TempDir()
	set := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range secrets {
		set(name, value)
	}
	return secretstore.Open(dir), set
}

// logged is a log several calls write at once, as the runner's is.
type logged struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(p)
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.String()
}

// sessions is a task's sessions with servers, closed with the test, logging to log.
func sessions(t *testing.T, servers Servers, st *secretstore.Store, log io.Writer) *Sessions {
	t.Helper()
	s := New(servers, st, log)
	t.Cleanup(func() { s.Close() })
	return s
}

// text is the text of a result's first content, taking a Call's answer whole.
func text(t *testing.T) func(*mcp.CallToolResult, error) string {
	return func(r *mcp.CallToolResult, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Content) == 0 {
			t.Fatal("a result with no content")
		}
		tc, ok := r.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("content %T", r.Content[0])
		}
		return tc.Text
	}
}

// THE CREDENTIAL IS READ AT EACH REQUEST, from the store, and goes as `Bearer` or in the header
// named; a server with no `auth` is sent none. One session carries a task's calls: the second
// call initializes nothing. No log and no result holds a value.
func TestTheCredentialIsReadAtEachRequest(t *testing.T) {
	sp := &spy{}
	url := serve(t, echo(t, nil, nil), nil, sp.wrap)
	st, set := store(t, map[string]string{"k": "v1", "api": "key-1"})
	var log logged
	s := sessions(t, Servers{
		"bearer": {URL: url, Secret: "k"},
		"header": {URL: url, Secret: "api", Header: "X-Api-Key"},
		"open":   {URL: url},
	}, st, &log)
	ctx := context.Background()
	if got := text(t)(s.Call(ctx, "bearer", "echo", json.RawMessage(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("echo answered %q", got)
	}
	set("k", "v2")
	if _, err := s.Call(ctx, "bearer", "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Every request of the session carries the value as it is when the request goes: the
	// initialize and the first call v1, the second call v2.
	auths := sp.auths("tools/call")
	if len(auths) != 2 || auths[0] != "Bearer v1" || auths[1] != "Bearer v2" {
		t.Errorf("the calls carried %q, want v1 then v2", auths)
	}
	if n := sp.count("initialize"); n != 1 {
		t.Errorf("two calls initialized %d times, want one session", n)
	}
	if _, err := s.Call(ctx, "header", "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(ctx, "open", "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	sp.mu.Lock()
	var header, open []string
	for _, r := range sp.saw {
		if r.method != "tools/call" {
			continue
		}
		if r.header.Get("X-Api-Key") != "" {
			header = append(header, r.header.Get("X-Api-Key"))
		} else if r.auth == "" {
			open = append(open, "none")
		}
	}
	sp.mu.Unlock()
	if len(header) != 1 || header[0] != "key-1" || len(open) != 1 {
		t.Errorf("the named header carried %q, and %d calls carried nothing (want 1)", header, len(open))
	}
	for _, v := range []string{"v1", "v2", "key-1"} {
		if strings.Contains(log.String(), v) {
			t.Errorf("the log holds a value")
		}
	}
	// A secret not in the store fails the call, naming the secret and no value, and nothing is
	// sent.
	before := len(sp.saw)
	if _, err := sessions(t, Servers{"x": {URL: url, Secret: "missing"}}, st, &log).Call(ctx, "x", "echo", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "the secret missing") {
		t.Errorf("a missing secret: %v", err)
	}
	if len(sp.saw) != before {
		t.Errorf("a call with no credential reached the server")
	}
}

// TWO CALLS AT ONCE SHARE ONE SESSION, and both answer.
func TestCallsAtOnceShareOneSession(t *testing.T) {
	sp := &spy{}
	var calls atomic.Int32
	url := serve(t, echo(t, &calls, nil), nil, sp.wrap)
	s := sessions(t, Servers{"s": {URL: url}}, nil, io.Discard)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Call(context.Background(), "s", "echo", json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)))
			if err == nil && r.Content[0].(*mcp.TextContent).Text != fmt.Sprintf(`{"i":%d}`, i) {
				err = fmt.Errorf("call %d answered %q", i, r.Content[0].(*mcp.TextContent).Text)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Error(err)
	}
	if n := sp.count("initialize"); n != 1 {
		t.Errorf("%d sessions opened for 8 calls at once", n)
	}
	if calls.Load() != 8 {
		t.Errorf("the server performed %d calls", calls.Load())
	}
}

// A SESSION THE SERVER ENDED IS OPENED AGAIN, once a call: the call answers, the server saw it
// once, and several calls finding it gone at once open one session between them. A server that
// knows no session the client opens fails the call rather than opening sessions without end.
func TestASessionTheServerEndedIsOpenedAgain(t *testing.T) {
	sp := &spy{}
	var calls atomic.Int32
	srv := echo(t, &calls, nil)
	url := serve(t, srv, nil, sp.wrap)
	var log logged
	s := sessions(t, Servers{"s": {URL: url}}, nil, &log)
	ctx := context.Background()
	if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	end := func() {
		for ss := range srv.Sessions() {
			ss.Close()
		}
	}
	end()
	if got := text(t)(s.Call(ctx, "s", "echo", json.RawMessage(`{"after":1}`))); got != `{"after":1}` {
		t.Errorf("after the session ended: %q", got)
	}
	if n, c := sp.count("initialize"), calls.Load(); n != 2 || c != 2 {
		t.Errorf("%d sessions and %d calls performed, want 2 and 2", n, c)
	}
	if !strings.Contains(log.String(), "the server ended the session") {
		t.Errorf("the log: %q", log.String())
	}
	end()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Call(ctx, "s", "echo", json.RawMessage(`{}`))
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Error(err)
	}
	if n, c := sp.count("initialize"), calls.Load(); n != 3 || c != 6 {
		t.Errorf("after four calls found the session gone at once: %d sessions and %d calls, want 3 and 6", n, c)
	}

	// A server that forgets every session as soon as it is made: the call is tried on a new
	// session once, and fails.
	var made atomic.Int32
	forgetful := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Mcp-Session-Id") != "" && r.Method == http.MethodPost {
				http.Error(w, "who?", http.StatusNotFound)
				return
			}
			made.Add(1)
			h.ServeHTTP(w, r)
		})
	}
	url = serve(t, echo(t, nil, nil), nil, forgetful)
	s = sessions(t, Servers{"f": {URL: url}}, nil, io.Discard)
	if _, err := s.Call(ctx, "f", "echo", json.RawMessage(`{}`)); err == nil || !errors.Is(err, mcp.ErrSessionMissing) {
		t.Errorf("a server that knows no session: %v", err)
	}
	if made.Load() > 2 {
		t.Errorf("a server that knows no session was given %d sessions", made.Load())
	}
}

// A SESSION THAT FAILED IS OPENED AGAIN BY THE NEXT CALL. A reply that is no 2xx and carries no
// JSON-RPC body (a gateway's plain 401 while a key is rotated) fails the SDK's whole connection:
// the call that got it fails, and the next call, which the dead connection sends nothing of,
// opens a session and answers, the server seeing it once. A call in flight when the connection
// failed had its request sent, and is not sent again.
func TestASessionThatFailedIsOpenedAgain(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			sp := &spy{}
			var echoes, waits atomic.Int32
			inFlight := make(chan struct{}, 1)
			release := make(chan struct{})
			letGo := sync.OnceFunc(func() { close(release) })
			srv := echo(t, &echoes, nil)
			srv.AddTool(&mcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					waits.Add(1)
					inFlight <- struct{}{}
					<-release
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
				})
			// The server's front refuses one POST, plainly, when told to.
			var refuse atomic.Bool
			refusing := func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && refuse.Swap(false) {
						http.Error(w, "no", status)
						return
					}
					h.ServeHTTP(w, r)
				})
			}
			var log logged
			s := sessions(t, Servers{"s": {URL: serve(t, srv, nil, sp.wrap, refusing), Timeout: 5 * time.Second}}, nil, &log)
			t.Cleanup(letGo) // before the sessions and the server close, which wait for the call
			ctx := context.Background()
			if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() {
				_, err := s.Call(ctx, "s", "wait", json.RawMessage(`{}`))
				waited <- err
			}()
			<-inFlight
			refuse.Store(true)
			if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{"refused":1}`)); err == nil {
				t.Fatal("the call the server refused answered")
			}
			// The call in flight ends, answered or failed as the failed connection has it, and is
			// not sent again. Let go first: the server ends a session once its calls are done, and
			// the SDK tells it before a call on the failed connection is answered.
			letGo()
			<-waited
			for i := range 2 {
				arguments := fmt.Sprintf(`{"after":%d}`, i)
				if got := text(t)(s.Call(ctx, "s", "echo", json.RawMessage(arguments))); got != arguments {
					t.Errorf("the call after: %q", got)
				}
			}
			if n, e, w := sp.count("initialize"), echoes.Load(), waits.Load(); n != 2 || e != 3 || w != 1 {
				t.Errorf("%d sessions, %d echoes and %d waits performed, want 2, 3 and 1", n, e, w)
			}
			if !strings.Contains(log.String(), "the session is closed; opening another") {
				t.Errorf("the log: %q", log.String())
			}
		})
	}
}

// cut is a connection to a server, cut the first time the server's answer to a call goes by:
// a TCP proxy that drops the connection on both sides at the first result it sees, and forwards
// everything else. What a connection cut after the call reached the server looks like. all cuts
// every connection there is.
type cut struct {
	done  atomic.Bool
	mu    sync.Mutex
	conns map[net.Conn]net.Conn
}

func (c *cut) all() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for client, server := range c.conns {
		client.Close()
		server.Close()
	}
	c.conns = nil
}

func (c *cut) proxy(t *testing.T, target string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", strings.TrimPrefix(target, "http://"))
			if err != nil {
				client.Close()
				continue
			}
			c.mu.Lock()
			if c.conns == nil {
				c.conns = map[net.Conn]net.Conn{}
			}
			c.conns[client] = server
			c.mu.Unlock()
			go func() { io.Copy(server, client); server.Close() }()
			go func() {
				defer client.Close()
				defer server.Close()
				buf := make([]byte, 32<<10)
				for {
					n, err := server.Read(buf)
					if n > 0 {
						if i := bytes.Index(buf[:n], []byte(`"result"`)); i >= 0 && bytes.Contains(buf[:n], []byte(`"content"`)) && c.done.CompareAndSwap(false, true) {
							// What came before the event carrying the result goes through (the
							// response's headers, an event ahead of it); the rest is dropped,
							// and both sides closed.
							keep := 0
							if j := bytes.LastIndex(buf[:i], []byte("\n\n")); j >= 0 {
								keep = j + 2
							}
							if j := bytes.LastIndex(buf[:i], []byte("\r\n\r\n")); j >= 0 && j+4 > keep {
								keep = j + 4
							}
							client.Write(buf[:keep])
							return
						}
						if _, err := client.Write(buf[:n]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

// A CALL IN FLIGHT WHEN THE SERVER ENDS THE SESSION IS NOT SENT AGAIN: the server may have
// performed it, so it fails, saying so; only a call the server refused as unknown is sent on a
// new session; and the next call opens one. The server gives event ids here, so each cut
// stream is resumed, and the resumption is what the server refuses: the SDK then fails the
// whole connection as the session missing, which every call in flight hears.
func TestACallInFlightWhenTheSessionEndsIsNotSentAgain(t *testing.T) {
	var calls atomic.Int32
	inFlight := make(chan struct{}, 8)
	release := make(chan struct{})
	letGo := sync.OnceFunc(func() { close(release) })
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	srv.AddTool(&mcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			inFlight <- struct{}{}
			<-release
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
		})
	// The server forgets a session on request: every request as of one it forgot is 404.
	var forgotten sync.Map
	forgetting := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, gone := forgotten.Load(r.Header.Get("Mcp-Session-Id")); gone {
				http.Error(w, "who?", http.StatusNotFound)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	c := &cut{}
	c.done.Store(true) // no cut at a result here: the connections are cut by hand (all)
	url := c.proxy(t, serve(t, srv, &mcp.StreamableHTTPOptions{EventStore: mcp.NewMemoryEventStore(nil)}, forgetting))
	t.Cleanup(letGo)
	var log logged
	s := sessions(t, Servers{"s": {URL: url, Timeout: 5 * time.Second}}, nil, &log)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.Call(context.Background(), "s", "wait", json.RawMessage(`{}`))
			errs <- err
		}()
	}
	<-inFlight
	<-inFlight
	time.Sleep(100 * time.Millisecond) // the streams' first events reach the client
	// The server forgets the session and the connections drop: each call's stream is cut, and
	// its resumption is refused as of a session the server does not know. A call whose stream
	// had not begun when the connection dropped hears its POST fail instead; either way the
	// call fails, and is not sent again.
	for ss := range srv.Sessions() {
		forgotten.Store(ss.ID(), true)
	}
	c.all()
	for range 2 {
		if err := <-errs; err == nil {
			t.Error("a call whose session ended in flight answered")
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("the server performed %d calls, want the 2 sent", n)
	}
	if strings.Contains(log.String(), "opening another") {
		t.Errorf("a session was opened for a call in flight: %q", log.String())
	}
	// The next call opens a session of its own and answers.
	if got := text(t)(s.Call(context.Background(), "s", "echo", json.RawMessage(`{"next":1}`))); got != `{"next":1}` {
		t.Errorf("the call after: %q", got)
	}
	letGo()
}

// A CALL IS NEVER SENT TWICE. A response cut after the call reached the server is resumed where
// the server gives event ids (a protocol before 2026-07-28, with a store of events), and the
// server saw the call once; where it gives none, the call fails, and the server still saw it
// once.
func TestACutResponseIsResumedOrTheCallFailsAndIsNeverSentTwice(t *testing.T) {
	ctx := context.Background()
	// With event ids: resumed.
	var calls atomic.Int32
	c := &cut{}
	url := c.proxy(t, serve(t, echo(t, &calls, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}}),
		&mcp.StreamableHTTPOptions{EventStore: mcp.NewMemoryEventStore(nil)}))
	s := sessions(t, Servers{"s": {URL: url}}, nil, io.Discard)
	if got := text(t)(s.Call(ctx, "s", "echo", json.RawMessage(`{"once":true}`))); got != `{"once":true}` {
		t.Errorf("resumed: %q", got)
	}
	if !c.done.Load() || calls.Load() != 1 {
		t.Errorf("cut %v, the server performed %d calls", c.done.Load(), calls.Load())
	}
	// Without: the call fails, and the server performed it once.
	calls.Store(0)
	c = &cut{}
	url = c.proxy(t, serve(t, echo(t, &calls, nil), nil))
	s = sessions(t, Servers{"n": {URL: url}}, nil, io.Discard)
	if _, err := s.Call(ctx, "n", "echo", json.RawMessage(`{}`)); err == nil {
		t.Errorf("a cut response with no event ids answered")
	}
	if !c.done.Load() || calls.Load() != 1 {
		t.Errorf("cut %v, the server performed %d calls", c.done.Load(), calls.Load())
	}
}

// A REDIRECT IS NOT FOLLOWED: the credential reaches no second host, and the call fails.
func TestARedirectIsNotFollowed(t *testing.T) {
	sp := &spy{}
	second := serve(t, echo(t, nil, nil), nil, sp.wrap)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)
	st, _ := store(t, map[string]string{"k": "v"})
	s := sessions(t, Servers{"s": {URL: first.URL, Secret: "k"}}, st, io.Discard)
	if _, err := s.Call(context.Background(), "s", "echo", json.RawMessage(`{}`)); err == nil {
		t.Error("a call through a redirect answered")
	}
	if len(sp.saw) != 0 {
		t.Errorf("the second host saw %d requests", len(sp.saw))
	}
}

// A RESPONSE PAST THE BOUND FAILS THE CALL, and one within it answers. The SDK reads the bound
// as the response's end, so the log says why the call failed.
func TestAResponsePastTheBoundFailsTheCall(t *testing.T) {
	url := serve(t, echo(t, nil, nil), nil)
	var log logged
	s := sessions(t, Servers{"s": {URL: url}}, nil, &log)
	s.readMost = 4096
	ctx := context.Background()
	if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{"a":"`+strings.Repeat("x", 1000)+`"}`)); err != nil {
		t.Errorf("within the bound: %v", err)
	}
	if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{"a":"`+strings.Repeat("x", 8000)+`"}`)); err == nil {
		t.Fatal("a response over the bound answered")
	}
	if !strings.Contains(log.String(), "s: the response is over the 4096 bytes") {
		t.Errorf("the log: %q", log.String())
	}
}

// A SERVER'S REQUEST OF THE CLIENT IS REFUSED, and the call it came in goes on: the client
// declares no capability, and a server that asks anyway is told so.
func TestAServersRequestForElicitationIsRefused(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "ask", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			_, err := req.Session.Elicit(ctx, &mcp.ElicitParams{Message: "which?", RequestedSchema: map[string]any{"type": "object"}})
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint("elicit: ", err)}}}, nil
		})
	url := serve(t, srv, nil)
	s := sessions(t, Servers{"s": {URL: url}}, nil, io.Discard)
	got := text(t)(s.Call(context.Background(), "s", "ask", json.RawMessage(`{}`)))
	if !strings.HasPrefix(got, "elicit: ") || got == "elicit: <nil>" || !strings.Contains(got, "elicitation") {
		t.Errorf("the server's elicitation: %q", got)
	}
}

// THE LIST IS EVERY PAGE, AND THE INSTRUCTIONS; a server with none gives "".
func TestTheListIsEveryPageAndTheInstructions(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ServerOptions{Instructions: "call get_me first", PageSize: 2})
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		srv.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	url := serve(t, srv, nil)
	s := sessions(t, Servers{"s": {URL: url}, "quiet": {URL: serve(t, echo(t, nil, nil), nil)}}, nil, io.Discard)
	tools, instructions, err := s.List(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "a,b,c,d,e" || instructions != "call get_me first" {
		t.Errorf("listed %v, instructions %q", names, instructions)
	}
	// Each tool's schema is structure, as the server spelled it.
	if in := tools[0].Input; in == nil || ir.Get(in, "type") == nil || ir.Get(in, "type").String != "object" {
		t.Errorf("the first tool's schema: %v", tools[0].Input)
	}
	if _, instructions, err = s.List(context.Background(), "quiet"); err != nil || instructions != "" {
		t.Errorf("a server with no instructions: %q, %v", instructions, err)
	}
	if _, _, err := s.List(context.Background(), "nobody"); err == nil || !strings.Contains(err.Error(), "calls no server nobody") {
		t.Errorf("a server the runner does not call: %v", err)
	}
}

// A CALL ENDS AT THE SERVER'S TIMEOUT, with the error saying so.
func TestACallEndsAtTheServersTimeout(t *testing.T) {
	// The server's tool waits until it is let go: a server's call is not ended by the client's
	// giving up on it.
	over := make(chan struct{})
	letGo := sync.OnceFunc(func() { close(over) })
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			<-over
			return &mcp.CallToolResult{}, nil
		})
	url := serve(t, srv, nil)
	t.Cleanup(letGo) // before the server closes, which waits on its handlers
	s := sessions(t, Servers{"s": {URL: url, Timeout: 200 * time.Millisecond}}, nil, io.Discard)
	start := time.Now()
	_, err := s.Call(context.Background(), "s", "slow", json.RawMessage(`{}`))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a slow server: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the call took %s past a 200ms timeout", time.Since(start))
	}
	// Once the server is done with the call, the session ends at once. (The SDK's server
	// holds the session's end while a call runs, and the client's telling it is bounded at 5s.)
	letGo()
	start = time.Now()
	s.Close()
	if time.Since(start) > time.Second {
		t.Errorf("Close waited %s", time.Since(start))
	}
}

// CLOSE ENDS EVERY SESSION, telling the server, and a call after it fails.
func TestCloseEndsEverySession(t *testing.T) {
	sp := &spy{}
	srv := echo(t, nil, nil)
	url := serve(t, srv, nil, sp.wrap)
	s := New(Servers{"s": {URL: url}, "t": {URL: url}}, nil, io.Discard)
	ctx := context.Background()
	for _, server := range []string{"s", "t"} {
		if _, err := s.Call(ctx, server, "echo", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := 0
		for range srv.Sessions() {
			n++
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions are open at the server after Close", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.Call(ctx, "s", "echo", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("a call after Close: %v", err)
	}
	if s.Close() != nil {
		t.Error("a second Close errs")
	}
}

// A CALL'S ARGUMENTS ARE THE STRUCTURE AS JSON: what the server reads is the structure the step
// holds, a `null` a `null`, a number spelled as it was (`1.0`), text as it was; anything but an
// object is refused before any call.
func TestTheArgumentsAreTheStructureAsJSON(t *testing.T) {
	body := `{"n": 1.0, "m": 1, "s": null, "e": {}, "l": [1, "x"], "u": "héllo ☃", "b": "x\ny", "q": "a \"quote\"", "x": 1e5, "q\"k": 2}`
	n, err := codec.ParseNode([]byte(body), codec.JSON)
	if err != nil {
		t.Fatal(err)
	}
	args, err := Arguments(n)
	if err != nil {
		t.Fatal(err)
	}
	s := sessions(t, Servers{"s": {URL: serve(t, echo(t, nil, nil), nil)}}, nil, io.Discard)
	got := text(t)(s.Call(context.Background(), "s", "echo", args))
	back, err := codec.ParseNode([]byte(got), codec.JSON)
	if err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	// The same structure, less how each side spelled it (`1e5` against `100000.0`).
	if !systems.Plain(back).DeepEqual(systems.Plain(n)) {
		t.Errorf("the server read %s, want the structure of %s", got, body)
	}
	// A float written with an exponent is a float, sent as one; a key holding a quote is a
	// JSON string, which tony's own JSON writer does not make it.
	for _, piece := range []string{`"n":1.0`, `"m":1`, `"s":null`, `"u":"héllo ☃"`, `"b":"x\ny"`, `"q":"a \"quote\""`, `"x":100000.0`, `"q\"k":2`} {
		if !strings.Contains(got, piece) {
			t.Errorf("the server read %s, want %s in it", got, piece)
		}
	}
	for bad, says := range map[string]string{`[1]`: "an object", `"x"`: "an object", `null`: "an object", `7`: "an object",
		`{a: !glob x}`: "a tag (!glob)", `{1: x}`: "a tag (!sparsearray)"} { // integer keys are tony's sparse array
		n, err := codec.ParseNode([]byte(bad), codec.JSON)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Arguments(n); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v, want %q", bad, err, says)
		}
	}
}

// A RESULT IS READ AS THE TASK READS IT: the text of its content in order, content that is not
// text named in its place, structured content as JSON where there is no text, and the server's
// error mark kept. A server's answer that the call is refused (a tool it has not; a tool that
// asked the client for input, which this client does not give, so the server fails the call) is
// a ServerError, the server's words in it.
func TestAResultIsReadAsTheTaskReadsIt(t *testing.T) {
	for _, c := range []struct {
		name string
		res  *mcp.CallToolResult
		want Result
	}{
		{"text in order", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "one"}, &mcp.TextContent{Text: "two"}}}, Result{Text: "one\ntwo"}},
		{"content that is not text", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "see:"},
			&mcp.ImageContent{Data: []byte{1}, MIMEType: "image/png"}, &mcp.AudioContent{Data: []byte{1}, MIMEType: "audio/wav"},
			&mcp.ResourceLink{URI: "file:///x", Name: "x"}, &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///y", Text: "y"}}}},
			Result{Text: "see:\n[an image, image/png, left out]\n[audio, audio/wav, left out]\n[a resource link, file:///x, not followed]\ny"}},
		{"an embedded resource that is no text", &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///z", Blob: []byte{1}}}}},
			Result{Text: "[an embedded resource that is no text, left out]"}},
		{"structured content alone", &mcp.CallToolResult{StructuredContent: map[string]any{"n": 1, "s": "x"}}, Result{Text: `{"n":1,"s":"x"}`}},
		{"structured content beside text", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "said"}}, StructuredContent: map[string]any{"n": 1}}, Result{Text: "said"}},
		{"the server's error", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such issue"}}}, Result{Text: "no such issue", IsError: true}},
		{"nothing", &mcp.CallToolResult{}, Result{}},
	} {
		if got := Read(c.res); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// The server's answer that the call is refused, through the SDK's own server: a tool that
	// asks for input the client does not give, and a tool the server has not.
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "ask", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"q": &mcp.ElicitParams{Mode: "form", Message: "which team?"}}}, nil
		})
	s := sessions(t, Servers{"s": {URL: serve(t, srv, nil)}}, nil, io.Discard)
	for tool, says := range map[string]string{"ask": "does not support elicitation", "nothing": "nothing"} {
		var refused *ServerError
		_, err := s.Call(context.Background(), "s", tool, json.RawMessage(`{}`))
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, says) || !strings.Contains(err.Error(), "the server answered an error to the call") {
			t.Errorf("a call of %s: %v, want the server's refusal saying %q", tool, err, says)
		}
	}
}
