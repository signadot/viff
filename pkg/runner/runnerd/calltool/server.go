package calltool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"

	"github.com/signadot/verse/pkg/codec"
)

// Server is the task process's end: the tools on offer, and what performing a call means here.
type Server struct {
	// Tools are the tools on offer, with params as the host has them: the server describes
	// them (Describe), so the schema it checks and the one the binary reads are the same.
	Tools []Tool
	// Call performs one call of the tool named, one this server offers, with the input as the
	// caller sent it, checked against the tool's schema (Check). out is what the caller reads;
	// refused says the call was declined rather than failed. The host decides what performing
	// means — in a task process, putting the call in front of the loop as an action — and the
	// server performs nothing itself. ctx ends when the caller goes away, which for a call that
	// waits (`question-ask`) withdraws what it was waiting on.
	Call func(ctx context.Context, tool string, input map[string]any) (out string, refused bool, err error)

	mu   sync.Mutex
	path string
	ln   net.Listener
	srv  *http.Server

	// list is Tools as the list reports them, built once: the normalised schema each call is
	// checked against; byName is the same, by name. once guards them for a Handler used
	// without Start.
	once   sync.Once
	list   []Tool
	byName map[string]Tool
	err    error
}

// prepare builds the list. A tool whose schema the codec cannot carry, or two tools of one
// name, is the error, so a server is refused rather than serving a tool its check would skip
// or a call that could reach either of two.
func (s *Server) prepare() error {
	s.once.Do(func() {
		s.list = make([]Tool, 0, len(s.Tools))
		s.byName = make(map[string]Tool, len(s.Tools))
		for _, t := range s.Tools {
			d, err := Describe(t)
			if err != nil {
				s.err = err
				return
			}
			if _, dup := s.byName[t.Name]; dup {
				s.err = fmt.Errorf("calltool: two tools named %q", t.Name)
				return
			}
			s.list = append(s.list, d)
			s.byName[t.Name] = d
		}
	})
	return s.err
}

// Handler serves the two routes, for a host that has its own listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", s.tools)
	mux.HandleFunc("POST /call", s.call)
	return mux
}

// Start listens on the unix socket at path and serves until Close. A socket left at the path
// that nothing answers is cleared and the path taken; one something answers is refused, since
// taking it would leave that server reachable by nobody; anything else at the path is said.
func (s *Server) Start(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return errors.New("calltool: already started")
	}
	if err := s.prepare(); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		// Something is at the path. Find out what before taking it: between the dial that
		// finds a dead socket and the remove, another Start could bind the path and lose it —
		// a window this leaves, narrowed to the one case where the path was already taken.
		if err := clear(path); err != nil {
			return err
		}
		ln, err = net.Listen("unix", path)
	}
	if err != nil {
		return fmt.Errorf("calltool: %w", err)
	}
	s.path, s.ln = path, ln
	s.srv = &http.Server{Handler: s.Handler()}
	go s.srv.Serve(ln)
	return nil
}

// clear removes what is at path if it is a socket nothing answers: a connection refused is a
// socket whose server is gone. One that answers stays, and anything that is not a socket is
// not touched: it is somebody's file, and the error says so.
func clear(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("calltool: %s: %w", path, err)
	case fi.Mode().Type() != os.ModeSocket:
		return fmt.Errorf("calltool: %s is not a socket", path)
	}
	conn, err := net.Dial("unix", path)
	switch {
	case err == nil:
		conn.Close()
		return fmt.Errorf("calltool: %s is already answered", path)
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, os.ErrNotExist):
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("calltool: clearing %s: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("calltool: %s: %w", path, err)
}

// Path is where the server listens, "" before Start.
func (s *Server) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Close stops serving and removes the socket. A call in flight is interrupted: its context
// ends, and the caller reads that the server went away.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	os.Remove(s.path)
	s.srv, s.ln = nil, nil
	return err
}

// Bodies go out in the wire form: a Result's Out is free text — git's words, a person's
// answer — and the wire rendering has no layout for such text to trip (codec.WriteWire).
func (s *Server) tools(w http.ResponseWriter, r *http.Request) {
	if err := s.prepare(); err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	codec.WriteWire(w, r, http.StatusOK, s.list)
}

func (s *Server) call(w http.ResponseWriter, r *http.Request) {
	var c Call
	if err := codec.Read(r, &c); err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if c.Tool == "" {
		codec.HTTPError(w, r, http.StatusBadRequest, "a call names a tool")
		return
	}
	if err := s.prepare(); err != nil {
		codec.HTTPError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	desc, ok := s.byName[c.Tool]
	if !ok {
		codec.HTTPError(w, r, http.StatusNotFound,
			fmt.Sprintf("no tool %q here; the tools are: %s", c.Tool, strings.Join(Names(s.list), ", ")))
		return
	}
	if c.Input == nil {
		c.Input = map[string]any{}
	}
	// The schema is checked here as well as in the binary, so a caller that is not the binary
	// — curl, a script that speaks the route itself — is told the same thing.
	if err := Check(desc, c.Input); err != nil {
		codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if s.Call == nil {
		codec.HTTPError(w, r, http.StatusNotImplemented, "this server performs no calls")
		return
	}
	out, refused, err := s.Call(r.Context(), c.Tool, c.Input)
	if err != nil {
		msg := err.Error()
		if !strings.HasPrefix(msg, c.Tool+": ") {
			msg = c.Tool + ": " + msg
		}
		codec.HTTPError(w, r, http.StatusInternalServerError, msg)
		return
	}
	codec.WriteWire(w, r, http.StatusOK, Result{Out: out, Refused: refused})
}
