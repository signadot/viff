package viffd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// AN INSTALL ANSWERS ONCE ACCEPTED, and `wait=<duration>` holds the answer until its rules are
// armed: with it, the answer counts the rule among the rules and names nothing arming, and a
// write right after is acted on; without it, the rule may still be arming when the answer
// comes, and the answer says so rather than counting it.
func TestAnInstallWaitsForItsRulesWhenAsked(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	install := func(query string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/charter"+query, strings.NewReader(`{
			name: review,
			condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
			action: {patch: {stage: reviewed}},
		}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/tony")
		resp, err := sendAsTester(req)
		if err != nil {
			t.Fatalf("installing: %v", err)
		}
		defer resp.Body.Close()
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing: %s: %s", resp.Status, body)
		}
		var out struct {
			Installed []map[string]any `tony:"field=installed"`
		}
		if err := codec.Unmarshal([]byte(body), &out, codec.Tony); err != nil {
			t.Fatalf("the answer: %v\n%s", err, body)
		}
		if len(out.Installed) != 1 {
			t.Fatalf("answer = %v, want one package", out.Installed)
		}
		return out.Installed[0]
	}

	got := install("?wait=10s")
	if n := count(got["rules"]); n != 1 || got["held"] != nil {
		t.Fatalf("with a wait: rules = %d, held = %v; want the rule armed and counted", n, got["held"])
	}
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, asTester(srv), ref, entity.Obj{"stage": "open"}.Payload())
	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := srv.Store.Get(ref); ok && entity.Str(e.Payload, "stage") == "reviewed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the rule the install waited for did not act on a write made right after")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// A bad wait is refused as what it is.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/charter?wait=soon", strings.NewReader(`{name: x}`))
	req.Header.Set("Content-Type", "text/tony")
	resp, err := sendAsTester(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(t, resp), "wait=") {
		t.Fatalf("wait=soon = %s; want 400 naming the parameter", resp.Status)
	}
}
