package versed

import (
	"net/http"
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
