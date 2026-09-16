package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// A rule's record says who installed it, and keeps saying so: across a restart, through a
// charter file refused and put back, and as `system:charter` for the charter the deployment
// names. Perms cannot tell the system's rules from a person's whose owner write failed; the
// install can, and this is where it writes it down.
func TestARuleRecordCarriesItsInstaller(t *testing.T) {
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
	first := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
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
		return entity.Str(e.Payload, "by")
	}
	if got := by(first, "review"); got != alice {
		t.Fatalf("review's record says by %q, want %q", got, alice)
	}
	if got := by(first, "system-watch"); got != entity.SystemCharter {
		t.Fatalf("the system charter's rule says by %q, want %q", got, entity.SystemCharter)
	}
	firstHTTP.Close()
	down()
	first.stopAllSources()

	second := NewWithStore(ScriptedBuilder, entitytest.StoreAt(t, addr))
	second.Start(t.Context())
	ts := httptest.NewServer(second.Handler())
	defer ts.Close()
	if got := second.Eng.InstalledBy("review"); got != alice {
		t.Fatalf("after a restart the engine has review installed by %q, want %q", got, alice)
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
		t.Fatalf("after the refused re-apply review's record says by %q, want %q", got, alice)
	}
	if got := owner(t, second, trigger.CharterRef("review")); got != alice {
		t.Fatalf("after the refused re-apply review's record is owned by %q, want %q", got, alice)
	}
	if got := second.Eng.InstalledBy("review"); got != alice {
		t.Fatalf("after the refused re-apply the engine has review by %q, want %q", got, alice)
	}
}
