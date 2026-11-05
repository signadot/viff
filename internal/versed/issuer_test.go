package versed

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Verse as an issuer: a relying party that knows only the issuer's URL finds its keys and
// verifies a token verse signed.

func issuerKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// issuingServer is a server that issues as its own test address.
func issuingServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := srv.SetIssuer(ts.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	return srv, ts
}

func TestARelyingPartyVerifiesATokenVerseSigned(t *testing.T) {
	srv, ts := issuingServer(t)
	const audience = "sts.example.com"
	claims := func() map[string]any {
		now := time.Now()
		return map[string]any{
			"iss": ts.URL, "aud": audience, "sub": "a-task",
			"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		}
	}
	token, err := srv.issuer.sign(claims())
	if err != nil {
		t.Fatal(err)
	}

	// The relying party: discovery from the issuer's URL alone, naming nobody, then the keys.
	rp, err := oidc.NewProvider(t.Context(), ts.URL)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	verifier := rp.Verifier(&oidc.Config{ClientID: audience})
	got, err := verifier.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("a token verse signed did not verify: %v", err)
	}
	if got.Subject != "a-task" || got.Issuer != ts.URL {
		t.Errorf("the verified token = %+v", got)
	}

	// Signed with another key, under the same issuer and the same kid, it is refused.
	forger := &issuer{url: srv.issuer.url, key: issuerKey(t), kid: srv.issuer.kid}
	forged, err := forger.sign(claims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), forged); err == nil {
		t.Error("a token signed with a key verse does not publish verified")
	}
	// And so is one for another audience, by the relying party's own check.
	if _, err := rp.Verifier(&oidc.Config{ClientID: "somebody-else"}).Verify(t.Context(), token); err == nil {
		t.Error("a token verified for an audience it does not name")
	}
}

// The two documents are JSON whatever the request asks for, and are read by a party that names
// nobody. A verse that issues nothing has neither.
func TestTheIssuersDocumentsAreReadByNobody(t *testing.T) {
	srv, ts := issuingServer(t)
	for _, path := range []string{issuerDiscoveryPath, issuerKeysPath} {
		resp, err := http.Get(ts.URL + path + "?format=tony")
		if err != nil {
			t.Fatal(err)
		}
		body := bodyOf(t, resp)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(body, "{\"") {
			t.Errorf("%s = %s, %q: %s", path, resp.Status, resp.Header.Get("Content-Type"), body)
		}
	}
	resp, err := http.Get(ts.URL + issuerKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, `"kid":"`+srv.issuer.kid+`"`) || strings.Contains(body, `"d"`) {
		t.Errorf("the key set = %s, want the public key under its kid and nothing private", body)
	}

	issuesNothing := testServer(t)
	issuesNothing.issuer = nil
	plain := httptest.NewServer(issuesNothing.Handler())
	defer plain.Close()
	for _, path := range []string{issuerDiscoveryPath, issuerKeysPath} {
		resp, err := http.Get(plain.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s at a verse that issues nothing = %s, want 404", path, resp.Status)
		}
	}
}

// A key's id is its JWK thumbprint, checked against the example of RFC 7638 §3.1.
func TestAKeysIDIsItsThumbprint(t *testing.T) {
	const n = "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw"
	const want = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
	raw, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		t.Fatal(err)
	}
	if got := thumbprint(&rsa.PublicKey{N: new(big.Int).SetBytes(raw), E: 65537}); got != want {
		t.Errorf("thumbprint = %s, want %s", got, want)
	}
}

func TestAnIssuerIsRefusedWhatItCannotSignOrServe(t *testing.T) {
	srv := testServer(t)
	key := issuerKey(t)
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for what, c := range map[string]struct {
		url string
		key *rsa.PrivateKey
	}{
		"no key":             {"https://verse.example.com", nil},
		"a key too short":    {"https://verse.example.com", small},
		"a url with no host": {"verse", key},
		"a url with a query": {"https://verse.example.com/?x=1", key},
	} {
		if err := srv.SetIssuer(c.url, c.key, "config"); err == nil {
			t.Errorf("an issuer with %s was taken", what)
		}
	}

	// The key is read from a PEM file, PKCS#1 or PKCS#8.
	dir := t.TempDir()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, blk := range map[string]*pem.Block{
		"pkcs1.pem": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
		"pkcs8.pem": {Type: "PRIVATE KEY", Bytes: pkcs8},
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadIssuerKey(path)
		if err != nil || !got.Equal(key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIssuerKey(junk); err == nil {
		t.Error("a file that is no key was read as one")
	}
}

// The key verse up makes where its config names none is kept in its data dir, readable by its
// owner alone, and read again at the next start: a restart signs as the same issuer.
func TestTheIssuerKeyIsKeptInTheDataDir(t *testing.T) {
	dir := t.TempDir()
	first, path, err := IssuerKeyIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key at %s: %v, %v; want it there, mode 0600", path, fi, err)
	}
	again, againPath, err := IssuerKeyIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if againPath != path || !again.Equal(first) {
		t.Fatalf("a second start made another key at %s, want %s's again", againPath, path)
	}
}

// Status says this verse issues, as what, and where its key is from; and says nothing where it
// issues nothing.
func TestStatusSaysTheIssuer(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status := func() map[string]any {
		resp, err := http.Get(ts.URL + "/status?format=tony")
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		decodeResp(t, resp, &out)
		return out
	}
	iss, _ := status()["issuer"].(map[string]any)
	if iss["url"] != testIssuerURL || iss["key"] != "config" {
		t.Fatalf("status issuer = %v, want %s from config", iss, testIssuerURL)
	}
	srv.issuer = nil
	if got, ok := status()["issuer"]; ok {
		t.Fatalf("status of a verse issuing nothing says issuer %v", got)
	}
}
