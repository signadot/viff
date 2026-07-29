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
			if name == "" {
				continue // no body, or a response not yet named — both are legitimate
			}
			if _, ok, err := schema.Get(name); err != nil || !ok {
				t.Errorf("%s %s declares signature %q, which is not in the schema (err=%v)",
					rt.Method, rt.Path, name, err)
			}
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
// a generated one — same place in the map, same keys.
func TestSchemaServesSignaturesWithoutOrigin(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/schema")
	if err != nil {
		t.Fatalf("get /schema: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	doc, err := codec.ParseNode(body, codec.Tony)
	if err != nil {
		t.Fatalf("parse /schema: %v", err)
	}

	sigs := ir.Get(doc, "signatures")
	if sigs == nil || sigs.Type != ir.ObjectType {
		t.Fatalf("/schema has no signatures object")
	}
	for _, k := range doc.Fields {
		if k.String != "signatures" {
			t.Errorf("/schema publishes %q beside the signatures; the response is the schema, "+
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
func TestEverySignatureReferenceResolves(t *testing.T) {
	names, err := schema.Names()
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	// A reference is `verse-<name>`, optionally qualified by the context alias of the
	// package that defines it (`launch:verse-launch-spec`). tony-base refs are external.
	ref := regexp.MustCompile(`(?:[a-z0-9-]+:)?(verse-[a-z0-9-]+)`)
	for _, n := range names {
		doc, _, err := schema.Get(n)
		if err != nil {
			t.Fatalf("get %s: %v", n, err)
		}
		body, err := codec.Marshal(doc, codec.Tony)
		if err != nil {
			t.Fatalf("render %s: %v", n, err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(body), -1) {
			if !known[m[1]] {
				t.Errorf("signature %q references %q, which is not published", n, m[1])
			}
		}
	}
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
