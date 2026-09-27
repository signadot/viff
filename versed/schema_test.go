package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/schema"
)

// The route table names signatures by string, which is the one thing about it that can be
// wrong without failing to compile. Every name a route declares must resolve in the schema —
// otherwise the published API points at a description that does not exist, which is worse
// than declaring nothing, because a reader has no way to tell a typo from a missing type.
func TestEveryRouteSignatureResolves(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api")
	if err != nil {
		t.Fatalf("get /api: %v", err)
	}
	defer resp.Body.Close()
	var table api.Routes
	if err := codec.Decode(resp.Body, &table, codec.Tony); err != nil {
		t.Fatalf("decode route table: %v", err)
	}
	if len(table.Routes) == 0 {
		t.Fatal("route table is empty")
	}

	for _, rt := range table.Routes {
		for _, name := range []string{rt.Request, rt.Response} {
			if name == "" || tonyBase[name] {
				continue // no body, a response not yet named, or a base type — all legitimate
			}
			if _, ok, err := schema.Get(name); err != nil || !ok {
				t.Errorf("%s %s declares signature %q, which is not in the schema (err=%v)",
					rt.Method, rt.Path, name, err)
			}
		}
	}
}

// tonyBase is the part of tony's own vocabulary a route may name instead of a verse signature.
// `GET /flows` returns subject addresses — a list of plain strings — and `string` is the whole
// truth about them, so it is published rather than left blank. These resolve against tony-base
// (go-tony/schema/base.tony), not against the verse schema, which is why they are named here.
var tonyBase = map[string]bool{"string": true, "int": true, "float": true, "bool": true}

// A route that returns something must say HOW MANY come back, and one that returns nothing must
// not. This is the invariant that keeps the published table right about a listing — the whole
// reason Returns exists — and it is also load-bearing for the root schema, which is built by
// reading it: a new top-level GET that forgot to say would silently not appear in the root, and
// a missing key there is indistinguishable from a path that does not exist.
func TestEveryRouteSaysWhatItReturns(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api")
	if err != nil {
		t.Fatalf("get /api: %v", err)
	}
	defer resp.Body.Close()
	var table api.Routes
	if err := codec.Decode(resp.Body, &table, codec.Tony); err != nil {
		t.Fatalf("decode route table: %v", err)
	}
	ok := map[string]bool{api.ReturnsOne: true, api.ReturnsList: true, api.ReturnsStream: true}
	for _, rt := range table.Routes {
		switch {
		case rt.Response != "" && !ok[rt.Returns]:
			t.Errorf("%s %s returns %s but says returns=%q, want one | list | stream",
				rt.Method, rt.Path, rt.Response, rt.Returns)
		case rt.Response == "" && rt.Returns != "":
			t.Errorf("%s %s says returns=%q with no response to return", rt.Method, rt.Path, rt.Returns)
		}
	}
}

// The table is a byproduct of registration, so it must cover the routes that actually exist.
// A spot check on the ones every client depends on.
func TestRouteTableCoversTheKnownSurface(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api")
	if err != nil {
		t.Fatalf("get /api: %v", err)
	}
	defer resp.Body.Close()
	var table api.Routes
	if err := codec.Decode(resp.Body, &table, codec.Tony); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]api.Route{}
	for _, r := range table.Routes {
		got[r.Method+" "+r.Path] = r
	}
	for _, want := range []string{
		"GET /status", "GET /entity", "POST /entity", "POST /gate/resolve",
		"GET /tasks", "POST /tasks", "GET /schema", "GET /api",
		"GET /flows", "POST /flow/suspend", "POST /flow/resume", "POST /flow/say", "POST /flow/cancel",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("route table is missing %q", want)
		}
	}
	// The request bodies unified in the api package should be declared as such.
	if r := got["POST /gate/resolve"]; r.Request != "verse-resolve" {
		t.Errorf("POST /gate/resolve request = %q, want verse-resolve", r.Request)
	}
	if r := got["POST /entity"]; r.Request != "verse-put-entity" {
		t.Errorf("POST /entity request = %q, want verse-put-entity", r.Request)
	}
	// Two routes are called `say` and take different bodies: a word about a question, and a
	// re-prompt of a task. Each publishes its own.
	if r := got["POST /gate/say"]; r.Request != "verse-gate-say" {
		t.Errorf("POST /gate/say request = %q, want verse-gate-say", r.Request)
	}
	if r := got["POST /tasks/{id}/say"]; r.Request != "verse-say" {
		t.Errorf("POST /tasks/{id}/say request = %q, want verse-say", r.Request)
	}
	for _, route := range []string{"POST /flow/suspend", "POST /flow/resume"} {
		if r := got[route]; r.Request != "verse-flow-select" || r.Response != "verse-flow-row" {
			t.Errorf("%s = %+v, want a verse-flow-select answered by a verse-flow-row", route, r)
		}
	}
	for _, gone := range []string{"GET /suspensions", "POST /suspend", "POST /resume", "POST /flow/pause"} {
		if _, ok := got[gone]; ok {
			t.Errorf("route table still serves %q", gone)
		}
	}
}

// The endpoint serves ONE schema. Whether a definition was generated from a Go struct or
// hand-written in curated.tony is verse's bookkeeping — it used to be published as a top-level
// `curated:` list beside the shapes, which asked every reader to hold a distinction they cannot
// act on: the two kinds resolve identically, and "this one is hand-written" is not a caveat a
// caller can do anything with. It is a provenance note about our build, and it left the
// impression of two tiers of schema where there is one.
//
// So: no origin metadata on the wire, and a curated definition is indistinguishable from a
// generated one — same namespace, same shape, same place.
func TestSchemaServesOneDocumentWithoutOrigin(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doc := published(t, ts, "/schema")
	defs := ir.Get(doc, "define")
	if defs == nil || defs.Type != ir.ObjectType {
		t.Fatalf("/schema has no define")
	}
	for _, k := range doc.Fields {
		switch k.String {
		case "context", "define", "signature":
		default:
			t.Errorf("/schema publishes %q beside the schema; the response is the schema, "+
				"not a report on where it came from", k.String)
		}
	}
	if n := ir.Get(ir.Get(doc, "signature"), "name"); n == nil || n.String != schema.Name {
		t.Errorf("/schema is not named %q", schema.Name)
	}

	// verse-match is curated (trigger.Match has a hand-written codec, so codegen emits an
	// empty `define` for it) and verse-spawn is generated. Both must arrive the same way,
	// with a real shape — which is also what the curated-wins rule buys: without it
	// verse-match, the most-authored type in the system, would publish as having no shape.
	for _, n := range []string{"verse-match", "verse-spawn"} {
		d := ir.Get(defs, n)
		if d == nil {
			t.Errorf("%s is not published", n)
			continue
		}
		if d.Type != ir.ObjectType || len(d.Fields) == 0 {
			t.Errorf("%s is served without a shape", n)
		}
	}
}

// The schema is obtainable WITHOUT the server — that is the point of assembling it in package
// schema rather than deriving it from a route table. What the endpoint serves must therefore be
// what the package holds, byte for byte, or "read it from the package" is advice that leads
// somewhere else than the API does.
func TestTheServedSchemaIsThePackagesSchema(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	offline, err := schema.Global()
	if err != nil {
		t.Fatalf("schema.Global: %v", err)
	}
	want, err := codec.Marshal(offline, codec.Tony)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := codec.Marshal(published(t, ts, "/schema"), codec.Tony)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("GET /schema and schema.Global() differ:\n%s\nvs\n%s", got, want)
	}
}

// JSON cannot represent tags, and the representation policy refuses to write it rather than
// render a lie. The schema endpoint is no exception — least of all it, since signatures are
// where tags live.
func TestSchemaRefusesJSON(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/schema", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotAcceptable {
		t.Errorf("Accept: application/json = %s, want 406", resp.Status)
	}
}

// Every name a definition REFERENCES must itself be defined, and no reference may still be
// QUALIFIED. Both halves are the folding's correctness condition: thirty documents with their
// own contexts become one namespace, so `launch:verse-launch-spec` has to come out the other
// side as verse-launch-spec, resolvable beside everything else.
//
// The dangling half is not hypothetical. It was live until go-tony v0.0.93: `verse-action`
// referred to `launch:spec` while the signature it meant is named `verse-launch-spec`, so
// anything resolving the published schema followed a name that did not exist. Nothing failed —
// a schema nobody resolves cannot fail — which is exactly why it needs a test and not a reader.
func TestEveryReferenceResolvesUnqualified(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	defs := ir.Get(published(t, ts, "/schema"), "define")
	if defs == nil || defs.Type != ir.ObjectType {
		t.Fatalf("/schema has no define")
	}
	known := map[string]bool{}
	for _, f := range defs.Fields {
		known[f.String] = true
	}
	// A reference is `verse-<name>`, with the alias of whatever schema it came from if it is
	// still qualified — which, after folding, it must not be. tony-base refs are external.
	ref := regexp.MustCompile(`([a-z0-9-]+:)?(verse-[a-z0-9-]+)`)
	for i, f := range defs.Fields {
		body, err := codec.Marshal(defs.Values[i], codec.Tony)
		if err != nil {
			t.Fatalf("render %s: %v", f.String, err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(body), -1) {
			if m[1] != "" {
				t.Errorf("%s references %s%s — one schema has one namespace, so folding should "+
					"have dissolved the alias", f.String, m[1], m[2])
			}
			if !known[m[2]] {
				t.Errorf("%s references %q, which is not defined", f.String, m[2])
			}
		}
	}
}

// THE WALK. The root says what a verse looks like — `entities: .[array(verse-entity)]` — and
// the server says what it serves. Neither is derived from the other any more, which is what
// makes the schema readable without a running verse, and which is also what lets them drift.
// So they are held together here, in both directions:
//
//   - every key in the root names a bare top-level path this server answers, with the shape it
//     answers in. A root that describes a path nobody serves is a promise; a wrong type is
//     worse than none, since the whole value of publishing a shape is being right about it.
//   - every bare top-level GET that returns a NAMED document appears in the root. Otherwise a
//     new noun joins the API and the schema quietly goes on describing the old surface.
//
// A route whose response is unnamed (GET /gates, still map literals) or a stream (GET /watch)
// is exempt in the second direction, and root.tony says why for each.
func TestTheRootIsTheServedSurface(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api")
	if err != nil {
		t.Fatalf("get /api: %v", err)
	}
	defer resp.Body.Close()
	var table api.Routes
	if err := codec.Decode(resp.Body, &table, codec.Tony); err != nil {
		t.Fatalf("decode route table: %v", err)
	}

	// What the SERVER says: the shape at each bare top-level path it answers, spelled as the
	// type expression the schema would use for it.
	served := map[string]string{}
	for _, rt := range table.Routes {
		path, ok := topLevel(rt.Path)
		if !ok || rt.Method != http.MethodGet || rt.Response == "" {
			continue
		}
		switch rt.Returns {
		case api.ReturnsOne:
			served[path] = ".[" + rt.Response + "]"
		case api.ReturnsList:
			served[path] = ".[array(" + rt.Response + ")]"
		}
	}

	// What the SCHEMA says, read from the package rather than the endpoint: the walk is what
	// the API does to the schema, so the schema is the side that must not come from the API.
	root, ok, err := schema.Root()
	if err != nil || !ok {
		t.Fatalf("schema.Root: ok=%v err=%v", ok, err)
	}
	for i, f := range root.Fields {
		path, want := f.String, root.Values[i].String
		got, ok := served[path]
		if !ok {
			t.Errorf("the root describes %q, which this verse does not serve as a bare GET "+
				"returning a named document", path)
			continue
		}
		if got != want {
			t.Errorf("the root types %s as %s; GET /%s answers %s", path, want, path, got)
		}
		delete(served, path)
	}
	for path, typ := range served {
		t.Errorf("GET /%s answers %s and the root does not mention it — a new top-level path "+
			"belongs in schema/root.tony, which is what says what a verse looks like", path, typ)
	}
}

// topLevel reports the single path segment of a bare top-level route, and whether the path is
// one. "/entities" is; "/flow/tree", "/tasks/{id}" and "/" are not. It lives with the walk
// because the walk is the only thing that asks: the schema names the paths, and this is how the
// route table's spelling of one is compared with it.
func topLevel(path string) (string, bool) {
	seg, _, more := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if seg == "" || more || strings.ContainsAny(seg, "{}") {
		return "", false
	}
	return seg, true
}

// The difference between /tasks and /tasks/{id} is the difference between the root
// having a key and the route table owning it, so the predicate deciding it is pinned on its own.
func TestTopLevelIsABarePathSegment(t *testing.T) {
	for _, c := range []struct {
		path string
		want string
	}{
		{"/entities", "entities"},
		{"/schema", "schema"},
		{"/flow/tree", ""},
		{"/tasks", "tasks"},
		{"/tasks/{id}", ""},
		{"/tasks/{id}/inbox", ""},
		{"/schema/{name}", ""},
		{"/", ""},
		{"", ""},
	} {
		got, ok := topLevel(c.path)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("topLevel(%q) = %q, %v; want %q, %v", c.path, got, ok, c.want, c.want != "")
		}
	}
}

// published GETs a tony document from the server and parses it — what a reader actually
// receives, rather than what the index holds.
func published(t *testing.T, ts *httptest.Server, path string) *ir.Node {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: %s", path, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc, err := codec.ParseNode(body, codec.Tony)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

// The two steer routes answer in ONE shape carrying the verb and the selection it was asked about,
// rather than a shape per verb whose key names what happened.
func TestSteerRoutesShareOneShapeCarryingTheVerb(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.Eng.Adopt("demo.pr.1", "run", newIdleHandle(t))
	testerOwns(t, srv, mirror.Ref("run")) // as the spawn that made it would have left it

	for _, c := range []struct{ path, body, wantVerb string }{
		{"/flow/say", `{subject: "demo.pr.*", text: "hi"}`, "say"},
		{"/flow/cancel", `{task: run}`, "cancel"},
	} {
		resp, err := httpPost(ts.URL+c.path, "application/tony", strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("post %s: %v", c.path, err)
		}
		var got api.Steered
		if err := codec.Decode(resp.Body, &got, codec.Tony); err != nil {
			t.Fatalf("decode %s: %v", c.path, err)
		}
		resp.Body.Close()
		if got.Verb != c.wantVerb || got.Affected != 1 {
			t.Errorf("%s = %+v, want verb %q reaching the one run", c.path, got, c.wantVerb)
		}
		if got.Subject+got.Task == "" {
			t.Errorf("%s = %+v, want the selection it was asked about", c.path, got)
		}
	}
}

// The bodies of the runner contract are published, each under its name: a runner in another
// language is written from the schema, and a body that is in no schema is one it guesses at.
func TestSchemaPublishesTheRunnerContract(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	defs := ir.Get(published(t, ts, "/schema"), "define")
	if defs == nil {
		t.Fatal("/schema has no define")
	}
	for _, name := range []string{
		"verse-runner-spawn", "verse-runner-provenance", "verse-runner-spawned",
		"verse-runner-say", "verse-runner-cancel", "verse-runner-vector", "verse-instance",
	} {
		if ir.Get(defs, name) == nil {
			t.Errorf("/schema does not define %s", name)
		}
	}
	spawn := ir.Get(defs, "verse-runner-spawn")
	for _, field := range []string{"virtualKey", "retain", "occurrence", "provenance", "answer"} {
		if spawn != nil && ir.Get(spawn, field) == nil {
			t.Errorf("verse-runner-spawn has no %s", field)
		}
	}
}
