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
		"GET /processes", "POST /processes", "GET /schema", "GET /api",
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
}

// The endpoint serves ONE flat set of signatures. Whether a signature was generated from a Go
// struct or hand-written in curated.tony is verse's bookkeeping — it used to be published as a
// top-level `curated:` list beside the shapes, which asked every reader to hold a distinction
// they cannot act on: the two kinds resolve identically, and "this one is hand-written" is not
// a caveat a caller can do anything with. It is a provenance note about our build, and it left
// the impression of two tiers of schema where there is one.
//
// So: no origin metadata on the wire, and a curated-origin signature is indistinguishable from
// a generated one — same place in the map, same keys. The document carries the root and the
// signatures, both of which ARE schema; nothing else belongs beside them.
func TestSchemaServesSignaturesWithoutOrigin(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doc := published(t, ts, "/schema")
	sigs := ir.Get(doc, "signatures")
	if sigs == nil || sigs.Type != ir.ObjectType {
		t.Fatalf("/schema has no signatures object")
	}
	for _, k := range doc.Fields {
		if k.String != "signatures" && k.String != "root" {
			t.Errorf("/schema publishes %q beside the schema; the response is the schema, "+
				"not a report on where it came from", k.String)
		}
	}

	// verse-match is curated (trigger.Match has a hand-written codec, so codegen emits an
	// empty `define` for it) and verse-spawn is generated. Both must arrive the same way,
	// with a real shape — which is also what the curated-wins rule buys: without it
	// verse-match, the most-authored type in the system, would publish as having no shape.
	for _, n := range []string{"verse-match", "verse-spawn"} {
		d := ir.Get(sigs, n)
		if d == nil {
			t.Errorf("%s is not published", n)
			continue
		}
		if def := ir.Get(d, "define"); def == nil || def.Type != ir.ObjectType || len(def.Fields) == 0 {
			t.Errorf("%s is served without a shape", n)
		}
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

// Every signature a published signature REFERENCES must itself be published.
//
// This is the invariant that a dangling cross-package reference violates, and one was live
// until go-tony v0.0.93: `verse-action` referred to `launch:spec` while the signature it
// meant is named `verse-launch-spec`, so anything resolving the published schema followed a
// name that did not exist. Nothing failed — a schema nobody resolves cannot fail — which is
// exactly why it needs a test rather than a reader.
//
// It reads the ENDPOINT rather than the index, and it checks the ROOT too — which is nothing
// but references, is not in the index at all (the server synthesizes it), and is the first
// thing anyone reads. A dangling name there is a dead end on the front page.
func TestEverySignatureReferenceResolves(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doc := published(t, ts, "/schema")
	sigs := ir.Get(doc, "signatures")
	if sigs == nil || sigs.Type != ir.ObjectType {
		t.Fatalf("/schema has no signatures object")
	}
	known := map[string]bool{}
	for _, f := range sigs.Fields {
		known[f.String] = true
	}
	// A reference is `verse-<name>`, optionally qualified by the context alias of the
	// package that defines it (`launch:verse-launch-spec`). tony-base refs are external.
	ref := regexp.MustCompile(`(?:[a-z0-9-]+:)?(verse-[a-z0-9-]+)`)
	check := func(name string, n *ir.Node) {
		body, err := codec.Marshal(n, codec.Tony)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(body), -1) {
			if m[1] == api.RootSignature {
				continue // the root names itself, in its own signature block
			}
			if !known[m[1]] {
				t.Errorf("%s references %q, which is not published", name, m[1])
			}
		}
	}
	for i, f := range sigs.Fields {
		check("signature "+f.String, sigs.Values[i])
	}
	check("the root", ir.Get(doc, "root"))
}

// verse-root is what a verse LOOKS LIKE: one object whose keys are the top-level paths and
// whose values are the shapes served there. It is the entry point the schema did not have — a
// flat set of named shapes answers "what is a verse-entity" and never "what is a verse" — so it
// is published FIRST, above the signatures it names, rather than filed under `v` among them.
//
// The set is pinned exactly, in both directions, because it is derived: a new top-level GET
// changes the front page of the API, and that should be a decision recorded here rather than
// something that happens to appear. Each absence below is a RULE, not an oversight — see
// rootSignature.
func TestRootSchemaIsTheTopLevelPathsAndTheirShapes(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doc := published(t, ts, "/schema")
	if doc.Fields[0].String != "root" {
		t.Errorf("/schema leads with %q; the root is what the document is read from",
			doc.Fields[0].String)
	}
	root := ir.Get(doc, "root")
	if root == nil {
		t.Fatal("/schema publishes no root")
	}
	// It resolves by name too, since it is a signature like any other and the values in it
	// point at siblings. Both must be the same document, not two renderings of one idea.
	inAll, _ := codec.Marshal(root, codec.Tony)
	byName, _ := codec.Marshal(published(t, ts, "/schema/"+api.RootSignature), codec.Tony)
	if string(byName) != string(inAll) {
		t.Errorf("GET /schema/verse-root and the root in the document disagree:\n%s\nvs\n%s",
			byName, inAll)
	}

	want := map[string]string{
		"api":       ".[verse-routes]",
		"entities":  ".[array(verse-entity)]",
		"entity":    ".[verse-entity]",
		"flows":     ".[array(string)]",
		"goals":     ".[array(verse-goal)]",
		"processes": ".[array(verse-instance)]",
		"status":    ".[verse-status]",
		"triggers":  ".[array(verse-trigger)]",
	}
	def := ir.Get(root, "define")
	if def == nil || def.Type != ir.ObjectType {
		t.Fatal("verse-root has no define")
	}
	got := map[string]string{}
	for i, f := range def.Fields {
		got[f.String] = def.Values[i].String
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("root[%s] = %q, want %q", k, got[k], v)
		}
		delete(got, k)
	}
	for k, v := range got {
		t.Errorf("root publishes %s: %s — a new top-level path belongs in this test, since it "+
			"changes what a verse is said to look like", k, v)
	}

	// The exclusions, each for its own reason:
	//   watch  — an SSE stream, not a document; no tony type says "a sequence of frames".
	//   gates  — its rows are still map literals, so there is no named shape to point at.
	//   schema — likewise: this document's own shape is not a named type.
	//   flow, gate, goal, scope — no bare GET. /flow/tree and the steers are things you do to
	//                             a verse, and GET /api is where the whole surface lives.
	for _, k := range []string{"watch", "gates", "schema", "flow", "gate", "goal", "scope"} {
		if _, ok := got[k]; ok {
			continue // already reported above
		}
		if v := ir.Get(def, k); v != nil {
			t.Errorf("root publishes %s: %s — see rootSignature for why it should not", k, v.String)
		}
	}
}

// A GET on a bare top-level path is what the root is made of, so the predicate deciding it is
// worth pinning on its own: the difference between /processes and /processes/{id} is the
// difference between the root having a key and the route table owning it.
func TestTopLevelIsABarePathSegment(t *testing.T) {
	for _, c := range []struct {
		path string
		want string
	}{
		{"/entities", "entities"},
		{"/schema", "schema"},
		{"/flow/tree", ""},
		{"/processes", "processes"},
		{"/processes/{id}", ""},
		{"/processes/{id}/inbox", ""},
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

// All eight steer routes answer in ONE shape carrying the verb, rather than four shapes whose
// key names what happened. The key used to do double duty — `paused: 2` was both the name of
// the act and the count of it — so a reader had to know which endpoint produced a document to
// know what its second key meant.
func TestSteerRoutesShareOneShapeCarryingTheVerb(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct{ path, body, wantVerb string }{
		{"/flow/pause", `{subject: "demo:pr:*"}`, "pause"},
		{"/flow/resume", `{subject: "demo:pr:*"}`, "resume"},
		{"/flow/cancel", `{subject: "demo:pr:*"}`, "cancel"},
		{"/flow/say", `{subject: "demo:pr:*", text: "hi"}`, "say"},
	} {
		resp, err := http.Post(ts.URL+c.path, "application/tony", strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("post %s: %v", c.path, err)
		}
		var got api.Steered
		if err := codec.Decode(resp.Body, &got, codec.Tony); err != nil {
			t.Fatalf("decode %s: %v", c.path, err)
		}
		resp.Body.Close()
		if got.Verb != c.wantVerb {
			t.Errorf("%s verb = %q, want %q", c.path, got.Verb, c.wantVerb)
		}
		if got.Subject != "demo:pr:*" {
			t.Errorf("%s subject = %q, want the address it was asked about", c.path, got.Subject)
		}
		if got.Goal != "" {
			t.Errorf("%s set goal on a flow steer: %q", c.path, got.Goal)
		}
	}
}
