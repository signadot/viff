package versed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
)

// installScoped installs a `dir` source into a scope (empty = baseline) and returns the
// response, so a test can assert on a refusal as well as on a success.
func installScoped(t *testing.T, ts *httptest.Server, scope, name, dir string) *http.Response {
	t.Helper()
	u := ts.URL + "/sources"
	if scope != "" {
		u += "?scope=" + scope
	}
	return postTony(t, u, map[string]any{"system": "git", "name": name, "dir": dir})
}

// A scoped source writes into its scope and NEVER into baseline.
//
// It follows from holding a scoped store, which is exactly why it needs a test: a converge
// handed the baseline store by accident corrupts what everyone reads, and nothing in the
// reflection would say so — the entities would be right, in the wrong place, for everybody.
func TestAScopedSourceWritesIntoItsScopeAndNotBaseline(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	if resp := installScoped(t, ts, "sandbox", "mine", dir); resp.StatusCode != http.StatusCreated {
		t.Fatalf("scoped install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	head := entity.Ref{System: source.GitSystem, Kind: "head", ID: baseOf(dir)}
	waitForRef(t, srv.Store.Scope("sandbox"), head)

	// Baseline read DIRECTLY, since a scoped read would find its own overlay and say nothing
	// about where the write landed.
	if _, ok, _ := srv.Store.Get(head); ok {
		t.Fatal("a scoped reflection landed in baseline, where everybody reads it")
	}
	// …and so did the registration, which is what makes its lifetime free.
	if _, ok := source.Find(srv.Store, "git:mine"); ok {
		t.Fatal("the registration landed in baseline rather than in the scope it reflects into")
	}
	if _, ok := source.Find(srv.Store.Scope("sandbox"), "git:mine"); !ok {
		t.Fatal("the registration should live in the scope it reflects into")
	}
}

// Shadowing is the FEATURE, and it is why the ownership check is scoped.
//
// A scoped store reads baseline layered underneath, so an unscoped check would see baseline's
// registrations and refuse the very case a sandbox exists for: my checkout's branches shadowing
// baseline's view of the same repo, for my triggers, without touching anyone else's.
func TestAScopedSourceMayShadowABaselineOne(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	if resp := installScoped(t, ts, "", "shared", dir); resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	// The SAME repo, in a scope: not a collision, and refusing it would refuse the motivating
	// case.
	if resp := installScoped(t, ts, "sandbox", "mine", dir); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a scoped source shadowing a baseline one = %s, want 201: %s",
			resp.Status, bodyOf(t, resp))
	}
	// But two in ONE scope must still be provably disjoint — single-writer holds inside a
	// scope exactly as it does in baseline.
	resp := installScoped(t, ts, "sandbox", "also-mine", dir)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("two sources over one repo in one scope = %s, want 409", resp.Status)
	}
}

// A registration must live in the scope it reflects into, so the two spellings of "which
// scope" are not allowed to disagree — that would be a registration in one place writing into
// another, which nothing would ever clean up.
func TestAScopedRegistrationNamesItsScopeOnce(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postTony(t, ts.URL+"/sources?scope=a", map[string]any{
		"system": "git", "name": "x", "dir": repoWithCommit(t), "scope": "b",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a registration naming two scopes = %s, want 400", resp.Status)
	}
}

// Deleting the scope takes the registration, what it reflected, and the reflector — which is
// what makes a scoped source have no deregistration path to forget, and why it cannot outlive
// the sandbox it was for.
func TestDeletingAScopeTakesItsSources(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	if resp := installScoped(t, ts, "sandbox", "mine", dir); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	head := entity.Ref{System: source.GitSystem, Kind: "head", ID: baseOf(dir)}
	waitForRef(t, srv.Store.Scope("sandbox"), head)

	resp := postTony(t, ts.URL+"/scope/delete?scope=sandbox", map[string]any{})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scope delete = %s: %s", resp.Status, bodyOf(t, resp))
	}

	// The reflector is stopped: nothing is left writing into a scope that no longer exists,
	// re-creating an overlay nobody asked for one converge at a time.
	srv.srcMu.Lock()
	n := len(srv.srcRuns)
	srv.srcMu.Unlock()
	if n != 0 {
		t.Fatalf("%d reflector(s) still running after their scope was deleted", n)
	}
	// Give a pass that was already in flight a moment to be wrong, if it is going to be.
	time.Sleep(200 * time.Millisecond)
	if _, ok := source.Find(srv.Store.Scope("sandbox"), "git:mine"); ok {
		t.Fatal("the registration survived its scope")
	}
	if _, ok, _ := srv.Store.Get(head); ok {
		t.Fatal("a scoped reflection reached baseline")
	}
}

// The claim scoped sources rest on, and the one the note calls the open question until it was
// measured: **a scope can delete what it does not own.**
//
// A scoped converge tombstones what external truth no longer has, and inside a scope most of
// what it finds standing is INHERITED from baseline. If a scoped delete could only remove
// scope-local entities, a scoped source could not say "that branch does not exist in this
// sandbox" and every scope would be stuck with baseline's set as a floor — which is the
// opposite of what a sandbox is for.
//
// The store primitive is measured by entity.TestLogdStore_ScopeHidesAnInheritedEntity; this is
// a reflection running on top of it, which is what the note says had not been done.
func TestAScopedReflectionHidesAnInheritedEntity(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	repo := baseOf(dir)
	// A branch baseline believes in and the repo does not have. It is under the cover a git
	// reflection claims (`git:ref:{repo}`), so a pass that sees it must decide about it.
	ghost := entity.Ref{System: source.GitSystem, Kind: "ref", ID: repo + "/ghost"}
	entitytest.Set(t, srv.Store, ghost, entity.Obj{
		"repo": repo, "name": "ghost", "sha": "deadbeef", "type": "commit",
	}.Payload())

	if resp := installScoped(t, ts, "sandbox", "mine", dir); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	scoped := srv.Store.Scope("sandbox")
	waitForRef(t, scoped, entity.Ref{System: source.GitSystem, Kind: "head", ID: repo})

	// In the scope, the branch is gone: this checkout does not have it, and that is what the
	// scope's triggers should see.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := scoped.Get(ghost); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok, _ := scoped.Get(ghost); ok {
		t.Fatal("a scoped reflection could not say a branch inherited from baseline is gone " +
			"— every scope would be stuck with baseline's set as a floor")
	}
	// And baseline still holds it, for everybody who is not in this sandbox.
	if _, ok, _ := srv.Store.Get(ghost); !ok {
		t.Fatal("a scoped reflection deleted a baseline entity: the overlay is supposed to " +
			"hide it, not remove it")
	}
}

// The install check is what the whole slot design rests on, so this is the case it exists for:
// two sources that write two PLACES in the same entities install alongside each other, with no
// flag and no warning, because their claims are provably disjoint one segment further down.
//
// It is the counterpart of TestTwoReposAreProvablyDisjoint: if this needed --allow-overlap then
// the flag would be the ordinary path and would mark nothing, and converge's slot scoping would
// be defending a property nobody could declare.
func TestTwoSlotSourcesInstallAlongsideEachOther(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	slot := func(name, team string) *http.Response {
		return postTony(t, ts.URL+"/sources?probe=false", map[string]any{
			"system": "linear", "name": name, "url": "http://x:1",
			"owns": []any{map[string]any{
				"ref": "linear:project:*", "at": []any{"owners", team},
			}},
		})
	}
	if resp := slot("eng", "ENG"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the first slot source = %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := slot("ops", "OPS"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("two slot sources over one entity = %s, want 201: %s",
			resp.Status, bodyOf(t, resp))
	}
	// The same slot twice is still two writers of one place, and is still refused — and the
	// refusal names the way out that is not a flag, since that is what somebody hitting this
	// most likely wants.
	resp := slot("eng-again", "ENG")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("two sources on ONE slot = %s, want 409", resp.Status)
	}
	body := bodyOf(t, resp)
	for _, want := range []string{"at: [owners, ENG]", "--allow-overlap"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the refusal does not mention %q: %s", want, body)
		}
	}
}

// A scoped source may claim the SAME slot a baseline source claims — shadowing is the feature
// one level down, and refusing it would refuse the sandbox case for shared entities only.
func TestAScopedSourceMayShadowABaselineSlot(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	slot := func(scope, name string) *http.Response {
		u := ts.URL + "/sources?probe=false"
		if scope != "" {
			u += "&scope=" + scope
		}
		return postTony(t, u, map[string]any{
			"system": "linear", "name": name, "url": "http://x:1",
			"owns": []any{map[string]any{
				"ref": "linear:project:*", "at": []any{"owners", "ENG"},
			}},
		})
	}
	if resp := slot("", "shared"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("baseline install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	if resp := slot("sandbox", "mine"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a scoped source shadowing a baseline slot = %s, want 201: %s",
			resp.Status, bodyOf(t, resp))
	}
	// And two in ONE scope are still two writers of one place.
	if resp := slot("sandbox", "also-mine"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("two slot sources in one scope = %s, want 409", resp.Status)
	}
}

// A registration that declines to say what it owns claims no slot either, so it is refused
// beside anything — unchanged by any of this, and asserted because "claims nothing" and "claims
// nothing in particular" are the two readings the check has always had to keep apart.
func TestASourceThatClaimsNothingIsStillRefusedBesideASlotSource(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if resp := postTony(t, ts.URL+"/sources?probe=false", map[string]any{
		"system": "linear", "name": "eng", "url": "http://x:1",
		"owns": []any{map[string]any{"ref": "linear:project:*", "at": []any{"owners", "ENG"}}},
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("slot install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	resp := postTony(t, ts.URL+"/sources?probe=false", map[string]any{
		"system": "linear", "name": "quiet", "url": "http://x:1",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a source claiming nothing = %s, want 409", resp.Status)
	}
}
