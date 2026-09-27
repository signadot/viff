package versed

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/runner"
)

// alicesRunner installs a runner that is alice's alone, in a verse that holds nothing out.
func alicesRunner(t *testing.T, srv *Server, name string) {
	t.Helper()
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	if _, _, err := srv.Store.Write(entity.Write{
		At:    runner.Ref(name).At(),
		Value: entity.Obj{"name": name, "url": "http://127.0.0.1:1"}.Payload(),
	}); err != nil {
		t.Fatalf("installing runner %s: %v", name, err)
	}
	owns(t, srv, "user:alice@example.com", nil, runner.Ref(name))
}

// A runner the asker may not read and a runner that is not there are ONE ANSWER, in the
// asker's view: it says neither that the runner exists nor that it does not.
func TestARunnerOutsideTheAskersViewIsOneAnswer(t *testing.T) {
	srv := testServer(t)
	alicesRunner(t, srv, "gpu")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := getAs(t, "alice@example.com", ts.URL+"/runners/gpu")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice reads her own runner: %s %s", resp.Status, readBody(t, resp))
	}
	resp.Body.Close()

	answers := map[string]string{}
	for _, name := range []string{"gpu", "nope"} {
		for _, route := range []string{"", "/check"} {
			resp := getAs(t, "bob@example.com", ts.URL+"/runners/"+name+route)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("bob reads runner %s%s: %s, want 404: %s", name, route, resp.Status, body)
			}
			if !strings.Contains(body, "in your view") {
				t.Errorf("runner %s%s: %q does not say whose view it is", name, route, body)
			}
			if strings.Contains(body, "alice") || strings.Contains(body, "refused") {
				t.Errorf("runner %s%s: %q says the runner is somebody's", name, route, body)
			}
			answers[name] = strings.ReplaceAll(body, name, "<name>")
		}
	}
	if answers["gpu"] != answers["nope"] {
		t.Fatalf("what bob may not read and what is not there read differently:\n  %s\n  %s",
			answers["gpu"], answers["nope"])
	}
}

// A spawn naming a runner its spawner may not read is refused in the spawner's view, and the
// runners named beside the refusal are the ones they may read.
func TestASpawnNamingARunnerOutsideTheSpawnersViewSaysWhoseViewItIs(t *testing.T) {
	srv := testServer(t)
	alicesRunner(t, srv, "gpu")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postAsTony(t, "bob@example.com", ts.URL+"/tasks", map[string]any{
		"id": "bobs", "prompt": "t", "runner": "verse.runners.gpu",
	})
	msg := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bob's spawn onto alice's runner = %s, want 400: %s", resp.Status, msg)
	}
	if !strings.Contains(msg, "in the view of user:bob@example.com") {
		t.Errorf("refusal = %q, want it to say whose view it is", msg)
	}
	if strings.Contains(msg, "alice") {
		t.Errorf("refusal = %q, which says the runner is somebody's", msg)
	}
	if _, ok, _ := srv.Store.Get(mirror.Ref("bobs")); ok {
		t.Fatal("the refused spawn was mirrored")
	}
}

// A read that did not happen is not a runner that is not installed: it is the store's error, on
// the lookup and on the spawn alike, and neither says "installed".
func TestARunnerReadThatFailedIsNotARunnerNotInstalled(t *testing.T) {
	srv := testServer(t)
	srv.Store = failingStore{Store: srv.Store, err: errors.New("context deadline exceeded")}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, route := range []string{"/runners/gpu", "/runners/gpu/check", "/io/git", "/io/git/check"} {
		resp := getAs(t, tester, ts.URL+route)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with the store unreadable = %s, want 503: %s", route, resp.Status, body)
		}
		if !strings.Contains(body, "context deadline exceeded") {
			t.Errorf("%s: the cause did not reach the caller: %q", route, body)
		}
		if strings.Contains(body, "in your view") {
			t.Errorf("%s: a failed read was answered as absence: %q", route, body)
		}
	}

	resp := postTony(t, ts.URL+"/tasks", map[string]any{
		"id": "waits", "prompt": "t", "runner": "verse.runners.gpu",
	})
	msg := readBody(t, resp)
	if !strings.Contains(msg, "could not be read") || !strings.Contains(msg, "context deadline exceeded") {
		t.Errorf("spawn with the store unreadable = %s %q, want the read's failure", resp.Status, msg)
	}
	if strings.Contains(msg, "is installed") || strings.Contains(msg, "are installed") {
		t.Errorf("spawn with the store unreadable = %q, which says what is installed", msg)
	}
}
