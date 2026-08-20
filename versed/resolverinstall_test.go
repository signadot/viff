package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/resolver"
)

func installResolver(t *testing.T, url, doc string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/resolvers", codec.MediaType(codec.Tony), strings.NewReader(doc))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return resp
}

const goodResolver = `{
	name:   security-review,
	kind:   security,
	byRef:  true,
	method: {driver: url, url: "https://reviewer.example/verse"},
}`

// A resolver is installed, listed and removed — the whole of what an operator can do with one
// before anything is bound to it.
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
	e, ok, _ := srv.Store.Get(resolver.Ref("security", "security-review"))
	if !ok {
		t.Fatal("nothing at resolvers.security.security-review")
	}
	got := entity.ToObj(e.Payload)
	if got["byRef"] != true {
		t.Fatalf("byRef did not survive the install: %+v", got)
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
	if len(out) != 1 || out[0]["ref"] != "verse.resolvers.security.security-review" {
		t.Fatalf("list = %v, want the one installed", out)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/resolvers/security/security-review", nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %s, want 204", del.Status)
	}
	if _, ok, _ := srv.Store.Get(resolver.Ref("security", "security-review")); ok {
		t.Fatal("the binding survived its removal")
	}
}

// A resolver that would do nothing is REFUSED rather than accepted.
//
// The hazard this is guarding against is already in the tree: launch.Spec.Resolver is decoded,
// bound, and dropped at launchOf, so a charter can name a resolver on a spawn today and have
// it silently do nothing. A field stored and not read is a supervisor that reports installed
// and supervises nothing — so each refusal here says which increment will read the field,
// which is news an operator can act on rather than a verdict that they were wrong.
func TestAResolverThatWouldDoNothingIsRefused(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct{ what, doc, wantIn string }{
		{"no door open",
			`{name: r, kind: security, method: {driver: url, url: "https://x"}}`,
			"needs a door"},
		{"a condition that claims everything",
			`{name: r, kind: security, condition: {subject: {path: [demo, *, *]}}, method: {driver: url, url: "https://x"}}`,
			"must name a system and a kind"},
		{"a condition that dispatches rather than covers",
			`{name: r, kind: security, condition: {subject: {path: [demo, pr, *]}, when: {path: [demo, *, *]}}, method: {driver: url, url: "https://x"}}`,
			"no firing here"},
		{"the design's older spelling for a deadline",
			`{name: r, kind: security, byRef: true, escalatesAfter: "4h", method: {driver: url, url: "https://x"}}`,
			"is not read yet"},
		{"an exclusivity with nothing to bound it",
			`{name: r, kind: security, byRef: true, exclusive: true, method: {driver: url, url: "https://x"}}`,
			"needs a deadline"},
		{"a driver that is not built",
			`{name: r, kind: security, byRef: true, method: {driver: llm, url: "https://x"}}`,
			"not built yet"},
		{"a url driver with nowhere to dial",
			`{name: r, kind: security, byRef: true, method: {driver: url}}`,
			"method.url is required"},
		{"the reserved kind",
			`{name: r, kind: instance, byRef: true, method: {driver: url, url: "https://x"}}`,
			"reserved"},
		{"a typo the decoder would swallow",
			`{name: r, kind: security, byRef: true, metod: {driver: url}}`,
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

// The two dials, and the rule the design binds them with: exclusive needs a deadline.
//
// An exclusive binding's answer is the only one that counts, so one that is down turns every
// question it owns into a question nobody may answer. A deadline is what makes that a delay
// instead of a deadlock — which is why the two arrive together rather than one at a time.
func TestExclusiveNeedsADeadline(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, c := range []struct{ what, doc, wantIn string }{
		{"exclusive with nothing to bound it",
			`{name: a, kind: security, byRef: true, exclusive: true, method: {driver: url, url: "https://x"}}`,
			"exclusive needs a deadline"},
		{"a duration that is not one",
			`{name: a, kind: security, byRef: true, deadline: {within: soon}, method: {driver: url, url: "https://x"}}`,
			"is not a duration"},
		{"an outcome this build does not have",
			`{name: a, kind: security, byRef: true, deadline: {within: "4h", then: proceed}, method: {driver: url, url: "https://x"}}`,
			"is not built"},
		{"the design's older spelling",
			`{name: a, kind: security, byRef: true, escalatesAfter: "4h", method: {driver: url, url: "https://x"}}`,
			"is not read yet"},
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

	// The two together install, and the record says the outcome even where the author left
	// it implicit — a later reader should not have to know the default to read the record.
	resp := installResolver(t, ts.URL,
		`{name: strict, kind: security, byRef: true, exclusive: true, `+
			`deadline: {within: "4h"}, method: {driver: url, url: "https://x"}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("exclusive with a deadline = %s, want 201", resp.Status)
	}
	e, _, _ := srv.Store.Get(resolver.Ref("security", "strict"))
	got := entity.ToObj(e.Payload)
	if got["exclusive"] != true {
		t.Fatalf("exclusive did not survive the install: %+v", got)
	}
	d, _ := got["deadline"].(map[string]any)
	if d == nil || d["within"] != "4h" || d["then"] != resolver.Escalate {
		t.Fatalf("the deadline did not survive as written and resolved: %+v", got)
	}

	// And a deadline alone installs: without exclusivity it is just a slow hand-off.
	resp = installResolver(t, ts.URL,
		`{name: patient, kind: security, byRef: true, deadline: {within: "30m", then: escalate}, `+
			`method: {driver: url, url: "https://x"}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a deadline without exclusive = %s, want 201", resp.Status)
	}
}
