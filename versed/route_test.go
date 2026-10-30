package versed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// routedServer is a verse whose own runner hosts nothing, with two runners registered by workload
// token, each offering the agent `fake` and scripts: `a` and `b`, in that order by name. It
// answers the token files, which a test rewrites to make a runner somebody else.
func routedServer(t *testing.T) (*Server, *httptest.Server, map[string]string) {
	t.Helper()
	srv, ts, cluster := runnerServer(t)
	noLocalRunner(t, srv)
	files := map[string]string{}
	for _, name := range []string{"a", "b"} {
		sub := "system:serviceaccount:verse:" + name
		files[name] = tokenFile(t, t.TempDir(), name, cluster.Token(iodAudience, sub, nil))
		face := guardedFace(t, ts.URL, files[name])
		if resp := postTony(t, ts.URL+"/runners", map[string]any{"name": name, "url": face.URL, "subject": sub}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("add %s = %s: %s", name, resp.Status, readBody(t, resp))
		}
	}
	return srv, ts, files
}

// ONE NOT CARRYING MORE, THEN BY NAME. Two runners taking a spawn, both carrying nothing, are
// tried in order of their names; once `a` carries a task, `b` carries less and takes the next.
func TestASpawnGoesToTheRunnerCarryingLess(t *testing.T) {
	srv, ts, _ := routedServer(t)
	defer ts.Close()
	first := spawnOK(t, ts, map[string]any{"id": "first", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	waitTaskStatus(t, srv, first, "running")
	if got := instanceOf(t, srv, first)["runnerName"]; got != "verse.runners.a" {
		t.Fatalf("with both idle the first went to %v, want verse.runners.a, first by name", got)
	}
	second := spawnOK(t, ts, map[string]any{"id": "second", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if got := instanceOf(t, srv, second)["runnerName"]; got != "verse.runners.b" {
		t.Fatalf("with a carrying a task the second went to %v, want verse.runners.b", got)
	}
}

// A RUNNER THAT FAILS THE HANDSHAKE IS PASSED OVER FOR THE NEXT. The first in order is now
// somebody else: its handshake fails, what it described is removed, and the spawn goes to the
// next. With every runner failing it, the spawn is refused naming each and why.
func TestAHandshakeThatFailsFallsThroughToTheNextRunner(t *testing.T) {
	srv, ts, files := routedServer(t)
	defer ts.Close()
	tokens := map[string][]byte{}
	for name, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = b
	}
	// answers makes runner name answer other's token: it proves a subject its registration does
	// not name.
	answers := func(name, other string) {
		t.Helper()
		if err := os.WriteFile(files[name], tokens[other], 0o600); err != nil {
			t.Fatal(err)
		}
	}

	answers("a", "b")
	id := spawnOK(t, ts, map[string]any{"id": "through", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	if got := instanceOf(t, srv, id)["runnerName"]; got != "verse.runners.b" {
		t.Fatalf("the spawn went to %v, want verse.runners.b past a, which failed its handshake", got)
	}
	if e, ok := findRunner(srv.Store, "a"); !ok || e.Current() {
		t.Fatalf("a after its failed handshake = %+v, want what it described removed", e.Described)
	}

	// b is somebody else too, and a is not described: nothing takes the spawn.
	answers("b", "a")
	resp := postTony(t, ts.URL+"/tasks", map[string]any{"id": "nowhere", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "took it at the handshake") ||
		!strings.Contains(body, "verse.runners.b has not proved it is the runner registered") ||
		!strings.Contains(body, "passed over: verse.runners.a, not described") {
		t.Fatalf("a spawn every runner fails = %s: %s, want 400 naming b's handshake and a passed over", resp.Status, body)
	}
}

// A RUN STILL LIVE IS LAUNCHED WHERE IT RUNS: a second launch of its id goes to the runner its
// record names, whose join answers with the run, rather than by load to the other runner, which
// would start a second copy and leave the first unreachable.
func TestALiveRunIsLaunchedAgainWhereItRuns(t *testing.T) {
	srv, ts, _ := routedServer(t)
	defer ts.Close()
	spawn := map[string]any{"id": "dup", "spawn": map[string]any{"agent": map[string]any{"name": "fake"}, "prompt": "t"}}
	spawnOK(t, ts, spawn)
	waitTaskStatus(t, srv, "dup", "running")
	if got := instanceOf(t, srv, "dup")["runnerName"]; got != "verse.runners.a" {
		t.Fatalf("the first launch went to %v, want verse.runners.a, first by name", got)
	}
	spawnOK(t, ts, spawn) // by load, b: a carries the run
	// The record is the mirror's to rewrite, after the launch answers: watched for a while, it
	// must not move to b.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got := instanceOf(t, srv, "dup")["runnerName"]; got != "verse.runners.a" {
			t.Fatalf("the second launch of the live id went to %v, want verse.runners.a, where it runs", got)
		}
	}
}
