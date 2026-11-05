package versed

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/verse/internal/versed/idptest"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/entity/entitytest"
	"github.com/signadot/verse/pkg/runner"
)

// A REGISTRATIONS READ THAT DID NOT HAPPEN REFUSES. Whether a runner's name needs proof is the
// registrations' to say, so a door that could not read them answers 503 to a bare name, rather
// than admitting it as a runner nobody registered with a subject; and an add's checks that one
// subject is one principal refuse the same way.
func TestTheDoorRefusesWhenTheRegistrationsAreUnread(t *testing.T) {
	cluster := idptest.New(t)
	fs := &failReads{Store: entitytest.Store(t)}
	srv := serverWith(t, runnerdtest.Scripted, fs)
	srv.Start(t.Context())
	if err := srv.SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := srv.SetIssuer(ts.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	face := bareFace(t)
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "identity": "gpu-bot", "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()

	fs.on = true
	whoami := ts.URL + "/whoami?format=tony"
	// A runner's bare name needs no read to be refused.
	code, body := relay(t, http.MethodGet, whoami, "", "runner:gpu-bot", "", "")
	if code != http.StatusUnauthorized || !strings.Contains(body, "never taken at its word") {
		t.Fatalf("a runner's bare name, registrations unread = %d %s, want 401", code, body)
	}
	code, body = relay(t, http.MethodGet, whoami, cluster.Token(iodAudience, runnerSub, nil), "", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a runner's token, registrations unread = %d %s, want 503", code, body)
	}
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu2", "url": face.URL, "subject": runnerSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("an add whose subject checks could not look = %s: %s, want 503", resp.Status, body)
	}
}

// A REMOVAL THROUGH VERSE IS MET AT ONCE. The door keeps the registrations between reads, and
// forgets them at an add or a remove, so a runner removed is refused on its next request, not
// within the keep.
func TestARemovedRunnerIsRefusedAtOnce(t *testing.T) {
	_, ts, cluster := runnerServer(t)
	face := bareFace(t)
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	token := cluster.Token(iodAudience, runnerSub, nil)
	whoami := ts.URL + "/whoami?format=tony"
	if code, body := relay(t, http.MethodGet, whoami, token, "", "", ""); code != http.StatusOK {
		t.Fatalf("whoami before the removal = %d %s", code, body)
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/runners/gpu", nil)
	req.Header.Set(api.PrincipalHeader, tester)
	rr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, rr); rr.StatusCode != http.StatusOK {
		t.Fatalf("remove = %s: %s", rr.Status, body)
	}
	if code, body := relay(t, http.MethodGet, whoami, token, "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("whoami right after the removal = %d %s, want 401", code, body)
	}
}

// THE HELLO IS DONE WHEN THE RUNNER IS DESCRIBED. A runner whose hello comes before anything can
// reach it, the ordinary pod start, is answered 503, so its backoff tries again; the hello that
// reaches it is answered 200.
func TestAHelloNothingCouldReachIsTriedAgain(t *testing.T) {
	helloRetryWait = 10 * time.Millisecond
	_, ts, cluster := runnerServer(t)
	own := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "gpu", own))
	url := face.URL
	face.Close() // the pod is up and announcing, and its Service does not route to it yet
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": url, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	hello := ts.URL + "/runners/hello?format=tony"
	code, body := relay(t, http.MethodPost, hello, own, "", "", "")
	if code != http.StatusServiceUnavailable || tonyOf(t, body)["reach"] != "unreachable" {
		t.Fatalf("a hello nothing could reach = %d %s, want 503 unreachable", code, body)
	}
}

// runnerStaticToken is a token a runner off Kubernetes holds, long enough to be named by its hash.
func runnerStaticToken(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// A RUNNER WITH NO RUNTIME TO ISSUE IT A TOKEN PROVES ITSELF WITH A STATIC ONE. The registration
// names the token by its hash, as its subject, so verse never holds it; the runner answers it at
// the describe and presents it at the door, and is its principal. No workload issuer is needed,
// and a workload token cannot claim a static registration, nor a static token a workload one.
func TestARunnerProvesItselfWithAStaticToken(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if err := srv.SetIssuer(ts.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	tok := runnerStaticToken(t)
	sub, err := runner.StaticSubject(tok)
	if err != nil {
		t.Fatal(err)
	}
	face := guardedFace(t, ts.URL, tokenFile(t, t.TempDir(), "edge", tok))
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "edge", "url": face.URL, "identity": "edge-bot", "subject": sub})
	var row map[string]any
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add with a static token's subject, at a verse naming no workload issuer = %s: %s", resp.Status, readBody(t, resp))
	}
	decodeResp(t, resp, &row)
	if row["reach"] != "a runner" || row["described"] == nil {
		t.Fatalf("the add's row = %v, want the runner described, which proved it at the describe", row)
	}

	whoami := ts.URL + "/whoami?format=tony"
	code, body := relay(t, http.MethodGet, whoami, tok, "", "", "")
	if who := tonyOf(t, body); code != http.StatusOK || who["principal"] != "runner:edge-bot" || who["by"] != runnerProvider {
		t.Fatalf("whoami with the static token = %d %s", code, body)
	}
	if code, body := relay(t, http.MethodGet, whoami, runnerStaticToken(t), "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("another static token = %d %s, want 401", code, body)
	}
	if code, body := relay(t, http.MethodGet, whoami, "", "runner:edge-bot", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("the static runner's bare name = %d %s, want 401", code, body)
	}

	// A malformed hash is refused as written.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "edge2", "url": face.URL, "subject": "sha256:abc"})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "lowercase hex") {
		t.Fatalf("a short hash = %s: %s", resp.Status, body)
	}
}

// A workload token whose `sub` spells a static registration's subject is not that runner: each
// kind of proof finds only its own kind of registration.
func TestAWorkloadTokenCannotClaimAStaticRegistration(t *testing.T) {
	_, ts, cluster := runnerServer(t)
	sub, err := runner.StaticSubject(runnerStaticToken(t))
	if err != nil {
		t.Fatal(err)
	}
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "edge", "url": bareFace(t).URL, "subject": sub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	if code, body := relay(t, http.MethodGet, ts.URL+"/whoami?format=tony", cluster.Token(iodAudience, sub, nil), "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("a workload token naming the static subject = %d %s, want 401", code, body)
	}
}

// One token is one party: an iod's static token a runner registration already names by its hash
// is refused at start, and a registration naming an iod's static token is refused at add.
func TestAStaticTokenIsOneParty(t *testing.T) {
	srv, ts, _ := runnerServer(t)
	tok := runnerStaticToken(t)
	sub, _ := runner.StaticSubject(tok)
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "edge", "url": bareFace(t).URL, "subject": sub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	file := tokenFile(t, t.TempDir(), "iod", tok)
	err := srv.SetAuth(Auth{Iod: AuthIod{Static: []IodStatic{{File: file, Principal: "agent:edge-iod", IO: "slack"}}}})
	if err == nil || !strings.Contains(err.Error(), "registration edge") {
		t.Fatalf("an iod's static token a runner registration names = %v", err)
	}

	other := runnerStaticToken(t)
	if err := srv.SetAuth(Auth{Iod: AuthIod{Static: []IodStatic{{File: tokenFile(t, t.TempDir(), "iod2", other), Principal: "agent:edge-iod", IO: "slack"}}}}); err != nil {
		t.Fatal(err)
	}
	otherSub, _ := runner.StaticSubject(other)
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "edge3", "url": bareFace(t).URL, "subject": otherSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "auth.iod.static") {
		t.Fatalf("a registration naming an iod's static token = %s: %s", resp.Status, body)
	}
}
