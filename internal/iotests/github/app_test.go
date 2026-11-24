package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/io/github"
)

// appKey is a fresh RSA key, PEM-encoded as GitHub hands an app's key over (PKCS#1).
func appKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// AN APP'S INSTALLATION, end to end: the source signs a JWT with the app's key, the fake verifies
// it and mints a token, and the reflection reads with that token. One token serves every request
// while it is good, so a second reflection mints nothing.
func TestTheSourceAuthenticatesAsAnAppInstallation(t *testing.T) {
	f := newFake()
	f.repo("acme/app").issue(1, "an issue", "open", "2026-09-01T00:00:00Z")
	k, p := appKey(t)
	f.app = &k.PublicKey
	src, err := github.New(context.Background(), github.Config{
		Repos: []string{"acme/app"},
		App:   &github.AppAuth{AppID: "5154941", InstallationID: "167", PrivateKey: p},
		Base:  f.serve(t),
	})
	if err != nil {
		t.Fatalf("github.New as an app: %v", err)
	}
	for range 2 {
		rn, err := src.Reflect(context.Background(), entity.Ref{})
		if err != nil {
			t.Fatalf("reflect as an app: %v", err)
		}
		if _, ok := payloads(rn)[src.ItemRef("issue", "acme/app", 1).String()]; !ok {
			t.Fatalf("the reflection as an app did not carry issue 1")
		}
	}
	if n := f.mintCount(); n != 1 {
		t.Errorf("minted %d installation tokens over two reflections, want 1", n)
	}
}

// A KEY THE APP DOES NOT HAVE is a source that does not start, as a revoked token is.
func TestAnAppWithTheWrongKeyDoesNotStart(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	k, _ := appKey(t)
	f.app = &k.PublicKey
	_, other := appKey(t)
	_, err := github.New(context.Background(), github.Config{
		Repos: []string{"acme/app"},
		App:   &github.AppAuth{AppID: "5154941", InstallationID: "167", PrivateKey: other},
		Base:  f.serve(t),
	})
	if err == nil || !strings.Contains(err.Error(), "installation 167") {
		t.Fatalf("github.New with a key the app does not have = %v, want a refused mint naming the installation", err)
	}
	if f.mintCount() != 0 {
		t.Errorf("the fake minted for a key it does not know")
	}
}

// A MALFORMED KEY is refused before anything is asked of GitHub.
func TestAnAppWithAMalformedKeyIsRefusedAtOnce(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	_, err := github.New(context.Background(), github.Config{
		Repos: []string{"acme/app"},
		App:   &github.AppAuth{AppID: "5154941", InstallationID: "167", PrivateKey: []byte("not a key")},
		Base:  f.serve(t),
	})
	if err == nil || !strings.Contains(err.Error(), "not PEM") {
		t.Fatalf("github.New with a malformed key = %v, want refused as not PEM", err)
	}
	if req, _ := f.counts(); req != 0 {
		t.Errorf("%d requests reached GitHub with a malformed key, want none", req)
	}
}

// A TOKEN GITHUB NO LONGER TAKES IS MINTED AGAIN, not believed (git-issue ne9fce9dh12ks1q4qsn0):
// the io keeps a token until shortly before the expiry GitHub gave, by its own clock, so a clock
// running slow or a revoked token is a 401 that is the io's to fix. A 401 is a refusal, and a
// refusal is recorded as permanent — so it is asked of a fresh token before it is.
func TestARefusedTokenIsMintedAgain(t *testing.T) {
	f := newFake()
	f.repo("acme/app").issue(1, "an issue", "open", "2026-09-01T00:00:00Z")
	k, p := appKey(t)
	f.app = &k.PublicKey
	src, err := github.New(context.Background(), github.Config{
		Repos: []string{"acme/app"},
		App:   &github.AppAuth{AppID: "5154941", InstallationID: "167", PrivateKey: p},
		Base:  f.serve(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.revokeTokens()
	if _, err := src.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatalf("a reflection after the token was revoked: %v", err)
	}
	if n := f.mintCount(); n != 2 {
		t.Errorf("minted %d, want 2: the revoked token replaced once", n)
	}
}

// THE APP'S TOKEN FOR ANOTHER PROGRAM: minted at once, so an app that cannot mint does not start,
// and kept while it is good.
func TestAnAppTokenIsMintedOnceAndKept(t *testing.T) {
	f := newFake()
	k, p := appKey(t)
	f.app = &k.PublicKey
	base := f.serve(t)
	tok, err := github.NewAppToken(context.Background(), github.AppAuth{AppID: "5154941", InstallationID: "167", PrivateKey: p}, base, nil)
	if err != nil {
		t.Fatalf("NewAppToken: %v", err)
	}
	a, err := tok.Token(context.Background())
	if err != nil || a == "" {
		t.Fatalf("Token = %q, %v", a, err)
	}
	b, _ := tok.Token(context.Background())
	if a != b || f.mintCount() != 1 {
		t.Errorf("two tokens %q, %q and %d mints, want one token minted once", a, b, f.mintCount())
	}
	other, _ := appKey(t)
	if _, err := github.NewAppToken(context.Background(), github.AppAuth{AppID: "5154941", InstallationID: "167",
		PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)})}, base, nil); err == nil {
		t.Error("an app whose key GitHub does not know minted a token")
	}
}
