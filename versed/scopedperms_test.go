package versed

import (
	"context"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// A grant made in a scope governs the scope alone, and a scope is governed by baseline's grants
// (docs/perms.md#scopes): perms take the world's rule, copy-on-write, because a grant has no
// live half that runs anywhere — the check reads a mirror of records, and a mirror layers as
// records do.
//
// One mirror served baseline and every scope, loaded from and watched on baseline. A scoped
// grant landed in the overlay and was read back into that one mirror, so it governed baseline's
// writes in this process; and after a restart the overlay's records were not in it at all, so a
// scope's own creations answered as nobody's and refused their creator.
func TestAScopedGrantGovernsTheScopeAlone(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	firstCtx, down := context.WithCancel(t.Context())
	first.Start(firstCtx)
	firstHTTP := httptest.NewServer(first.Handler())
	defer firstHTTP.Close()

	const alice, bob = "alice@example.com", "bob@example.com"
	write := func(ts *httptest.Server, as, scope, at, stage string) (int, string) {
		t.Helper()
		url := ts.URL + "/entity"
		if scope != "" {
			url += "?scope=" + scope
		}
		put, err := codec.Marshal(api.PutEntity{At: at, Payload: entity.Obj{"stage": stage}.Payload()}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		resp := postAs(t, as, url, "application/tony", string(put))
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(raw))
	}
	admitted := func(ts *httptest.Server, as, scope, at, stage string) {
		t.Helper()
		if code, msg := write(ts, as, scope, at, stage); code/100 != 2 {
			t.Fatalf("%s writing %s in scope %q = %d %q, want it admitted", as, at, scope, code, msg)
		}
	}
	refused := func(ts *httptest.Server, as, scope, at, stage string) {
		t.Helper()
		if code, msg := write(ts, as, scope, at, stage); code != http.StatusForbidden {
			t.Fatalf("%s writing %s in scope %q = %d %q, want 403", as, at, scope, code, msg)
		}
	}
	grant := func(ts *httptest.Server, as, scope, at string, levels entity.Obj) {
		t.Helper()
		body, err := codec.Marshal(api.PermsEdit{At: at, Grant: levels.Payload()}, codec.Tony)
		if err != nil {
			t.Fatal(err)
		}
		url := ts.URL + "/perms"
		if scope != "" {
			url += "?scope=" + scope
		}
		req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/tony")
		req.Header.Set(api.PrincipalHeader, as)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("granting at %s in scope %q = %s %s", at, scope, resp.Status, strings.TrimSpace(string(raw)))
		}
	}

	// The scope is open — a dev's rule is installed into it — so its mirror is loaded and
	// watched, which is the path a connected dev's checks take.
	addScopedRule(t, firstHTTP, "me", `{
		name: connect-probe,
		condition: {subject: {path: "never.never.*"}},
		action: {patch: {seen: true}},
	}`)

	// alice's entity in baseline is hers everywhere: bob may not write it from baseline, and
	// not from the scope either — baseline's grants govern the scope.
	admitted(firstHTTP, alice, "", "demo.pr.1", "open")
	refused(firstHTTP, bob, "", "demo.pr.1", "merged")
	refused(firstHTTP, bob, "me", "demo.pr.1", "merged")

	// alice grants bob read-write on it IN THE SCOPE. bob writes it there, and there alone:
	// baseline's check never sees the overlay's grant. (Before: admitted in baseline too.)
	grant(firstHTTP, alice, "me", "demo.pr.1", entity.Obj{"read-write": []any{bob}})
	admitted(firstHTTP, bob, "me", "demo.pr.1", "merged")
	refused(firstHTTP, bob, "", "demo.pr.1", "merged")

	// What alice creates in the scope is hers in the scope.
	admitted(firstHTTP, alice, "me", "demo.pr.2", "open")
	refused(firstHTTP, bob, "me", "demo.pr.2", "merged")

	// A RESTART. The scope's records are in the overlay, which a mirror loaded from baseline
	// never held: alice was refused her own entity, and bob his grant. (Before: 403 on both.)
	down()
	first.stopAllIOs()
	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	secondHTTP := httptest.NewServer(second.Handler())
	defer secondHTTP.Close()

	admitted(secondHTTP, alice, "me", "demo.pr.2", "edited")
	refused(secondHTTP, bob, "me", "demo.pr.2", "merged")
	admitted(secondHTTP, bob, "me", "demo.pr.1", "again")
	refused(secondHTTP, bob, "", "demo.pr.1", "again")
}
