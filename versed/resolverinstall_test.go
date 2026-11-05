package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/resolver"
)

func installResolver(t *testing.T, url, doc string) *http.Response {
	t.Helper()
	resp, err := httpPost(url+"/resolvers", codec.MediaType(codec.Tony), strings.NewReader(doc))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return resp
}

const goodResolver = `{
	name:   security-review,
	match:  {about: demo.pr.*, labels: {team: security}},
	method: {driver: service, url: "https://reviewer.example/verse"},
}`

// A resolver is installed, listed and removed — the whole of what an operator can do with one
// before any question is held out to it.
func TestAResolverIsInstalledListedAndRemoved(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := installResolver(t, ts.URL, goodResolver)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s, want 201", resp.Status)
	}

	// At the address the design gives it, readable field by field like any other entity.
	e, ok, _ := srv.Store.Get(resolver.Ref("security-review"))
	if !ok {
		t.Fatal("nothing at resolvers.security.security-review")
	}
	got := entity.ToObj(e.Payload)
	if m, _ := got["match"].(map[string]any); m == nil || m["about"] != "demo.pr.*" {
		t.Fatalf("the match did not survive the install: %+v", got)
	}
	if m, _ := got["method"].(map[string]any); m == nil || m["url"] != "https://reviewer.example/verse" {
		t.Fatalf("method did not survive the install: %+v", got)
	}

	list, err := http.Get(ts.URL + "/resolvers")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer list.Body.Close()
	var out []map[string]any
	decodeResp(t, list, &out)
	if len(out) != 1 || out[0]["at"] != "verse.resolvers.security-review" {
		t.Fatalf("list = %v, want the one installed", out)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/resolvers/security-review", nil)
	req.Header.Set(api.PrincipalHeader, tester)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %s, want 204", del.Status)
	}
	if _, ok, _ := srv.Store.Get(resolver.Ref("security-review")); ok {
		t.Fatal("the resolver survived its removal")
	}
}

// A resolver that would do nothing, or less than it says, is REFUSED rather than accepted: a
// field stored and not read is a resolver that reports installed and answers nothing. The ladder's
// fields are refused saying what took their place.
func TestAResolverThatWouldDoNothingIsRefused(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct{ what, doc, wantIn string }{
		{"no match",
			`{name: r, method: {driver: service}}`,
			"needs a `match`"},
		{"a standing condition, which is retired",
			`{name: r, match: {}, condition: {subject: {path: "demo.pr.*"}}, method: {driver: service}}`,
			"retired"},
		{"a named door, which is retired",
			`{name: r, match: {}, byRef: true, method: {driver: service}}`,
			"retired"},
		{"a deadline, which is retired",
			`{name: r, match: {}, deadline: {within: "4h"}, method: {driver: service}}`,
			"retired"},
		{"an exclusivity, which is retired",
			`{name: r, match: {}, exclusive: true, method: {driver: service}}`,
			"retired"},
		{"a person driver, which is retired",
			`{name: r, match: {}, method: {driver: person, who: peggy@example.com}}`,
			"retired"},
		{"a driver that is not built",
			`{name: r, match: {}, method: {driver: llm, url: "https://x"}}`,
			"not built yet"},
		// Verse never dials a resolver's service: it dials verse. A driver named for an address
		// verse would call is told the driver is a service.
		{"a url driver",
			`{name: r, match: {}, method: {driver: url, url: "https://desk.example/verse"}}`,
			`is "service"`},
		// A resolver's address is its name. One that still files itself under a kind is told which
		// field it is, rather than installed with the field dropped in silence.
		{"a kind",
			`{name: r, kind: security, match: {}, method: {driver: service}}`,
			`has no field "kind"`},
		{"a typo the decoder would swallow",
			`{name: r, match: {}, metod: {driver: service}}`,
			`has no field "metod"`},
	} {
		resp := installResolver(t, ts.URL, c.doc)
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: install = %s, want 400", c.what, resp.Status)
			continue
		}
		if msg := string(body[:n]); !strings.Contains(msg, c.wantIn) {
			t.Errorf("%s: refused with %q, want it to mention %q", c.what, strings.TrimSpace(msg), c.wantIn)
		}
	}

	// And none of them installed anything.
	list, err := http.Get(ts.URL + "/resolvers")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer list.Body.Close()
	var out []map[string]any
	decodeResp(t, list, &out)
	if len(out) != 0 {
		t.Fatalf("a refused install left %v behind", out)
	}
}
