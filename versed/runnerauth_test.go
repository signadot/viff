package versed

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/runner/runnerd"
	"github.com/signadot/verse/versed/idptest"
)

// Who a runner is, decided at registration (runnerauth.go): a registration names the subject of
// the runner's workload token, and verse holds the runner to it.

const runnerSub = "system:serviceaccount:verse:gpu-runner"

// runnerServer is a server whose deployment names a workload issuer (the fake cluster), with no
// iod accounts, and which issues tokens of its own as its address: a verse that can verify a
// runner's token, prove itself to a runner, and has nothing else to verify.
func runnerServer(t *testing.T) (*Server, *httptest.Server, *idptest.IdP) {
	t.Helper()
	srv, ts, cluster := wordOnlyServer(t)
	if err := srv.SetIssuer(ts.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	return srv, ts, cluster
}

// wordOnlyServer is runnerServer without an issuer of its own: a verse that could verify a
// runner's token and could never prove itself to one.
func wordOnlyServer(t *testing.T) (*Server, *httptest.Server, *idptest.IdP) {
	t.Helper()
	cluster := idptest.New(t)
	srv := testServer(t)
	srv.issuer = nil
	srv.Start(t.Context())
	if err := srv.SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL}}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, cluster
}

// A registration records the subject verse will recognise the runner by, and refuses one where
// nothing could ever verify it.
func TestARegistrationNamesTheRunnersSubject(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	face := bareFace(t)
	// Unprobed: a bare face answers no token, and the handshake is the next test's.
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add with a subject = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	var one map[string]any
	sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
	if err != nil {
		t.Fatal(err)
	}
	decodeResp(t, sr, &one)
	if one["subject"] != runnerSub {
		t.Fatalf("the registration = %v, want subject %s", one, runnerSub)
	}

	// A subject with space in it matches no token, and is refused as written.
	resp = postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu2", "url": face.URL, "subject": "system:serviceaccount: verse:gpu"})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "holds no space") {
		t.Fatalf("a subject with space = %s: %s", resp.Status, body)
	}

	// One subject, one principal: a second registration claiming gpu's subject is refused, and a
	// registration may keep its own on a re-add.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu2", "url": face.URL, "subject": runnerSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, `already runner "gpu"`) {
		t.Fatalf("a second registration with gpu's subject = %s: %s", resp.Status, body)
	}
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-add with its own subject = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	// ... and one verified principal is one registration: another subject under gpu's identity
	// would be a record that cannot say which runner acted.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu-b", "url": face.URL, "identity": "gpu", "subject": "system:serviceaccount:verse:gpu-b"})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "a verified principal is one registration") {
		t.Fatalf("a second subject under one identity = %s: %s", resp.Status, body)
	}
	// ... and a subject that is an iod's is the iod's: the door would take the token for the
	// iod before it looked among the registrations.
	withIod := testServer(t)
	withIod.Start(t.Context())
	iodSub := "system:serviceaccount:verse:slack-iod"
	if err := withIod.SetAuth(Auth{Iod: AuthIod{Issuer: face.URL, Accounts: map[string]IodAccount{iodSub: {Principal: "agent:slack-iod", IO: "slack"}}}}); err != nil {
		t.Fatal(err)
	}
	tsIod := httptest.NewServer(withIod.Handler())
	defer tsIod.Close()
	if err := withIod.SetIssuer(tsIod.URL, issuerKey(t), "config"); err != nil {
		t.Fatal(err)
	}
	resp = postTony(t, tsIod.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": iodSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "auth.iod.accounts") {
		t.Fatalf("an iod's subject on a registration = %s: %s", resp.Status, body)
	}
	// ... and in the other order, a config naming a registered runner's subject as an iod's is
	// refused at start, so no subject is ever two parties at the door.
	if err := srv.SetAuth(Auth{Iod: AuthIod{Issuer: cluster.URL, Accounts: map[string]IodAccount{runnerSub: {Principal: "agent:gpu-iod"}}}}); err == nil ||
		!strings.Contains(err.Error(), "registration gpu") {
		t.Fatalf("an iod account with a registered runner's subject = %v", err)
	}

	// A principal is one thing across the verse: a subject in a named scope is refused, since the
	// door reads the baseline registrations and would neither admit nor refuse the runner.
	resp = postTony(t, ts.URL+"/runners?probe=false&scope=sandbox", map[string]any{"name": "gpu3", "url": face.URL, "subject": runnerSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "baseline") {
		t.Fatalf("a subject in a scope = %s: %s", resp.Status, body)
	}

	// A verse naming no workload issuer refuses a subject, naming the config that would let it.
	plain := testServer(t)
	tsPlain := httptest.NewServer(plain.Handler())
	defer tsPlain.Close()
	resp = postTony(t, tsPlain.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "auth.iod.issuer") {
		t.Fatalf("a subject at a verse with no workload issuer = %s: %s", resp.Status, body)
	}
}

// A runner is its principal when it presents its workload token, under every mode, and its name
// alone is refused, registered or not.
func TestARunnerProvesItselfWithItsWorkloadToken(t *testing.T) {
	_, ts, cluster := runnerServer(t)
	face := bareFace(t)
	for _, body := range []map[string]any{
		{"name": "gpu", "url": face.URL, "identity": "gpu-bot", "subject": runnerSub},
	} {
		resp := postTony(t, ts.URL+"/runners?probe=false", body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("add %v = %s: %s", body["name"], resp.Status, readBody(t, resp))
		}
		resp.Body.Close()
	}
	whoami := ts.URL + "/whoami?format=tony"

	token := cluster.Token(iodAudience, runnerSub, nil)
	code, body := relay(t, http.MethodGet, whoami, token, "", "", "")
	if who := tonyOf(t, body); code != http.StatusOK || who["principal"] != "runner:gpu-bot" || who["by"] != runnerProvider {
		t.Fatalf("whoami with the runner's token = %d %s", code, body)
	}
	if code, body := relay(t, http.MethodGet, whoami, token, "runner:gpu-bot", "", ""); code != http.StatusOK {
		t.Errorf("the runner naming itself = %d %s", code, body)
	}
	if code, body := relay(t, http.MethodGet, whoami, token, "user:alice@example.com", "", ""); code != http.StatusForbidden || !strings.Contains(body, "made as itself") {
		t.Errorf("the runner naming a person = %d %s, want 403", code, body)
	}
	// A runner relays a task (relayTask), never an account: an account to relay beside its token
	// is refused, not carried out as the runner with the account unread.
	if code, body := relay(t, http.MethodGet, whoami, token, "", "github:alice", ""); code != http.StatusForbidden || !strings.Contains(body, "never an account") {
		t.Errorf("the runner naming an account to relay = %d %s, want 403", code, body)
	}
	for what, bad := range map[string]string{
		"a token for another audience": cluster.Token("other", runnerSub, nil),
		"a subject nobody registered":  cluster.Token(iodAudience, "system:serviceaccount:verse:stranger", nil),
		"a forged token":               cluster.Forged(iodAudience, runnerSub, nil),
	} {
		if code, body := relay(t, http.MethodGet, whoami, bad, "", "", ""); code != http.StatusUnauthorized {
			t.Errorf("%s = %d %s, want 401", what, code, body)
		}
	}
	if _, body := relay(t, http.MethodGet, whoami, cluster.Token(iodAudience, "system:serviceaccount:verse:stranger", nil), "", "", ""); !strings.Contains(body, "no registered runner") {
		t.Errorf("an unregistered subject's refusal does not say so: %s", body)
	}

	// A runner's name is refused on its own, a registered runner's and a name nobody registered.
	for _, name := range []string{"runner:gpu-bot", "runner:cpu-bot"} {
		code, body = relay(t, http.MethodGet, whoami, "", name, "", "")
		if code != http.StatusUnauthorized || !strings.Contains(body, "never taken at its word") {
			t.Errorf("the bare name %s = %d %s, want 401", name, code, body)
		}
	}
}

// tokenFile writes a token where a runner or a verse reads its own from, and answers the path.
func tokenFile(t *testing.T, dir, name, token string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// guardedFace is a runner face serving the verse at verseURL alone, by the tokens it signs for the
// subject of the token in file, which it answers on its describe.
func guardedFace(t *testing.T, verseURL, file string) *httptest.Server {
	t.Helper()
	g, err := runnerd.NewGuard(runnerd.Verse{URL: verseURL}, file)
	if err != nil {
		t.Fatal(err)
	}
	h, release := runnerFaceHandler()
	return faceServing(t, g.Wrap(h), release)
}

// The handshake before a launch is versed's observation, recorded as versed: a spawner who owns
// nothing under the registration launches on a runner whose registration holds no description,
// the handshake finds it proving itself, and the registration holds what it described, which the
// spawner could not have written.
func TestTheLaunchHandshakeIsRecordedAsVerse(t *testing.T) {
	_, ts, cluster := runnerServer(t)
	own := tokenFile(t, t.TempDir(), "gpu", cluster.Token(iodAudience, runnerSub, nil))
	face := guardedFace(t, ts.URL, own)
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	reg := func() map[string]any {
		sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
		if err != nil {
			t.Fatal(err)
		}
		var one map[string]any
		decodeResp(t, sr, &one)
		return one
	}
	// Replaced and found by a check, then itself again, with nothing having asked since.
	if err := os.WriteFile(own, []byte(cluster.Token(iodAudience, "system:serviceaccount:verse:other", nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	if cr, err := ts.Client().Get(ts.URL + "/runners/gpu/check"); err != nil {
		t.Fatal(err)
	} else {
		cr.Body.Close()
	}
	if one := reg(); one["described"] != nil {
		t.Fatalf("the registration after the check = %v, want what it described removed", one)
	}
	if err := os.WriteFile(own, []byte(cluster.Token(iodAudience, runnerSub, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Bob did not register gpu and may not write its registration; the launch's handshake is
	// recorded all the same.
	body, err := codec.Marshal(map[string]any{"id": "bobs", "spawn": map[string]any{"script": "true", "runner": "verse.runners.gpu"}}, codec.Tony)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/tony")
	req.Header.Set(api.PrincipalHeader, "user:bob@example.com")
	sr, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if out := readBody(t, sr); sr.StatusCode != http.StatusCreated && sr.StatusCode != http.StatusOK {
		t.Fatalf("bob's spawn on a runner proving itself again = %s: %s", sr.Status, out)
	}
	if one := reg(); one["described"] == nil {
		t.Fatalf("the registration after bob's launch = %v, want what it described recorded by verse", one)
	}
}

// The describe is the handshake: versed presents its token, the runner answers its own, and the
// registration records whether the runner proved it is its subject. One that does not takes no
// spawn, and a check that finds it proving itself again lets it.
func TestTheDescribeIsTheHandshake(t *testing.T) {
	srv, ts, cluster := runnerServer(t)
	dir := t.TempDir()
	own := tokenFile(t, dir, "gpu", cluster.Token(iodAudience, runnerSub, nil))
	face := guardedFace(t, ts.URL, own)

	// Registered with its subject, the runner proves itself at the describe, and the registration says so.
	resp := postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	var row map[string]any
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	decodeResp(t, resp, &row)
	if row["reach"] != "a runner" || row["described"] == nil {
		t.Fatalf("the add's row = %v, want reach \"a runner\" and described", row)
	}
	reg := func() map[string]any {
		sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
		if err != nil {
			t.Fatal(err)
		}
		var one map[string]any
		decodeResp(t, sr, &one)
		return one
	}
	if one := reg(); one["described"] == nil {
		t.Fatalf("the registration = %v, want described", one)
	}

	// A subject that is another runner's: the token versed mints for it is one that runner's door
	// does not take, so the describe is refused and the add reads unreachable, with the runner's
	// own words saying for whom it takes tokens. Installed with the warning, as any runner
	// nothing answers at is; the hello of the runner that holds that subject never finds this
	// registration, and says so in its log.
	other := guardedFace(t, ts.URL, tokenFile(t, dir, "other", cluster.Token(iodAudience, "system:serviceaccount:verse:other", nil)))
	gpu2Sub := "system:serviceaccount:verse:gpu2"
	resp = postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu2", "url": other.URL, "subject": gpu2Sub})
	decodeResp(t, resp, &row)
	if why := fmt.Sprint(row["why"]); resp.StatusCode != http.StatusCreated || row["reach"] != "unreachable" || !strings.Contains(why, "refused this verse") || !strings.Contains(why, "system:serviceaccount:verse:other") {
		t.Fatalf("another runner's subject = %s %v", resp.Status, row)
	}
	// A runner answering no token is refused at the add: waiting fixes nothing.
	resp = postTony(t, ts.URL+"/runners", map[string]any{"name": "gpu3", "url": bareFace(t).URL, "subject": "system:serviceaccount:verse:gpu3"})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "answered no "+runner.TokenHeader) {
		t.Fatalf("a runner answering no token = %s: %s", resp.Status, body)
	}

	// The runner behind gpu's url is replaced by one that is somebody else: the check finds it,
	// the registration says so in place of what it described, and a spawn naming it is refused.
	if err := os.WriteFile(own, []byte(cluster.Token(iodAudience, "system:serviceaccount:verse:other", nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nothing has checked yet and the registration still holds its description: the launch asks anyway,
	// since the proof is at the describe and the launch is what would hand the impostor the
	// prompt. The spawn is refused and the registration records what the handshake found.
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "nope0", "spawn": map[string]any{"script": "true", "runner": "verse.runners.gpu"}})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "has not proved it is the runner registered") {
		t.Fatalf("a spawn on a replaced runner, before any check = %s: %s", resp.Status, body)
	}
	if one := reg(); one["described"] != nil {
		t.Fatalf("the registration after the refused launch = %v, want what it described removed", one)
	}
	cr, err := ts.Client().Get(ts.URL + "/runners/gpu/check")
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]any
	decodeResp(t, cr, &check)
	if check["reach"] != "not verified" || !strings.Contains(fmt.Sprint(check["why"]), "registration says") {
		t.Fatalf("check of a replaced runner = %v", check)
	}
	if one := reg(); one["described"] != nil {
		t.Fatalf("the registration after the check = %v, want what it described removed", one)
	}
	resp = postTony(t, ts.URL+"/tasks", map[string]any{"id": "nope", "spawn": map[string]any{"script": "true", "runner": "verse.runners.gpu"}})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "has not proved it is the runner registered") {
		t.Fatalf("a spawn on an unverified runner = %s: %s", resp.Status, body)
	}
	if _, ok, _ := srv.Store.Get(mirror.Ref("nope")); ok {
		t.Fatal("the refused spawn was mirrored")
	}

	// The runner is itself again, and a check lets it back.
	if err := os.WriteFile(own, []byte(cluster.Token(iodAudience, runnerSub, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	cr, err = ts.Client().Get(ts.URL + "/runners/gpu/check")
	if err != nil {
		t.Fatal(err)
	}
	decodeResp(t, cr, &check)
	if check["reach"] != "a runner" {
		t.Fatalf("check of the runner back = %v", check)
	}
	if one := reg(); one["verified"] == "" || one["described"] == nil || one["unverified"] != nil {
		t.Fatalf("the registration after the second check = %v", one)
	}

	// A registration is written whole. An add that describes nothing now, here unprobed with a
	// subject the runner has not proved, leaves no `verified` behind for the new subject to
	// wear; and one that names no subject takes the runner at its word, whatever the last said.
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": "system:serviceaccount:verse:other"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-add with another subject = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	if one := reg(); one["subject"] != "system:serviceaccount:verse:other" || one["described"] != nil {
		t.Fatalf("the registration after a re-add with another subject = %v, want the subject alone", one)
	}
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "never taken at its word") {
		t.Fatalf("re-add without a subject = %s: %s, want it refused", resp.Status, body)
	}
	if one := reg(); one["subject"] != "system:serviceaccount:verse:other" {
		t.Fatalf("the registration after a refused re-add = %v, want it as it was", one)
	}

	// The token goes where a vouched identity is, and every registration is one: it is probed as
	// this verse.
	var bearerSeen atomic.Bool
	h, release := runnerFaceHandler()
	seeing := faceServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/describe" {
			bearerSeen.Store(r.Header.Get("Authorization") != "")
		}
		h.ServeHTTP(w, r)
	}), release)
	resp = postTony(t, ts.URL+"/runners", map[string]any{"name": "vouched", "url": seeing.URL, "subject": "system:serviceaccount:verse:vouched"})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !bearerSeen.Load() {
		t.Fatalf("a registration with a subject at a bare face = %s, bearer on its describe = %v; want the refusal and the bearer: %s", resp.Status, bearerSeen.Load(), body)
	}

	// A verse that issues nothing could never prove itself to a guarded runner: a subject is
	// refused at the add, naming the issuer it lacks.
	_, tsPlain, _ := wordOnlyServer(t)
	resp = postTony(t, tsPlain.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "issues none") || !strings.Contains(body, "auth.issuer") {
		t.Fatalf("a verse issuing nothing adding a guarded runner with its subject = %s: %s", resp.Status, body)
	}
	// Another verse's token, signed by another key under another name, is refused at the
	// describe, which the add reads as unreachable: installed, with the warning, since the
	// deployment fixes it.
	_, tsWrong, _ := runnerServer(t)
	resp = postTony(t, tsWrong.URL+"/runners", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	decodeResp(t, resp, &row)
	if resp.StatusCode != http.StatusCreated || row["reach"] != "unreachable" || !strings.Contains(fmt.Sprint(row["why"]), "refused this verse") {
		t.Fatalf("another verse at a guarded runner = %s %v", resp.Status, row)
	}
	// And the token minted for one runner opens no other's door: the token versed presented to
	// gpu, replayed at other, is refused there.
	var replayed string
	seen := faceServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replayed = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Write([]byte("{}"))
	}), func() {})
	resp = postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "seen", "url": seen.URL, "subject": runnerSub})
	resp.Body.Close()
	ts.Client().Get(ts.URL + "/runners/seen/check")
	if replayed == "" {
		t.Fatal("versed presented no token to the registration with a subject")
	}
	req, _ := http.NewRequest(http.MethodGet, other.URL+"/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+replayed)
	rr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()
	if rr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gpu's token replayed at other = %s, want 401: a token is minted for one runner", rr.Status)
	}
}

// The hello is the check, asked by the runner itself: a registered runner that says it is here,
// with its token, is described then and there, and the registration says verified. A runner
// without a subject has nothing to announce, and a token no registration names is refused at
// the door.
func TestTheHelloIsTheCheckAskedByTheRunner(t *testing.T) {
	helloRetryWait = 10 * time.Millisecond
	_, ts, cluster := runnerServer(t)
	dir := t.TempDir()
	own := cluster.Token(iodAudience, runnerSub, nil)
	face := guardedFace(t, ts.URL, tokenFile(t, dir, "gpu", own))
	// Registered unprobed, as a registration written before its pod is up: nothing is described.
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "identity": "gpu-bot", "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	reg := func() map[string]any {
		sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
		if err != nil {
			t.Fatal(err)
		}
		var one map[string]any
		decodeResp(t, sr, &one)
		return one
	}
	if one := reg(); one["described"] != nil || one["verified"] != nil {
		t.Fatalf("before the hello = %v, want nothing described", one)
	}

	hello := ts.URL + "/runners/hello?format=tony"
	code, body := relay(t, http.MethodPost, hello, own, "", "", "")
	if out := tonyOf(t, body); code != http.StatusOK || out["reach"] != "a runner" || out["at"] != "verse.runners.gpu" {
		t.Fatalf("the hello = %d %s", code, body)
	}
	if one := reg(); one["verified"] == "" || one["described"] == nil || one["unverified"] != nil {
		t.Fatalf("after the hello = %v, want verified and described", one)
	}

	// A token no registration names is refused at the door, before any handler: that is the
	// runner's cue to keep announcing.
	if code, body := relay(t, http.MethodPost, hello, cluster.Token(iodAudience, "system:serviceaccount:verse:stranger", nil), "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("a stranger's hello = %d %s, want 401", code, body)
	}
	// A runner's name with no token is refused at the door: there is no runner taken at its word
	// to announce itself.
	if code, body := relay(t, http.MethodPost, hello, "", "runner:cpu-bot", "", ""); code != http.StatusUnauthorized || !strings.Contains(body, "never taken at its word") {
		t.Fatalf("a runner's bare name's hello = %d %s, want 401", code, body)
	}
	// A person's hello is nobody's.
	if code, _ := relay(t, http.MethodPost, hello, "", "user:alice@example.com", "", ""); code != http.StatusBadRequest {
		t.Fatalf("a person's hello = %d, want 400", code)
	}
}

// A hello whose describe offers a doer twice describes nothing: a launch names its doer, and
// could not say which of the two it means, so the registration stays undescribed and the runner
// is told why.
func TestAHelloOfferingADoerTwiceDescribesNothing(t *testing.T) {
	helloRetryWait = 10 * time.Millisecond
	_, ts, cluster := runnerServer(t)
	own := cluster.Token(iodAudience, runnerSub, nil)
	g, err := runnerd.NewGuard(runnerd.Verse{URL: ts.URL}, tokenFile(t, t.TempDir(), "gpu", own))
	if err != nil {
		t.Fatal(err)
	}
	face := faceServing(t, g.Wrap(offeringAll(runnerd.NewInProcessHandler(nil), "fake", "fake").HTTPHandler()), func() {})
	resp := postTony(t, ts.URL+"/runners?probe=false", map[string]any{"name": "gpu", "url": face.URL, "subject": runnerSub})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %s: %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()
	code, body := relay(t, http.MethodPost, ts.URL+"/runners/hello?format=tony", own, "", "", "")
	if code == http.StatusOK || !strings.Contains(body, "it offers fake twice") {
		t.Fatalf("the hello of a runner offering fake twice = %d %s, want it refused saying so", code, body)
	}
	sr, err := ts.Client().Get(ts.URL + "/runners/gpu")
	if err != nil {
		t.Fatal(err)
	}
	var one map[string]any
	decodeResp(t, sr, &one)
	if one["described"] != nil {
		t.Fatalf("after the refused hello = %v, want nothing described", one)
	}
}
