package versed

import (
	"fmt"
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

// serveSchema publishes every signature the runtime's types define, as one flat set keyed by
// name, plus the synthesized verse-root. Where a signature came from — generated from a Go
// struct, or hand-written because codegen cannot describe the type — is verse's problem and
// stays out of the response.
func (s *Server) serveSchema(w http.ResponseWriter, r *http.Request) {
	doc, err := schema.All(rootSignature(s.routes))
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
	doc, ok, err := s.signature(name)
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

// signature resolves a name against everything this server publishes: the index of the
// runtime's own types, plus the root it synthesizes from its route table.
func (s *Server) signature(name string) (*ir.Node, bool, error) {
	if name == api.RootSignature {
		return rootSignature(s.routes), true, nil
	}
	return schema.Get(name)
}

// rootSignature synthesizes verse-root: what a verse looks like as ONE document, keyed by the
// top-level paths it serves.
//
// The rest of the schema is a set of named shapes with no entry point — you can look up
// verse-entity if you already know to ask for it, and there is nothing that says what a verse
// IS. This is that: `verse schema` prints an object whose keys are the top level of the API
// and whose values are the shapes served there, and every value is a reference, so the whole
// graph is reachable by following names from here.
//
// It is DERIVED from the route table, not written down, so it cannot describe a surface the
// server does not serve. The rules, in full:
//
//   - GET only. The root is what a verse LOOKS LIKE; POST /flow/pause is something you DO to
//     it. The control plane is published by GET /api, which is the complete surface.
//   - A BARE top-level path only — /entities, not /flow/tree or /processes/{id}/inbox. The
//     root is the root; one level down is the route table's business.
//   - A named Response only. An unnamed one (GET /gates, whose rows are still map literals)
//     is absent rather than typed `object` or `ir`, which would be a claim true of anything.
//     So the keys missing from here are exactly the naming work still outstanding, and each
//     one appears the moment its handler answers in a named type.
//   - No streams. GET /watch is a sequence of SSE frames, not a document, and no tony type
//     says that. Typing it `array(verse-event)` would describe a body that never arrives.
//
// The references are UNQUALIFIED (`.[verse-entity]`, not `.[entity:verse-entity]`) because
// the published schema is one namespace keyed by bare name — that is what GET /schema/{name}
// resolves against. The generated documents qualify with a context alias because tony-codegen
// works per Go package and has to say which package a type came from; the root names
// published signatures, not Go types, so it has nothing to qualify.
func rootSignature(routes []api.Route) *ir.Node {
	fields := map[string]*ir.Node{}
	for _, rt := range routes {
		if rt.Method != http.MethodGet || rt.Response == "" {
			continue
		}
		name, ok := topLevel(rt.Path)
		if !ok {
			continue
		}
		switch rt.Returns {
		case api.ReturnsOne:
			fields[name] = ir.FromString(fmt.Sprintf(".[%s]", rt.Response))
		case api.ReturnsList:
			fields[name] = ir.FromString(fmt.Sprintf(".[array(%s)]", rt.Response))
		}
	}
	return ir.FromMap(map[string]*ir.Node{
		"context":   ir.FromSlice([]*ir.Node{ir.FromString("tony-format/context")}),
		"define":    ir.FromMap(fields),
		"signature": ir.FromMap(map[string]*ir.Node{"name": ir.FromString(api.RootSignature)}),
	})
}

// topLevel reports the single path segment of a bare top-level route, and whether the path is
// one. "/entities" is; "/flow/tree", "/processes/{id}" and "/" are not.
func topLevel(path string) (string, bool) {
	seg, _, more := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if seg == "" || more || strings.ContainsAny(seg, "{}") {
		return "", false
	}
	return seg, true
}
