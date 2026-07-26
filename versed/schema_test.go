package versed

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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
	srv := New(ScriptedBuilder)
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
	srv := New(ScriptedBuilder)
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

// A curated signature is a human's claim about the code; a generated one is derived from it.
// The endpoint reports which is which, and this pins the rule that decides: curated wins only
// where codegen produced nothing usable (an empty `define`), never over a real description.
func TestCuratedSignaturesAreReportedAndOnlyFillGaps(t *testing.T) {
	cur, err := schema.Curated()
	if err != nil {
		t.Fatalf("curated: %v", err)
	}
	isCurated := map[string]bool{}
	for _, n := range cur {
		isCurated[n] = true
	}
	// trigger.Match has a hand-written codec, so codegen emits `define: {}` for it — the
	// curated description must win, or the most-authored type in the system publishes as
	// having no shape.
	if !isCurated["verse-match"] {
		t.Error("verse-match should be curated: codegen describes it as empty")
	}
	// A type codegen describes properly must NOT be curated.
	if isCurated["verse-spawn"] {
		t.Error("verse-spawn is generated; a curated entry must not shadow it")
	}

	// And every curated name resolves, with a non-empty description.
	for _, n := range cur {
		d, ok, err := schema.Get(n)
		if err != nil || !ok {
			t.Errorf("curated %q does not resolve (err=%v)", n, err)
			continue
		}
		// An EMPTY define can be the honest answer: trigger.Formula emits a tag
		// (`!quorum 2`), not a field map, so any field list would describe a shape that
		// never appears on the wire — which is precisely what codegen produced for it. The
		// description lives in curated.tony's prose in those cases.
		if def := ir.Get(d, "define"); def != nil && def.Type != ir.ObjectType {
			t.Errorf("curated %q has a define that is not an object", n)
		}
	}
}

// JSON cannot represent tags, and the representation policy refuses to write it rather than
// render a lie. The schema endpoint is no exception — least of all it, since signatures are
// where tags live.
func TestSchemaRefusesJSON(t *testing.T) {
	srv := New(ScriptedBuilder)
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

// The upgrade that retired two curated entries: entity.Entity and Event are GENERATED now,
// because go-tony v0.0.93 consults TextMarshaler when typing a struct field, so `ref` is
// described as the string its generated code has always emitted rather than as an int.
func TestEntitySignaturesAreGeneratedNotCurated(t *testing.T) {
	cur, err := schema.Curated()
	if err != nil {
		t.Fatalf("curated: %v", err)
	}
	for _, n := range cur {
		if n == "verse-entity" || n == "verse-event" {
			t.Errorf("%s is curated again — if codegen regressed, say so here rather than "+
				"letting a hand-written signature quietly stand in for a generated one", n)
		}
	}
	for _, n := range []string{"verse-entity", "verse-event"} {
		if _, ok, _ := schema.Get(n); !ok {
			t.Errorf("%s is not published at all", n)
		}
	}
}
