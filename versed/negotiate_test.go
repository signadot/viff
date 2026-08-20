package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The wire is negotiated and the default is tony: a client that says nothing gets tony, a
// client that asks for something verse renders gets it, and a client that demands JSON is
// told no rather than handed tony under a JSON content type.
func TestWireNegotiation(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(accept, param string) (string, string, int) {
		t.Helper()
		u := ts.URL + "/status"
		if param != "" {
			u += "?" + codec.FormatParam + "=" + param
		}
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.Header.Get("Content-Type"), string(b), resp.StatusCode
	}

	// No preference at all — the baseline, which is tony.
	ct, body, _ := get("", "")
	if ct != codec.MediaTony {
		t.Errorf("no Accept: Content-Type = %q, want %q", ct, codec.MediaTony)
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("no Accept: body looks like JSON, want tony:\n%s", body)
	}

	// Demanding JSON is 406, not tony wearing a JSON label. A client that asked a specific
	// question gets an answer to that question — including the answer "no".
	ct, body, code := get(codec.MediaJSON, "")
	if code != http.StatusNotAcceptable {
		t.Errorf("Accept json: status = %d, want 406 (got Content-Type %q)", code, ct)
	}
	if !strings.Contains(body, "does not render json") {
		t.Errorf("Accept json: the refusal does not explain itself:\n%s", body)
	}
	if ct, _, code = get(codec.MediaJSON, "json"); code != http.StatusNotAcceptable {
		t.Errorf("?format=json: status = %d, want 406 (Content-Type %q)", code, ct)
	}

	// Listing JSON alongside something writable is a PREFERENCE, and preferences are
	// satisfiable: the client gets tony rather than a refusal.
	if ct, _, code = get(codec.MediaJSON+", "+codec.MediaTony+";q=0.5", ""); ct != codec.MediaTony ||
		code != http.StatusOK {
		t.Errorf("Accept json,tony: (%q, %d), want tony and 200", ct, code)
	}

	// Asking by query parameter — the lever that also works on an SSE route, where Accept
	// is spoken for by text/event-stream.
	if ct, _, _ = get("", "tony"); ct != codec.MediaTony {
		t.Errorf("?format=tony: Content-Type = %q, want %q", ct, codec.MediaTony)
	}
	if ct, _, _ = get("", "yaml"); ct != codec.MediaYAML {
		t.Errorf("?format=yaml: Content-Type = %q, want %q", ct, codec.MediaYAML)
	}

	// The parameter outranks the header — it is the more specific request.
	if ct, _, _ = get(codec.MediaTony, "yaml"); ct != codec.MediaYAML {
		t.Errorf("?format=yaml with Accept tony: Content-Type = %q, want yaml", ct)
	}

	// */* expresses no preference, so it must not be read as one.
	if ct, _, _ = get("*/*", ""); ct != codec.MediaTony {
		t.Errorf("Accept */*: Content-Type = %q, want the default %q", ct, codec.MediaTony)
	}

	// A malformed parameter falls back rather than failing the request: it is a rendering
	// preference, and a 400 would be a worse answer than the default rendering. Note the
	// contrast with ?format=json above — a typo has an obvious best guess, a format verse
	// deliberately does not produce does not.
	if ct, _, _ = get("", "tny"); ct != codec.MediaTony {
		t.Errorf("?format=tny: Content-Type = %q, want the default", ct)
	}
}

// A request BODY is read in the format it declares — the side of the wire where JSON is
// still entirely welcome.
func TestRequestBodyNegotiation(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := "at: demo.pr.1\npayload:\n  stage: open\n"
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/entity", strings.NewReader(body))
	req.Header.Set("Content-Type", codec.MediaTony)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("posting a tony body: %s", resp.Status)
	}

	e, ok, err := srv.Store.Get(mustRef(t, "demo:pr:1"))
	if err != nil || !ok {
		t.Fatalf("entity not stored: ok=%v err=%v", ok, err)
	}
	if entity.ToObj(e.Payload)["stage"] != "open" {
		t.Errorf("payload did not decode from tony: %+v", e.Payload)
	}
}

func mustRef(t *testing.T, s string) entity.Ref {
	t.Helper()
	r, err := entity.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
