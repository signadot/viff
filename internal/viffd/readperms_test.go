package viffd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
	"go.viff.dev/viff/pkg/mirror"
)

func getAs(t *testing.T, as, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if as != "" {
		req.Header.Set(api.PrincipalHeader, as)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The perms mirror is the system's: a person reading it is refused, and told so rather than
// told nothing stands there.
func TestTheMirrorIsNotAPersonsToRead(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()

	rec := entity.PermsRef(entity.RefAt("demo", "pr", "1"))
	resp = getAs(t, "alice@example.com", ts.URL+"/entity?at="+rec.String())
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "reading it needs read") {
		t.Fatalf("alice read her own owner record: %s %s", resp.Status, body)
	}
	// Her own pr, though, and anybody else's in the open viff.
	resp = getAs(t, "bob@example.com", ts.URL+"/entity?at=demo.pr.1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob reads alice's pr in the open viff: %s", resp.Status)
	}
}

// With the open reading rule off, a listing and a watch answer a person only what they may
// read: alice's task is alice's to see, and bob's docket does not list her question.
func TestAListingAnswersOnlyWhatMayBeRead(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)

	pr(t, srv, "1", "payments")
	spawnAs(t, "alice@example.com", ts.URL, "run-1", `change.pr."1"`)
	waitQuestion(t, srv, "run-1")

	listing := func(as string) string {
		resp := getAs(t, as, ts.URL+"/state?at=mirror.task.instance")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(body)
	}
	if got := listing("bob@example.com"); strings.Contains(got, "run-1") {
		t.Fatalf("bob's listing shows alice's task: %s", got)
	}
	if got := listing("alice@example.com"); !strings.Contains(got, "run-1") {
		t.Fatalf("alice's listing does not show her task: %s", got)
	}
	resp := getAs(t, "bob@example.com", ts.URL+"/entity?at="+mirror.Ref("run-1").String())
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob read alice's task: %s", resp.Status)
	}
}

// A charter reads as its owner: two users, two charters, one entity only one of them may
// read. The owner's rule fires on it and the other's does not — and the other's leaves no
// trace, since a rule that cannot see a subject is not woken by it.
func TestACharterReadsAsItsOwner(t *testing.T) {
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	entity.PolicyOf(srv.Store).SetRootChildGrants(nil)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, who := range []struct{ name, principal, mark string }{
		{"alices-rule", "alice@example.com", "alice"},
		{"bobs-rule", "bob@example.com", "bob"},
	} {
		resp := postAs(t, who.principal, ts.URL+"/charter", "application/tony", `{
			name: `+who.name+`,
			condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
			action: {patch: {seenBy: {`+who.mark+`: true}}},
		}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("install %s: %s", who.name, resp.Status)
		}
	}
	resp := putAs(t, "alice@example.com", ts.URL, "demo.pr.1", entity.Obj{"stage": "open"}.Payload())
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("alice's put: %s", resp.Status)
	}

	pr := entity.RefAt("demo", "pr", "1")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := srv.Store.Get(pr); ok && entity.Get(entity.Get(e.Payload, "seenBy"), "alice") != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, _, _ := srv.Store.Get(pr)
	seen := entity.Get(e.Payload, "seenBy")
	if entity.Get(seen, "alice") == nil {
		t.Fatalf("alice's rule never fired on her pr: %v", e.Payload)
	}
	// Bob's had every chance the same commits gave alice's, and did not.
	time.Sleep(100 * time.Millisecond)
	e, _, _ = srv.Store.Get(pr)
	if entity.Get(entity.Get(e.Payload, "seenBy"), "bob") != nil {
		t.Fatalf("bob's rule fired on alice's pr: %v", e.Payload)
	}
	if got := owner(t, srv, charter.RuleRef("", "bobs-rule")); got != "user:bob@example.com" {
		t.Fatalf("bob's rule is owned by %q", got)
	}

	// And a rule of a person's reading the mirror is refused at install, saying why.
	resp = postAs(t, "bob@example.com", ts.URL+"/charter", "application/tony", `{
		name: policy-watch,
		condition: {subject: {path: "mirror.perms.node.*"}},
		action: {patch: {noted: true}},
	}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 == 2 || !strings.Contains(string(body), "perms mirror") {
		t.Fatalf("bob's rule over the mirror was installed, or refused for another reason: %s %s", resp.Status, body)
	}
}
