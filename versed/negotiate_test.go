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

// The wire is negotiated and the default is tony. Both halves matter: a client that says
// nothing gets tony, and a client that asks for JSON still gets JSON — the flip changed the
// default, not the capability, and `VERSE_WIRE_FORMAT=json` puts it back without a rebuild.
func TestWireNegotiation(t *testing.T) {
	srv := New(ScriptedBuilder)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(accept, param string) (string, string) {
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
		return resp.Header.Get("Content-Type"), string(b)
	}

	// No preference at all — the baseline, which is tony.
	ct, body := get("", "")
	if ct != codec.MediaTony {
		t.Errorf("no Accept: Content-Type = %q, want %q", ct, codec.MediaTony)
	}
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("no Accept: body looks like JSON, want tony:\n%s", body)
	}

	// JSON is still reachable by asking — the flip changed the default, not the capability.
	ct, body = get(codec.MediaJSON, "")
	if ct != codec.MediaJSON {
		t.Errorf("Accept json: Content-Type = %q, want %q", ct, codec.MediaJSON)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("Accept json: body is not JSON:\n%s", body)
	}

	// Asking by query parameter — the lever that also works on an SSE route, where Accept
	// is spoken for by text/event-stream.
	if ct, _ = get("", "tony"); ct != codec.MediaTony {
		t.Errorf("?format=tony: Content-Type = %q, want %q", ct, codec.MediaTony)
	}
	if ct, _ = get("", "yaml"); ct != codec.MediaYAML {
		t.Errorf("?format=yaml: Content-Type = %q, want %q", ct, codec.MediaYAML)
	}

	// The parameter outranks the header — it is the more specific request.
	if ct, _ = get(codec.MediaTony, "json"); ct != codec.MediaJSON {
		t.Errorf("?format=json with Accept tony: Content-Type = %q, want json", ct)
	}

	// */* expresses no preference, so it must not be read as one.
	if ct, _ = get("*/*", ""); ct != codec.MediaTony {
		t.Errorf("Accept */*: Content-Type = %q, want the default %q", ct, codec.MediaTony)
	}

	// A malformed parameter falls back rather than failing the request: it is a rendering
	// preference, and a 400 would be a worse answer than the default rendering.
	if ct, _ = get("", "tny"); ct != codec.MediaTony {
		t.Errorf("?format=tny: Content-Type = %q, want the default", ct)
	}
}

// A request BODY is read in the format it declares, so a client may send tony to a server
// whose responses are still JSON.
func TestRequestBodyNegotiation(t *testing.T) {
	srv := New(ScriptedBuilder)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := "ref: demo:pr:1\npayload:\n  stage: open\n"
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
	if e.Payload["stage"] != "open" {
		t.Errorf("payload did not decode from tony: %+v", e.Payload)
	}
}

func mustRef(t *testing.T, s string) entity.Ref {
	t.Helper()
	r, err := entity.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
