package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
)

// heard is what a stub server was told about the request.
type heard struct {
	version, principal, authorization, contentType, accept, query, body string
	hit                                                                 bool
}

// stub answers claiming version claims ("" claims nothing), with status and body, and records what
// it heard.
func stub(t *testing.T, claims string, status int, body string) (*httptest.Server, *heard) {
	t.Helper()
	h := &heard{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*h = heard{
			version: r.Header.Get(api.VersionHeader), principal: r.Header.Get(api.PrincipalHeader),
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"),
			query: r.URL.RawQuery, body: string(b), hit: true,
		}
		if claims != "" {
			w.Header().Set(api.VersionHeader, claims)
		}
		w.Header().Set("Content-Type", codec.MediaTony)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, h
}

func addr(ts *httptest.Server) string { return strings.TrimPrefix(ts.URL, "http://") }

// Every request says which contract it speaks and who is asking. A server cannot refuse a client
// it cannot identify, and a write that arrives as nobody is one the permissions check holds
// against nobody.
func TestTransportStampsVersionAndPrincipal(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "ok: true\n")
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Version: "v1.2.0", Principal: client.Static("agent:slack-io"),
	}}}
	resp, err := c.Get("/status", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if h.version != "v1.2.0" || h.principal != "agent:slack-io" {
		t.Errorf("the server heard version %q, principal %q", h.version, h.principal)
	}
}

// A bearer proves who is asking, so the request carries it and no name: the server makes the
// request as whoever the token proves, and a name beside it could only disagree. With no bearer
// to send, the name goes as before.
func TestTransportSendsABearerInPlaceOfAName(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "ok: true\n")
	token := "tok"
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Principal: client.Static("user:ana@example.com"),
		Bearer:    func(context.Context) (string, error) { return token, nil },
	}}}
	resp, err := c.Get("/status", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if h.authorization != "Bearer tok" || h.principal != "" {
		t.Errorf("the server heard Authorization %q and principal %q, want the bearer alone", h.authorization, h.principal)
	}
	token = ""
	if resp, err = c.Get("/status", nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if h.authorization != "" || h.principal != "user:ana@example.com" {
		t.Errorf("with no bearer the server heard Authorization %q and principal %q", h.authorization, h.principal)
	}
}

// A bearer that cannot be had fails the request rather than sending it under a name alone.
func TestTransportBearerErrorFailsTheRequest(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "")
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Principal: client.Static("user:ana@example.com"),
		Bearer:    func(context.Context) (string, error) { return "", errors.New("the login has expired") },
	}}}
	if _, err := c.Get("/status", nil); err == nil {
		t.Fatal("a request whose bearer failed was sent")
	}
	if h.hit {
		t.Error("the server was reached under the name alone")
	}
}

// A principal that cannot be said fails the request rather than sending it as nobody.
func TestTransportPrincipalErrorFailsTheRequest(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "")
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Principal: func() (string, error) { return "", errors.New("config agent: not a name") },
	}}}
	if _, err := c.Get("/status", nil); err == nil {
		t.Fatal("a request whose principal failed was sent")
	}
	if h.hit {
		t.Error("the server was reached as nobody")
	}
}

// Drift is served and reported; what to do with the report is the program's.
func TestTransportDriftWarnsAndServes(t *testing.T) {
	ts, _ := stub(t, "v1.9.0", http.StatusOK, "ok: true\n")
	var warned []string
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Version: "v1.2.0", Warn: func(msg string) { warned = append(warned, msg) },
	}}}
	resp, err := c.Get("/status", nil)
	if err != nil {
		t.Fatalf("a drifting server was not served: %v", err)
	}
	resp.Body.Close()
	if len(warned) != 1 || !strings.Contains(warned[0], "v1.2.0") || !strings.Contains(warned[0], "v1.9.0") {
		t.Errorf("warned %q, want one message naming both versions", warned)
	}
}

// An incompatible server fails the request in the client too: the server's refusal stops a write,
// and this still says something true when the server is too old to look at the header.
func TestTransportIncompatibleFails(t *testing.T) {
	ts, _ := stub(t, "v2.0.0", http.StatusOK, "ok: true\n")
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{Version: "v1.2.0"}}}
	_, err := c.Get("/status", nil)
	if err == nil || !strings.Contains(err.Error(), "v2.0.0") {
		t.Fatalf("a v1 client read a v2 server: %v", err)
	}
}

// A server that states no version is not one that disagrees.
func TestTransportSilentWithoutAVersion(t *testing.T) {
	ts, _ := stub(t, "", http.StatusOK, "ok: true\n")
	warned := false
	c := &client.Client{Addr: addr(ts), HTTP: &http.Client{Transport: client.Transport{
		Version: "v1.2.0", Warn: func(string) { warned = true },
	}}}
	resp, err := c.Get("/status", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if warned {
		t.Error("an unstamped server was warned about")
	}
}

// New stamps this build's version and the principal it was given.
func TestNewStampsThisBuildAndThePrincipal(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "ok: true\n")
	resp, err := client.New(addr(ts), "agent:slack-io").Get("/status", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if h.version != api.Version() || h.principal != "agent:slack-io" {
		t.Errorf("heard version %q, principal %q", h.version, h.principal)
	}
}

// The path is escaped, so an id carrying a "#" 404s instead of silently truncating to the wrong
// route; the scope and the wire format ride every request; json, which verse does not render,
// falls back to tony.
func TestURLEscapesAndCarriesScopeAndWire(t *testing.T) {
	c := &client.Client{Addr: "verse:9797", Scope: "dev", Wire: codec.JSON}
	u, err := url.Parse(c.URL("/tasks/run-x#d1/events", url.Values{"since": {"7"}}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Fragment != "" || !strings.Contains(u.EscapedPath(), "%23") {
		t.Errorf("the # was not escaped: path %q, fragment %q", u.EscapedPath(), u.Fragment)
	}
	q := u.Query()
	if q.Get("scope") != "dev" || q.Get("since") != "7" || q.Get(codec.FormatParam) != codec.Tony.String() {
		t.Errorf("query %q, want scope dev, since 7, and the tony wire", u.RawQuery)
	}
}

// A body goes out in the wire format and asks for the Accept format, with tony as the fallback.
func TestPostRendersTheWireAndAsksForAccept(t *testing.T) {
	ts, h := stub(t, "", http.StatusOK, "ok: true\n")
	c := &client.Client{Addr: addr(ts), Accept: codec.YAML}
	resp, err := c.Post("/entity", nil, map[string]any{"at": "demo.pr.1"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if h.contentType != codec.MediaType(codec.Tony) {
		t.Errorf("Content-Type %q, want tony", h.contentType)
	}
	if !strings.HasPrefix(h.accept, codec.MediaType(codec.YAML)) || !strings.Contains(h.accept, codec.MediaTony) {
		t.Errorf("Accept %q, want yaml first and tony as the fallback", h.accept)
	}
	if !strings.Contains(h.body, "demo.pr.1") {
		t.Errorf("the body did not carry the document: %q", h.body)
	}
}

// A refusal comes back in the server's words, and its status only when it said nothing.
func TestStatusErrorCarriesTheServersWords(t *testing.T) {
	said := "stratum observe still holds 3 rules, removing it would leave them installed and unchecked"
	ts, _ := stub(t, "", http.StatusBadRequest, said)
	resp, err := (&client.Client{Addr: addr(ts)}).Get("/strata/observe", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := client.StatusError(resp, "removing a stratum"); !strings.Contains(err.Error(), said) {
		t.Errorf("the refusal lost its words: %v", err)
	}

	ts2, _ := stub(t, "", http.StatusBadRequest, "")
	resp2, err := (&client.Client{Addr: addr(ts2)}).Get("/strata/observe", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := client.OKBody(resp2); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("a silent refusal did not fall back to its status: %v", err)
	}
}
