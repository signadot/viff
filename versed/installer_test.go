package versed

import (
	"context"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// WHOSE A RULE IS, IS ITS OWNER RECORD'S TO SAY, and nothing else says it: the rule's own entity
// carries no installer, and the engine keeps no account of one. It is its installer's across a
// restart, through a charter file refused and put back, and the system's for the charter the
// deployment names — read, each time, from the one place that is checked.
func TestWhoseARuleIsIsSaidOnce(t *testing.T) {
	const alice = "user:alice@example.com"
	path := filepath.Join(t.TempDir(), "system.tony")
	if err := os.WriteFile(path, []byte(`{
		name: system-watch,
		condition: {subject: {path: "demo.sys.*", value: {stage: open}}},
		action: {patch: {stage: seen}},
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	first.SystemCharter = path
	firstCtx, down := context.WithCancel(t.Context())
	first.Start(firstCtx)
	firstHTTP := httptest.NewServer(first.Handler())
	review := `{
		name: review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`
	if resp := installAs(t, "alice@example.com", firstHTTP.URL, review); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice's install: %s %s", resp.Status, readBody(t, resp))
	}
	by := func(srv *Server, name string) string {
		t.Helper()
		e, ok, err := srv.Store.Get(trigger.CharterRef(name))
		if err != nil || !ok {
			t.Fatalf("no record for %s (ok=%v err=%v)", name, ok, err)
		}
		if said := entity.Get(e.Payload, "by"); said != nil {
			t.Fatalf("%s's entity says who installed it (%v): that is its owner record's to say", name, entity.ToObj(e.Payload))
		}
		return srv.Eng.OwnerOf(name)
	}
	if got := by(first, "review"); got != alice {
		t.Fatalf("review is %q's, want %q", got, alice)
	}
	if got := by(first, "system-watch"); got != entity.SystemCharter {
		t.Fatalf("the system charter's rule is %q's, want %q", got, entity.SystemCharter)
	}
	firstHTTP.Close()
	down()
	first.stopAllIOs()

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	ts := httptest.NewServer(second.Handler())
	defer ts.Close()
	if got := by(second, "review"); got != alice {
		t.Fatalf("after a restart review is %q's, want %q", got, alice)
	}

	// A file that changes review and then carries a rule that does nothing is refused whole, and
	// review is put back as alice's: its record, its owner, and the engine's account of it.
	resp := installAs(t, "alice@example.com", ts.URL, `[
		{name: review, condition: {subject: {path: "demo.pr.*", value: {stage: open}}}, action: {patch: {stage: looked}}},
		{name: broken, condition: {subject: {path: "demo.pr.*"}}, action: {}},
	]`)
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a file carrying a rule that does nothing installed")
	}
	resp.Body.Close()
	if got := by(second, "review"); got != alice {
		t.Fatalf("after the refused re-apply review is %q's, want %q", got, alice)
	}
	if got := owner(t, second, trigger.CharterRef("review")); got != alice {
		t.Fatalf("after the refused re-apply review's record is owned by %q, want %q", got, alice)
	}
}
