package versed

import (
	"net/http"
	"strings"

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

// handle installs h at pattern ("GET /status") and records what it accepts and returns.
//
// req and resp are SIGNATURE NAMES (verse-put-entity, verse-flow-tree, …), resolvable
// against GET /schema. An empty resp means the response is not a named type yet — see
// api.Route on why that absence is published rather than papered over.
func (rt *router) handle(pattern string, h http.HandlerFunc, req, resp string) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok { // a pattern with no method matches every method
		method, path = "*", pattern
	}
	rt.routes = append(rt.routes, api.Route{Method: method, Path: path, Request: req, Response: resp})
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

// serveSchema publishes every signature the runtime's types generate, plus which of them are
// curated rather than generated.
func (s *Server) serveSchema(w http.ResponseWriter, r *http.Request) {
	doc, err := schema.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, doc)
}

// serveSignature publishes one signature by name. An unknown name is a 404 listing nothing —
// the caller can read GET /schema for what exists.
func (s *Server) serveSignature(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	doc, ok, err := schema.Get(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such signature: "+name, http.StatusNotFound)
		return
	}
	writeOut(w, r, http.StatusOK, doc)
}
