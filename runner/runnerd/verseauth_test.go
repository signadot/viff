package runnerd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/versed/idptest"
)

const (
	runnerSub = "system:serviceaccount:verse:gpu-runner"
	otherSub  = "system:serviceaccount:verse:other-runner"
)

// fakeVerse is a verse as a runner's door sees it: an issuer signing tokens as verseIssuer.URL,
// whose keys the runner reads at a url of its own, verseURL, which is not the issuer's name, as
// a verse called one thing outside the cluster and another inside is. The runner's own token is
// the cluster's.
func fakeVerse(t *testing.T) (cluster, verseIssuer *idptest.IdP, verseURL string) {
	t.Helper()
	cluster, verseIssuer = idptest.New(t), idptest.New(t)
	target, err := url.Parse(verseIssuer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	face := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != keySetPath {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = "/keys" // where the fake issuer serves its key set
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(face.Close)
	return cluster, verseIssuer, face.URL
}

// ownTokenFile is the runner's own token, written where the runner reads it from.
func ownTokenFile(t *testing.T, cluster *idptest.IdP, sub string) (path, token string) {
	t.Helper()
	token = cluster.Token("verse", sub, nil)
	path = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, token
}

// A runner with a `verse:` block serves that verse alone: a request is admitted when its bearer
// is a token the verse signed for this runner, its own subject as the audience. The describe is
// answered to anyone, since the description is public, and carries the runner's own token to the
// verse alone.
func TestARunnerServesItsVerseAlone(t *testing.T) {
	cluster, verse, verseURL := fakeVerse(t)
	file, ownToken := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Verse{URL: verseURL, Issuer: verse.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	// The audience is the runner's own name, read off its token; the issuer is as configured.
	if v := g.Verse(); v.Audience != runnerSub || v.Issuer != verse.URL {
		t.Fatalf("the block filled in = %+v", v)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	ts := httptest.NewServer(g.Wrap(inner))
	defer ts.Close()

	get := func(path, token string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// The description is public, and proves nothing: no bearer, no token back.
	if resp := get("/describe", ""); resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != "" {
		t.Fatalf("a bare describe = %s, %s=%q; want 200 and no token", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	if resp := get("/tasks", ""); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no bearer elsewhere = %s", resp.Status)
	}
	good := verse.Token(runnerSub, "verse", nil)
	if resp := get("/describe", good); resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != ownToken {
		t.Fatalf("the verse's token on the describe = %s, %s=%q", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	if resp := get("/tasks", good); resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != "" {
		t.Fatalf("the verse's token elsewhere = %s, %s=%q: the token is answered on the describe alone", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	// A bearer that is presented is checked, on the describe too: a verse that believes it proves
	// itself and does not is told there. A token minted for a sibling is the one that matters:
	// a copy taken at one runner opens no other's door.
	for what, bad := range map[string]string{
		"a token the verse minted for another runner": verse.Token(otherSub, "verse", nil),
		"a token for the default runner's audience":   verse.Token(VerseAudience, "verse", nil),
		"the cluster's token, not the verse's":        cluster.Token(runnerSub, "verse", nil),
		"a forged token":                              verse.Forged(runnerSub, "verse", nil),
	} {
		for _, path := range []string{"/describe", "/tasks"} {
			if resp := get(path, bad); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get(runner.TokenHeader) != "" {
				t.Errorf("%s at %s = %s, %s=%q, want 401 and no token", what, path, resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
			}
		}
	}

	// No token file: the block names the audience, the verse is served, and the describe
	// carries nothing. This is the default runner's shape.
	noFile, err := NewGuard(Verse{URL: verseURL, Issuer: verse.URL, Audience: VerseAudience}, "")
	if err != nil {
		t.Fatal(err)
	}
	bare := httptest.NewServer(noFile.Wrap(inner))
	defer bare.Close()
	req, _ := http.NewRequest(http.MethodGet, bare.URL+"/describe", nil)
	req.Header.Set("Authorization", "Bearer "+verse.Token(VerseAudience, "verse", nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != "" {
		t.Fatalf("with no token file = %s, %s=%q", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	// No token file and no audience is a door that takes nothing, refused at start.
	if _, err := NewGuard(Verse{URL: verseURL}, ""); err == nil || !strings.Contains(err.Error(), "verse.audience") {
		t.Errorf("no token and no audience: NewGuard = %v, want a refusal naming the audience", err)
	}
	// A token file that is named and holds nothing is refused where it was named.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for what, f := range map[string]string{"an empty file": empty, "a file that is not there": filepath.Join(t.TempDir(), "missing")} {
		if _, err := NewGuard(Verse{URL: verseURL}, f); err == nil || !strings.Contains(err.Error(), "workload token") {
			t.Errorf("%s as the token file: NewGuard = %v, want a refusal naming the token", what, err)
		}
	}
	// The block names where the verse is, or it names nothing.
	for what, v := range map[string]Verse{
		"no url":                   {Issuer: verse.URL},
		"a url that is no url":     {URL: "versed"},
		"an issuer that is no url": {URL: verseURL, Issuer: "oidc"},
		"an audience with space":   {URL: verseURL, Audience: "system:serviceaccount: verse:gpu"},
	} {
		if _, err := NewGuard(v, file); err == nil || !strings.Contains(err.Error(), "verse") {
			t.Errorf("%s: NewGuard = %v, want a refusal naming the block", what, err)
		}
	}
	// The issuer defaults to the url: one verse, called one thing.
	one, err := NewGuard(Verse{URL: verseURL}, file)
	if err != nil || one.Verse().Issuer != verseURL {
		t.Fatalf("Verse{URL} = %+v, %v; want the issuer defaulted to the url", one.Verse(), err)
	}
	// ... and spelt with a trailing slash it is the same issuer, as versed spells its `iss`.
	if slashed, err := NewGuard(Verse{URL: verseURL, Issuer: verseURL + "/"}, file); err != nil || slashed.Verse().Issuer != verseURL {
		t.Fatalf("Verse{Issuer: url/} = %+v, %v; want the slash dropped", slashed.Verse(), err)
	}

	// A verse whose keys cannot be read is a bearer that cannot be checked, not a bad bearer.
	down, err := NewGuard(Verse{URL: "http://127.0.0.1:1", Issuer: verse.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	dts := httptest.NewServer(down.Wrap(inner))
	defer dts.Close()
	req, _ = http.NewRequest(http.MethodGet, dts.URL+"/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+good)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a verse whose keys are unreachable = %s, want 503", resp.Status)
	}
}

// A runner announces itself to its verse in a loop, with its token, until the verse answers
// 2xx; and its readiness is the server's, heard or not.
func TestARunnerAnnouncesItselfUntilHeard(t *testing.T) {
	helloFirstWait, helloMaxWait = 5*time.Millisecond, 20*time.Millisecond
	cluster := idptest.New(t)
	file, ownToken := ownTokenFile(t, cluster, runnerSub)
	var hellos atomic.Int32
	var bearers sync.Map
	versed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/runners/hello" {
			http.Error(w, "not the hello", http.StatusNotFound)
			return
		}
		n := hellos.Add(1)
		bearers.Store(n, r.Header.Get("Authorization"))
		if n < 3 { // the registration is not there yet: the door refuses
			http.Error(w, "no registration says that subject", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("reach: a runner"))
	}))
	defer versed.Close()

	g, err := NewGuard(Verse{URL: versed.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.Announce(ctx, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
	if n := hellos.Load(); n != 3 {
		t.Fatalf("hellos = %d, want 3: two refused, one heard", n)
	}
	if b, _ := bearers.Load(int32(1)); b != "Bearer "+ownToken {
		t.Fatalf("the hello's bearer = %q, want the runner's own token", b)
	}
	// Said once per change of answer: the refusal, then being heard.
	if len(said) != 2 || !strings.Contains(said[0], "401") || !strings.Contains(said[1], "describes this runner now") {
		t.Fatalf("said = %q", said)
	}

	// Readiness is the server's: a guarded runner nobody has heard yet is ready, with no bearer
	// at the probe, and so is a runner with no door.
	fresh, err := NewGuard(Verse{URL: versed.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	for name, h := range map[string]http.Handler{"guarded": fresh.Wrap(inner), "doorless": inner} {
		ts := httptest.NewServer(WithReady(h))
		resp, err := http.Get(ts.URL + "/ready")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		ts.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("a %s runner's ready, unheard = %s, want 200", name, resp.Status)
		}
	}

	// Nothing to announce with (no token of its own) returns at once.
	quiet, err := NewGuard(Verse{URL: versed.URL, Audience: VerseAudience}, "")
	if err != nil {
		t.Fatal(err)
	}
	quiet.Announce(ctx, func(string, ...any) { t.Fatal("a runner with no token announced something") })
}

// The hello reaches the verse as the door reads its keys: through the roots `verse.ca` names, so
// a verse behind a private CA hears the runner; and a try the verse takes and never answers is a
// try that failed, so the backoff tries again.
func TestTheHelloTrustsTheVersesCA(t *testing.T) {
	helloFirstWait, helloMaxWait = 5*time.Millisecond, 20*time.Millisecond
	cluster := idptest.New(t)
	file, _ := ownTokenFile(t, cluster, runnerSub)
	verse := idptest.NewTLS(t)
	var hellos atomic.Int32
	verse.Handle("", "/runners/hello", func(w http.ResponseWriter, r *http.Request) {
		if hellos.Add(1) == 1 {
			<-r.Context().Done() // taken, never answered
			return
		}
		w.Write([]byte("reach: a runner"))
	})
	old := helloTimeout
	helloTimeout = 100 * time.Millisecond
	t.Cleanup(func() { helloTimeout = old })

	g, err := NewGuard(Verse{URL: verse.URL, CA: verse.CAFile()}, file)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.Announce(ctx, func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) })
	if n := hellos.Load(); n != 2 {
		t.Fatalf("hellos = %d, want 2: one never answered, one heard; said %q", n, said)
	}
	if last := said[len(said)-1]; !strings.Contains(last, "describes this runner now") {
		t.Fatalf("said = %q, want the verse heard over its CA", said)
	}
}

// What go-oidc says when a key fetch fails is the one thing keysUnread reads, since its verifier
// wraps the key set's error with %v: every way the fetch fails reads as keys unread, and a token
// that fails on its own does not. A go-oidc that changes its wording fails here.
func TestKeysUnreadMatchesGoOIDC(t *testing.T) {
	issuer := idptest.New(t)
	keys := func(h http.HandlerFunc) string {
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		return ts.URL
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	verify := func(keysURL, token string) error {
		ctx := context.Background()
		v := oidc.NewVerifier(issuer.URL, oidc.NewRemoteKeySet(ctx, keysURL), &oidc.Config{ClientID: "verse"})
		_, err := v.Verify(ctx, token)
		return err
	}
	good := issuer.Token("verse", runnerSub, nil)
	for what, at := range map[string]string{
		"nothing listening":     closed.URL,
		"a status not 200":      keys(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }),
		"an answer not of keys": keys(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>not keys</html>")) }),
	} {
		err := verify(at, good)
		if err == nil || !keysUnread(err) {
			t.Errorf("%s: %v, want it read as keys unread", what, err)
		}
	}
	if err := verify(issuer.URL+"/keys", issuer.Forged("verse", runnerSub, nil)); err == nil || keysUnread(err) {
		t.Errorf("a forged token: %v, want a refusal that is not keys unread", err)
	}
}
