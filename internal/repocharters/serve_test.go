package repocharters_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/codec"
)

// tester is who the tests act as: a user viff knows, as the CLI names one.
const tester = "tester@example.com"

// postAs posts body as the principal as, as the CLI does with its config's `user:`.
func postAs(t *testing.T, as, url, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

// installIO registers one io with an out half, as installer, unprobed.
func installIO(t *testing.T, ts *httptest.Server, as string, body map[string]any) *http.Response {
	t.Helper()
	b, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	return postAs(t, as, ts.URL+"/iods?probe=false", "application/tony", string(b))
}

// installCharter installs doc at at, as the tester.
func installCharter(t *testing.T, ts *httptest.Server, at, doc string) {
	t.Helper()
	u := ts.URL + "/charter"
	if at != "" {
		u += "?at=" + at
	}
	resp := postAs(t, tester, u, "text/tony", doc)
	if resp.StatusCode >= http.StatusBadRequest {
		t.Fatalf("installing the charter: %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
}

// answerPath is where a question is answered.
func answerPath(id string) string { return "/asks/" + url.PathEscape(id) + "/answer" }

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return string(b)
}
