package versed

import (
	"net/http"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/schema"
)

// router registers a route AND records it, in one call, so the published table at GET /api
// is a byproduct of routing rather than a second list maintained beside it. A parallel table
// rots the first time someone adds a handler and forgets; this one cannot, because there is
// no way to register without recording.
type router struct {
	mux    *http.ServeMux
	routes []api.Route
	// unattributed is the patterns a request may WRITE through without saying who makes it
	// (handleUnattributed). Everything else that is not a read is refused at the door.
	unattributed map[string]bool
	// control is the patterns a trusted io may act through for a person it relays
	// (handleControl): answering a question, saying on it, steering a task or a flow. Said at
	// registration, so a route renamed or added carries its own answer to "may an io do this for
	// somebody", rather than a list elsewhere having to be remembered.
	control map[string]bool
}

// returns is what a route answers with: the signature of ONE document, and how many of them
// come back. A registration has to SAY which — see one/list/stream — because the name alone
// cannot, and a table claiming `verse-trigger` for a body that is an array of them is wrong in
// the artifact whose whole job is being right about the wire.
type returns struct {
	sig     string
	returns string
}

// one, list and stream are the three ways a body can carry a signature; nothing is the fourth,
// for a route that answers with no document worth naming. They are constructors rather than a
// bare string argument so that adding a route makes the question unavoidable: there is no way
// to declare a response without choosing one.
func one(sig string) returns    { return returns{sig, api.ReturnsOne} }
func list(sig string) returns   { return returns{sig, api.ReturnsList} }
func stream(sig string) returns { return returns{sig, api.ReturnsStream} }

// nothing is the empty response: no body, or a shape that is not a named type yet. See
// api.Route on why that absence is published rather than papered over.
var nothing = returns{}

// handle installs h at pattern ("GET /status") and records what it accepts and returns.
//
// req is a SIGNATURE NAME (verse-put-entity, verse-resolve, …) and so is the name inside ret;
// both resolve against GET /schema.
func (rt *router) handle(pattern string, h http.HandlerFunc, req string, ret returns) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok { // a pattern with no method matches every method
		method, path = "*", pattern
	}
	rt.routes = append(rt.routes, api.Route{
		Method: method, Path: path, Request: req, Response: ret.sig, Returns: ret.returns,
	})
	rt.mux.HandleFunc(pattern, h)
}

// handleUnattributed is handle for a route whose caller is no principal and acts as none: what
// it does is done as a party the ROUTE names — the source a hint is for — and never as
// whoever sent it. It is said here, per route, because the default is
// the other way: a request that is not a read names who makes it (Server.principals), and a
// route added without thinking about it is one that refuses.
func (rt *router) handleUnattributed(pattern string, h http.HandlerFunc, req string, ret returns) {
	if rt.unattributed == nil {
		rt.unattributed = map[string]bool{}
	}
	rt.unattributed[pattern] = true
	rt.handle(pattern, h, req, ret)
}

// handleControl is handle for a route that is a control act: what a person does to a question or
// a run, which is all an io relaying a person does for them by default (iod.go).
func (rt *router) handleControl(pattern string, h http.HandlerFunc, req string, ret returns) {
	if rt.control == nil {
		rt.control = map[string]bool{}
	}
	rt.control[pattern] = true
	rt.handle(pattern, h, req, ret)
}

// mount installs a sub-handler at a prefix. It records nothing: the routes underneath belong
// to the mounted runtime and are published by whatever describes THAT surface, not by
// claiming them here.
func (rt *router) mount(pattern string, h http.Handler) { rt.mux.Handle(pattern, h) }

// serveAPI publishes the route table — the API describing its own shape, with every entry
// pointing at signatures in GET /schema.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	writeOut(w, r, http.StatusOK, api.Routes{Routes: s.routes})
}

// serveSchema publishes the schema, whole: the root — what a verse looks like, by top-level
// path — and every definition it names. The server does not build it. It is assembled from the
// generated and curated parts in package schema, with no route table involved, so the same
// document is obtainable without a running verse; this route is one way to ask for it, not
// where it lives.
func (s *Server) serveSchema(w http.ResponseWriter, r *http.Request) {
	doc, err := schema.Global()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, doc)
}

// serveDefinition publishes one definition by name, under that name — a fragment of the schema
// rather than a document of its own, since its references resolve against the rest of it. An
// unknown name is a 404 listing nothing; the caller can read GET /schema for what exists.
func (s *Server) serveDefinition(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	def, ok, err := schema.Get(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such definition: "+name, http.StatusNotFound)
		return
	}
	writeOut(w, r, http.StatusOK, ir.FromMap(map[string]*ir.Node{name: def}))
}

// unattributedRoutes is the patterns registered as writable by a request that names nobody.
func (s *Server) unattributedRoutes() []string {
	out := make([]string, 0, len(s.unattributed))
	for p := range s.unattributed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
