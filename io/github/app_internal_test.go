package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An installation token is kept while it is good, and replaced shortly before it lapses, so a
// request never reaches GitHub carrying one that expired on the way.
func TestAnInstallationTokenIsReplacedBeforeItExpires(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	minted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		minted++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": fmt.Sprintf("ghs_%d", minted), "expires_at": clock.Add(time.Hour).Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)
	inst, err := newInstallation(AppAuth{AppID: "1", InstallationID: "2", PrivateKey: p}, srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	now := clock
	inst.now = func() time.Time { return now }
	ask := func(want string) {
		t.Helper()
		got, err := inst.authorization(context.Background())
		if err != nil || got != "Bearer "+want {
			t.Fatalf("at %s: %q, %v; want Bearer %s", now.Format(time.Kitchen), got, err, want)
		}
	}
	ask("ghs_1")
	now = clock.Add(54 * time.Minute)
	ask("ghs_1") // six minutes left: still good
	now = clock.Add(56 * time.Minute)
	ask("ghs_2") // four left: replaced
	if minted != 2 {
		t.Errorf("minted %d, want 2", minted)
	}
}
