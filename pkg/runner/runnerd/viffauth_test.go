package runnerd

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/idptest"
	"go.viff.dev/viff/pkg/runner"
)

const (
	runnerSub = "system:serviceaccount:viff:gpu-runner"
	otherSub  = "system:serviceaccount:viff:other-runner"
)

// fakeViff is a viff instance as a runner's door sees it: an issuer signing tokens as viffIssuer.URL,
// whose keys the runner reads at a url of its own, viffURL, which is not the issuer's name, as
// a viff instance called one thing outside the cluster and another inside is. The runner's own token is
// the cluster's.
func fakeViff(t *testing.T) (cluster, viffIssuer *idptest.IdP, viffURL string) {
	t.Helper()
	cluster, viffIssuer = idptest.New(t), idptest.New(t)
	target, err := url.Parse(viffIssuer.URL)
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
	return cluster, viffIssuer, face.URL
}

// ownTokenFile is the runner's own token, written where the runner reads it from.
func ownTokenFile(t *testing.T, cluster *idptest.IdP, sub string) (path, token string) {
	t.Helper()
	token = cluster.Token("viff", sub, nil)
	path = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, token
}

// A runner with a `viff:` block serves that viff alone: a request is admitted when its bearer
// is a token the viff instance signed for this runner, its own subject as the audience. The describe is
// answered to anyone, since the description is public, and carries the runner's own token to the
// viff alone.
func TestARunnerServesItsViffAlone(t *testing.T) {
	cluster, viff, viffURL := fakeViff(t)
	file, ownToken := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Viff{URL: viffURL, Issuer: viff.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	// The audience is the runner's own name, read off its token; the issuer is as configured.
	if v := g.Viff(); v.Audience != runnerSub || v.Issuer != viff.URL {
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
	good := viff.Token(runnerSub, "viff", nil)
	if resp := get("/describe", good); resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != ownToken {
		t.Fatalf("the viff instance's token on the describe = %s, %s=%q", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	if resp := get("/tasks", good); resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != "" {
		t.Fatalf("the viff instance's token elsewhere = %s, %s=%q: the token is answered on the describe alone", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	// A bearer that is presented is checked, on the describe too: a viff instance that believes it proves
	// itself and does not is told there. A token minted for a sibling is the one that matters:
	// a copy taken at one runner opens no other's door.
	for what, bad := range map[string]string{
		"a token the viff instance minted for another runner": viff.Token(otherSub, "viff", nil),
		"a token for another audience":                        viff.Token("some-audience", "viff", nil),
		"the cluster's token, not the viff instance's":        cluster.Token(runnerSub, "viff", nil),
		"a forged token": viff.Forged(runnerSub, "viff", nil),
		// What else the viff instance's issuer signs, for an audience a charter declared: this runner's.
		"a gate credential the viff instance minted for this runner": viff.Token(runnerSub, "mirror.trigger.rule.deploy:auto",
			map[string]any{"task": "deploy@demo.pr.1", "gates": []any{map[string]any{"passed": "auto"}}}),
	} {
		for _, path := range []string{"/describe", "/tasks"} {
			if resp := get(path, bad); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get(runner.TokenHeader) != "" {
				t.Errorf("%s at %s = %s, %s=%q, want 401 and no token", what, path, resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
			}
		}
	}

	// No token file: the block names the audience, the viff instance is served, and the describe
	// carries nothing.
	noFile, err := NewGuard(Viff{URL: viffURL, Issuer: viff.URL, Audience: "some-audience"}, "")
	if err != nil {
		t.Fatal(err)
	}
	bare := httptest.NewServer(noFile.Wrap(inner))
	defer bare.Close()
	req, _ := http.NewRequest(http.MethodGet, bare.URL+"/describe", nil)
	req.Header.Set("Authorization", "Bearer "+viff.Token("some-audience", "viff", nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get(runner.TokenHeader) != "" {
		t.Fatalf("with no token file = %s, %s=%q", resp.Status, runner.TokenHeader, resp.Header.Get(runner.TokenHeader))
	}
	// No token file and no audience is a door that takes nothing, refused at start.
	if _, err := NewGuard(Viff{URL: viffURL}, ""); err == nil || !strings.Contains(err.Error(), "viff.audience") {
		t.Errorf("no token and no audience: NewGuard = %v, want a refusal naming the audience", err)
	}
	// A token file that is named and holds nothing is refused where it was named.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for what, f := range map[string]string{"an empty file": empty, "a file that is not there": filepath.Join(t.TempDir(), "missing")} {
		if _, err := NewGuard(Viff{URL: viffURL}, f); err == nil || !strings.Contains(err.Error(), "workload token") {
			t.Errorf("%s as the token file: NewGuard = %v, want a refusal naming the token", what, err)
		}
	}
	// The block names where the viff instance is, or it names nothing.
	for what, v := range map[string]Viff{
		"no url":                   {Issuer: viff.URL},
		"a url that is no url":     {URL: "viffd"},
		"an issuer that is no url": {URL: viffURL, Issuer: "oidc"},
		"an audience with space":   {URL: viffURL, Audience: "system:serviceaccount: viff:gpu"},
	} {
		if _, err := NewGuard(v, file); err == nil || !strings.Contains(err.Error(), "viff") {
			t.Errorf("%s: NewGuard = %v, want a refusal naming the block", what, err)
		}
	}
	// The issuer defaults to the url: one viff instance, called one thing.
	one, err := NewGuard(Viff{URL: viffURL}, file)
	if err != nil || one.Viff().Issuer != viffURL {
		t.Fatalf("Viff{URL} = %+v, %v; want the issuer defaulted to the url", one.Viff(), err)
	}
	// ... and spelt with a trailing slash it is the same issuer, as viffd spells its `iss`.
	if slashed, err := NewGuard(Viff{URL: viffURL, Issuer: viffURL + "/"}, file); err != nil || slashed.Viff().Issuer != viffURL {
		t.Fatalf("Viff{Issuer: url/} = %+v, %v; want the slash dropped", slashed.Viff(), err)
	}

	// A viff instance whose keys cannot be read is a bearer that cannot be checked, not a bad bearer.
	down, err := NewGuard(Viff{URL: "http://127.0.0.1:1", Issuer: viff.URL}, file)
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
		t.Fatalf("a viff instance whose keys are unreachable = %s, want 503", resp.Status)
	}
}

// A runner announces itself to its viff instance in a loop, with its token, until the viff instance answers
// 2xx; and its readiness is the server's, heard or not.
func TestARunnerAnnouncesItselfUntilHeard(t *testing.T) {
	helloFirstWait, helloMaxWait = 5*time.Millisecond, 20*time.Millisecond
	cluster := idptest.New(t)
	file, ownToken := ownTokenFile(t, cluster, runnerSub)
	var hellos atomic.Int32
	var bearers sync.Map
	viffd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer viffd.Close()

	g, err := NewGuard(Viff{URL: viffd.URL}, file)
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
	fresh, err := NewGuard(Viff{URL: viffd.URL}, file)
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
	quiet, err := NewGuard(Viff{URL: viffd.URL, Audience: "some-audience"}, "")
	if err != nil {
		t.Fatal(err)
	}
	quiet.Announce(ctx, func(string, ...any) { t.Fatal("a runner with no token announced something") })
}

// The hello reaches the viff instance as the door reads its keys: through the roots `viff.ca` names, so
// a viff instance behind a private CA hears the runner; and a try the viff instance takes and never answers is a
// try that failed, so the backoff tries again.
func TestTheHelloTrustsTheViffsCA(t *testing.T) {
	helloFirstWait, helloMaxWait = 5*time.Millisecond, 20*time.Millisecond
	cluster := idptest.New(t)
	file, _ := ownTokenFile(t, cluster, runnerSub)
	viff := idptest.NewTLS(t)
	var hellos atomic.Int32
	viff.Handle("", "/runners/hello", func(w http.ResponseWriter, r *http.Request) {
		if hellos.Add(1) == 1 {
			<-r.Context().Done() // taken, never answered
			return
		}
		w.Write([]byte("reach: a runner"))
	})
	old := helloTimeout
	helloTimeout = 100 * time.Millisecond
	t.Cleanup(func() { helloTimeout = old })

	g, err := NewGuard(Viff{URL: viff.URL, CA: viff.CAFile()}, file)
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
		t.Fatalf("said = %q, want the viff instance heard over its CA", said)
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
		v := oidc.NewVerifier(issuer.URL, oidc.NewRemoteKeySet(ctx, keysURL), &oidc.Config{ClientID: "viff"})
		_, err := v.Verify(ctx, token)
		return err
	}
	good := issuer.Token("viff", runnerSub, nil)
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
	if err := verify(issuer.URL+"/keys", issuer.Forged("viff", runnerSub, nil)); err == nil || keysUnread(err) {
		t.Errorf("a forged token: %v, want a refusal that is not keys unread", err)
	}
}

// A RAISE VIFF REFUSED THE RUNNER ON IS THE RUNNER'S FAULT, and an error to the task: viff's
// door says so with WWW-Authenticate. A refusal of the ask itself is viff's answer, a no.
func TestARaiseRefusedAtTheDoorIsTheRunnersFault(t *testing.T) {
	cluster, _, _ := fakeViff(t)
	var door atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if door.Load() {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope"`)
		}
		http.Error(w, "not this", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	path, _ := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Viff{URL: srv.URL}, path)
	if err != nil {
		t.Fatal(err)
	}
	if code, said, err := g.Carry(context.Background(), "t1", http.MethodPost, "/asks", []byte("{question: x}")); err != nil || code != http.StatusForbidden || said != "not this" {
		t.Errorf("a refusal of the ask = %d %q %v, want viff's 403", code, said, err)
	}
	door.Store(true)
	if code, _, err := g.Carry(context.Background(), "t1", http.MethodPost, "/asks", []byte("{question: x}")); err == nil || !strings.Contains(err.Error(), "not this") {
		t.Errorf("a refusal at the door = %d, %v, want an error saying why", code, err)
	}
}

// A READ IS CARRIED AS A READ: the runner's own token and the task's id, the answer asked for in
// tony on the query beside what the path already asks, and no body or Content-Type where there
// is nothing to send.
func TestARunnerCarriesATasksReadToViff(t *testing.T) {
	cluster, _, _ := fakeViff(t)
	type seen struct {
		method, uri, task, ctype, body string
		bearer                         bool
	}
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = seen{r.Method, r.URL.RequestURI(), r.Header.Get(api.TaskHeader), r.Header.Get("Content-Type"), string(b),
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")}
		fmt.Fprint(w, "- {id: a-1}\n")
	}))
	t.Cleanup(srv.Close)
	path, _ := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Viff{URL: srv.URL}, path)
	if err != nil {
		t.Fatal(err)
	}
	code, said, err := g.Carry(context.Background(), "t1", http.MethodGet, "/asks", nil)
	if err != nil || code != http.StatusOK || said != "- {id: a-1}" {
		t.Fatalf("a read = %d %q %v", code, said, err)
	}
	if want := (seen{http.MethodGet, "/asks?format=tony", "t1", "", "", true}); got != want {
		t.Errorf("the read was carried as %+v, want %+v", got, want)
	}
	if _, _, err := g.Carry(context.Background(), "t1", http.MethodGet, "/asks?waiting=me", nil); err != nil || got.uri != "/asks?waiting=me&format=tony" {
		t.Errorf("a read with a query was carried to %q (%v)", got.uri, err)
	}
	if _, _, err := g.Carry(context.Background(), "t1", http.MethodPost, "/asks", []byte("{question: x}")); err != nil || got.ctype != "application/tony" || got.body != "{question: x}" {
		t.Errorf("a raise was carried as %+v (%v)", got, err)
	}
}

// AN ANSWER OVER WHAT THE RUNNER CARRIES IS AN ERROR NAMING THE CAP, not a document cut at the cap:
// a cut document reads as nothing, or as less than viff said.
func TestARunnerCarriesNoAnswerOverItsCap(t *testing.T) {
	cluster, _, _ := fakeViff(t)
	var size int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("- {id: "))
		w.Write(bytes.Repeat([]byte("a"), size))
		w.Write([]byte("}\n"))
	}))
	t.Cleanup(srv.Close)
	path, _ := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Viff{URL: srv.URL}, path)
	if err != nil {
		t.Fatal(err)
	}
	size = carryCap - len("- {id: }\n")
	if code, said, err := g.Carry(context.Background(), "t1", http.MethodGet, "/asks", nil); err != nil || code != http.StatusOK || len(said) != carryCap-1 {
		t.Fatalf("an answer at the cap = %d, %d bytes, %v", code, len(said), err)
	}
	size++
	if _, said, err := g.Carry(context.Background(), "t1", http.MethodGet, "/asks", nil); err == nil || !strings.Contains(err.Error(), "over 4194304 bytes") || said != "" {
		t.Errorf("an answer over the cap = %q, %v; want an error naming the cap", said, err)
	}
}

// A RUNNER'S VIFF MAY STOP IT, and nobody else: POST /stop behind the door answers 202 and stops
// the runner; with no bearer, or another's, the stop is refused and the runner goes on.
func TestARunnersViffMayStopIt(t *testing.T) {
	cluster, viff, viffURL := fakeViff(t)
	file, _ := ownTokenFile(t, cluster, runnerSub)
	g, err := NewGuard(Viff{URL: viffURL, Issuer: viff.URL}, file)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{}, 1)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	ts := httptest.NewServer(g.Wrap(WithStop(inner, func() { stopped <- struct{}{} })))
	defer ts.Close()
	stop := func(method, token string) int {
		req, _ := http.NewRequest(method, ts.URL+StopPath, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for what, token := range map[string]string{"no bearer": "", "another runner's token": viff.Token(otherSub, "viff", nil)} {
		if code := stop(http.MethodPost, token); code != http.StatusUnauthorized {
			t.Errorf("a stop with %s = %d, want 401", what, code)
		}
	}
	good := viff.Token(runnerSub, "viff", nil)
	if code := stop(http.MethodGet, good); code != http.StatusMethodNotAllowed {
		t.Errorf("GET /stop = %d, want 405", code)
	}
	select {
	case <-stopped:
		t.Fatal("a refused stop stopped the runner")
	default:
	}
	if code := stop(http.MethodPost, good); code != http.StatusAccepted {
		t.Fatalf("the viff instance's stop = %d, want 202", code)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the viff instance's stop did not stop the runner")
	}
}
