package versed

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
)

// testIssuerURL is what a test server signs as, until a test makes it an issuer at its own
// address. Every verse issues, as `verse up` does, since it proves itself to every runner.
const testIssuerURL = "http://verse.test"

// testIssuerKey is the key every test server signs with: one per package, since making one is
// slow.
var testIssuerKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := NewIssuerKey()
	if err != nil {
		panic(err)
	}
	return key
})

// testServer is a server over a store of its own — the assembly `up` performs, minus
// the daemons it would spawn, so an HTTP-surface test gets the real store the product
// runs on without knowing how to stand one up.
func testServer(t *testing.T) *Server {
	t.Helper()
	s := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	issuing(t, s)
	// A source installed in a test runs a reflector under the server's context, which for a
	// server nobody started is Background — so without this it outlives the store it writes to
	// and goes on polling into a closed session, in whichever test runs next.
	t.Cleanup(s.stopAllIOs)
	t.Cleanup(func() { checkAskRecords(t, s.Store) })
	return s
}

// owns gives each place an owner record naming who, as the install, spawn or first write that
// made it would have left: a fixture that stands something up through the engine or the store
// directly acts as the system, which leaves no record, and a node with none is its nearest
// owner's — the root's, or verse's — so nobody the test goes on to act as would hold anything
// there. grants is what the record holds out; nil is the default above it.
func owns(t *testing.T, srv *Server, who string, grants []entity.Grant, places ...entity.Ref) {
	t.Helper()
	for _, at := range places {
		if err := entity.Establish(srv.Store, at, who, entity.Ref{}, grants); err != nil {
			t.Fatalf("recording %s as %s's: %v", at, who, err)
		}
	}
}

// testerOwns is owns for the principal an unattributed test request is made as.
func testerOwns(t *testing.T, srv *Server, places ...entity.Ref) {
	t.Helper()
	owns(t, srv, "user:"+tester, nil, places...)
}

// asTester is the store a fixture seeds the world through when the test goes on to act on it
// as the tester: what they wrote is theirs.
func asTester(srv *Server) entity.Store { return entity.As(srv.Store, "user:"+tester) }

// issuing makes s an issuer as every test server is, for a server a test assembles itself.
func issuing(t *testing.T, s *Server) *Server {
	t.Helper()
	if err := s.SetIssuer(testIssuerURL, testIssuerKey(), "config"); err != nil {
		t.Fatal(err)
	}
	return s
}

// face is a runner face that proves itself: served behind a runner's door holding a static
// token, whose hash is the Subject its registration names. Bare is the same handler with no door,
// for a test that pokes the runner as its own operator would, from beside it; verse is given URL.
type face struct {
	*httptest.Server
	Subject string
	Token   string
	Bare    string
}

// provenFace serves h as a runner of the verse at verseURL, behind the door runnerd puts in
// front of a runner (Guard): it takes the verse's tokens, read against the issuer the verse
// publishes, and answers its own static token on the describe. release ends the work on h
// before the face closes.
func provenFace(t *testing.T, verseURL string, h http.Handler, release func()) *face {
	t.Helper()
	tok := runnerStaticToken(t)
	sub, err := runner.StaticSubject(tok)
	if err != nil {
		t.Fatal(err)
	}
	g, err := runnerd.NewGuard(runnerd.Verse{URL: verseURL, Issuer: issuerOf(t, verseURL)}, tokenFile(t, t.TempDir(), "token", tok))
	if err != nil {
		t.Fatal(err)
	}
	bare := httptest.NewServer(h)
	t.Cleanup(bare.Close) // after the guarded face's cleanup, which releases the work
	return &face{Server: faceServing(t, g.Wrap(h), release), Subject: sub, Token: tok, Bare: bare.URL}
}

// issuerOf is the issuer the verse at verseURL signs as, from its discovery document.
func issuerOf(t *testing.T, verseURL string) string {
	t.Helper()
	resp, err := http.Get(verseURL + issuerDiscoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || doc.Issuer == "" {
		t.Fatalf("the discovery document at %s: %s, %v", verseURL, resp.Status, err)
	}
	return doc.Issuer
}

// reg is the registration of the runner f under name, as `verse runner add -token-file` sends it.
func (f *face) reg(name string) map[string]any {
	return map[string]any{"name": name, "url": f.URL, "subject": f.Subject}
}

// staticRunnerAs registers, unprobed, a runner acting as principal (`runner:<identity>`), proved
// by a static token, and answers the token: what a request made as that runner presents.
func staticRunnerAs(t *testing.T, verseURL, principal string) string {
	t.Helper()
	tok := runnerStaticToken(t)
	sub, err := runner.StaticSubject(tok)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(principal, "runner:")
	resp := postTony(t, verseURL+"/runners?probe=false", map[string]any{"name": id, "url": "http://" + id + ".invalid", "identity": id, "subject": sub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusCreated {
		t.Fatalf("registering %s: %s: %s", principal, resp.Status, body)
	}
	return tok
}

// sendAs is a request made as who, presenting token where there is one.
func sendAs(t *testing.T, method, url, who, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/tony")
	if who != "" {
		req.Header.Set(api.PrincipalHeader, who)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// someSubject is a static token's subject no runner holds: a registration's, for a url nothing
// answers at.
func someSubject(t *testing.T) string {
	t.Helper()
	sub, err := runner.StaticSubject(runnerStaticToken(t))
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
