package versed

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// alicesRunner installs a runner that is alice's alone, in a verse that holds nothing out.
func alicesRunner(t *testing.T, srv *Server, name string) {
	t.Helper()
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	if _, _, err := srv.Store.Write(entity.Write{
		At:    runnerEntityRef(name).At(),
		Value: entity.Obj{"name": name, "url": "http://127.0.0.1:1"}.Payload(),
	}); err != nil {
		t.Fatalf("installing runner %s: %v", name, err)
	}
	owns(t, srv, "user:alice@example.com", nil, runnerEntityRef(name))
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
		"id": "bobs", "doer": map[string]any{"agent": "fake"}, "prompt": "t", "runner": "verse.runners.gpu",
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

	for _, route := range []string{"/runners/gpu", "/runners/gpu/check", "/iods/git", "/iods/git/check"} {
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
		"id": "waits", "doer": map[string]any{"agent": "fake"}, "prompt": "t", "runner": "verse.runners.gpu",
	})
	msg := readBody(t, resp)
	if !strings.Contains(msg, "could not be read") || !strings.Contains(msg, "context deadline exceeded") {
		t.Errorf("spawn with the store unreadable = %s %q, want the read's failure", resp.Status, msg)
	}
	if strings.Contains(msg, "is installed") || strings.Contains(msg, "are installed") {
		t.Errorf("spawn with the store unreadable = %q, which says what is installed", msg)
	}
}

// An address the asker's view holds nothing at says so as the view's, on both routes that read
// one address; an address the asker may not read into is refused, naming the nearest owner, and
// is the same refusal for a node that stands there and one that does not.
func TestAReadAtOneAddressSaysWhoseViewAndRefusesWhatIsNotTheirs(t *testing.T) {
	srv := testServer(t)
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := putAs(t, "alice@example.com", ts.URL, "spaces.alice.notes", entity.Obj{"todo": "milk"}.Payload())
	resp.Body.Close()

	for _, route := range []string{"/state?at=spaces.alice.nope", "/entity?at=spaces.alice.nope"} {
		resp := getAs(t, "alice@example.com", ts.URL+route)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "in your view, nothing stands at spaces.alice.nope") {
			t.Errorf("alice reads %s: %s %q, want 404 in her view", route, resp.Status, body)
		}
	}
	refusals := map[string]string{}
	for _, leaf := range []string{"notes", "nope"} {
		for _, route := range []string{"/state?at=spaces.alice.", "/entity?at=spaces.alice."} {
			resp := getAs(t, "bob@example.com", ts.URL+route+leaf)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "nearest owner on record is user:alice@example.com") {
				t.Errorf("bob reads %s%s: %s %q, want it refused naming the owner", route, leaf, resp.Status, body)
			}
			refusals[leaf] = strings.ReplaceAll(body, leaf, "<leaf>")
		}
	}
	if refusals["notes"] != refusals["nope"] {
		t.Errorf("the refusal says whether a node stands there:\n  %s\n  %s", refusals["notes"], refusals["nope"])
	}
}
