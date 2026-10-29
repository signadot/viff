package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd"
)

// THE LOCAL RUNNER IS A REGISTRATION LIKE ANY OTHER, written by the system: `verse up` registers
// it as `local`, the hello describes it, and a spawn is routed to it by what it offers. Registered
// again with nothing changed, it keeps its description; nobody but the system may take its name.
func TestTheLocalRunnerIsARegistrationLikeAnyOther(t *testing.T) {
	srv := testServer(t)
	srv.NoLocalRunner = true // no runner in process: the local runner is the registration
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	stopped := make(chan struct{}, 1)
	h, release := runnerFaceHandler()
	face := provenFace(t, ts.URL, runnerd.WithStop(h, func() { stopped <- struct{}{} }), release)

	if ours, err := srv.LocalRunnerAt(face.URL, face.Subject); !ours {
		t.Fatalf("the local runner at its url = %v (%v), want it proving its token", ours, err)
	}
	if ours, _ := srv.LocalRunnerAt(face.URL, someOtherSubject(t)); ours {
		t.Fatal("a runner proving another token was taken for the local runner")
	}
	if err := srv.RegisterLocalRunner(face.URL, face.Subject); err != nil {
		t.Fatal(err)
	}
	// Not described until it says hello, or is checked; the status says so, from the runner.
	st := apiStatus(t, ts)
	if st.Runner == nil || st.Runner.At != "verse.runners.local" || st.Runner.Described || st.Runner.Why == "" {
		t.Fatalf("status before the describe = %+v, want local not described and why", st.Runner)
	}
	if resp, err := http.Get(ts.URL + "/runners/local/check"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("check = %v %v", resp, err)
	}
	if st := apiStatus(t, ts); !st.Runner.Described || len(st.Runner.Offers) == 0 || st.Runner.URL != face.URL {
		t.Fatalf("status after the describe = %+v, want it described with what it offers", st.Runner)
	}
	id := spawnOK(t, ts, map[string]any{"spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if got := instanceOf(t, srv, id)["runnerName"]; got != "verse.runners.local" {
		t.Fatalf("the spawn went to %v, want the local runner", got)
	}
	// `verse ps --runner local` is the local runner's work, by its bare name.
	for _, ref := range []string{"local", "verse.runners.local"} {
		if got := psList(t, ts, ref); len(got) != 1 || got[0]["id"] != id {
			t.Fatalf("ps --runner %s = %v, want the one task on it", ref, got)
		}
	}

	// Registered again as it stands, it keeps what it described.
	if err := srv.RegisterLocalRunner(face.URL, face.Subject); err != nil {
		t.Fatal(err)
	}
	if e, ok := findRunner(srv.Store, LocalRunnerName); !ok || !e.Current() {
		t.Fatalf("registered again unchanged, local = %+v, want its description kept", e)
	}
	// Nobody but the system takes the name.
	resp := postTony(t, ts.URL+"/runners", face.reg(LocalRunnerName))
	if body := readBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "`verse up`'s") {
		t.Fatalf("a person registering `local` = %s: %s, want it refused", resp.Status, body)
	}

	// Stopped as its verse, and its registration removed.
	if err := srv.StopRunnerAt(face.URL, face.Subject); err != nil {
		t.Fatalf("stopping the local runner: %v", err)
	}
	<-stopped
	if err := srv.RemoveLocalRunner(); err != nil {
		t.Fatal(err)
	}
	if _, ok := findRunner(srv.Store, LocalRunnerName); ok {
		t.Fatal("the local runner's registration stands after it was removed")
	}
	if st := apiStatus(t, ts); st.Runner == nil || st.Runner.Why != "it is not registered" {
		t.Fatalf("status after the removal = %+v", st.Runner)
	}
}

// someOtherSubject is the subject of a static token no runner here holds.
func someOtherSubject(t *testing.T) string {
	t.Helper()
	return "sha256:" + strings.Repeat("0", 64)
}

// apiStatus is the verse's /status, decoded.
func apiStatus(t *testing.T, ts *httptest.Server) api.Status {
	t.Helper()
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st api.Status
	if err := codec.Decode(resp.Body, &st, codec.Tony); err != nil {
		t.Fatal(err)
	}
	return st
}
