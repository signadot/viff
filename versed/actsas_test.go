package versed

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/schedule"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/trigger"
)

// A PLACE THAT ACTS IS INSTALLED AS SOMEBODY'S OR NOT AT ALL. A rule fires, a source reflects, a
// binding answers and a schedule ticks as the owner on the place's own record, and as nobody
// without one. The record is written after the place and only for a writer who holds `owner`
// there, so each of these used to leave a place that stood, listed as installed, and did
// nothing. The install is refused and nothing is left standing.

const carolP = "user:carol@example.com"

// lessThanOwner holds `read-write` beneath each registry out to carol and the right to create
// there to nobody: what an operator's added line does, where she then installs.
func lessThanOwner(t *testing.T, srv *Server, rows ...entity.Ref) {
	t.Helper()
	for _, row := range rows {
		if err := entity.SetGrants(srv.Store, row, []entity.Grant{
			{At: row.Segs(), Below: true, To: map[entity.Level][]string{entity.ReadWrite: {carolP}, entity.Read: {entity.Everyone}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnInstallerHoldingLessThanOwnerInstallsNothing(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	lessThanOwner(t, srv,
		entity.VerseRef(trigger.CharterSystem, trigger.CharterKind),
		entity.VerseRef(schedule.System), entity.VerseRef(source.System), entity.VerseRef(resolver.System))
	carol := "carol@example.com"
	dir := repoWithCommit(t)

	for _, c := range []struct {
		what string
		resp *http.Response
		at   entity.Ref
	}{
		{"a rule", installAs(t, carol, ts.URL, `{
			name: carols,
			condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
			action: {patch: {stage: seen}},
		}`), trigger.CharterRef("carols")},
		{"a schedule", installAs(t, carol, ts.URL, `{name: carols-hour, every: 1h}`),
			schedule.Ref(schedule.RecordID("", "carols-hour"))},
		{"a binding", postAs(t, carol, ts.URL+"/resolvers", "application/tony",
			`{name: carols-review, byRef: true, method: {driver: url, url: "https://x"}}`), resolver.Ref("carols-review")},
		{"a source", postAs(t, carol, ts.URL+"/sources", "application/tony",
			fmt.Sprintf(`{name: carols-repo, root: git, dir: %q}`, dir)), source.Ref("carols-repo")},
	} {
		body := bodyOf(t, c.resp)
		if c.resp.StatusCode != http.StatusConflict || !strings.Contains(body, "act as nobody") {
			t.Errorf("%s by somebody holding read-write there: %s %q, want 409 saying it would act as nobody",
				c.what, c.resp.Status, body)
		}
		if _, stands, _ := srv.Store.Get(c.at); stands {
			t.Errorf("%s was refused and still stands at %s", c.what, c.at)
		}
	}
	if _, had := srv.Eng.Rule("carols"); had {
		t.Error("the refused rule is in the engine")
	}
	srv.srcMu.Lock()
	n := len(srv.srcRuns)
	srv.srcMu.Unlock()
	if n != 0 {
		t.Errorf("%d reflectors running for a source that was refused", n)
	}
}

// deafMirror commits every write but the perms records it is told to lose, which it reports as
// the store not having answered: the window record() leaves open by design, since the next
// write closes it — and for a place that acts there is no next write.
type deafMirror struct {
	entity.Store
	lose *atomic.Bool
}

func (d deafMirror) Write(w entity.Write) (bool, int64, error) {
	if d.lose.Load() && w.At.Ref.Under(entity.VerseRoot, entity.PermsSystem, entity.PermsKind) {
		return false, 0, errors.New("the store did not answer")
	}
	return d.Store.Write(w)
}

func (d deafMirror) Scope(name string) entity.Store { return deafMirror{d.Store.Scope(name), d.lose} }

func TestAnInstallTheStoreDidNotRecordIsTakenBackOut(t *testing.T) {
	lose := &atomic.Bool{}
	srv := NewWithStore(ScriptedBuilder, deafMirror{entitytest.Store(t), lose})
	t.Cleanup(srv.stopAllSources)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const rule = `{
		name: watched,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen}},
	}`

	lose.Store(true)
	resp := installAs(t, "alice@example.com", ts.URL, rule)
	if body := bodyOf(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "act as nobody") {
		t.Fatalf("an install whose record was lost: %s %q, want 409", resp.Status, body)
	}
	if _, stands, _ := srv.Store.Get(trigger.CharterRef("watched")); stands {
		t.Error("the rule stands, with no record to say whose")
	}
	if _, had := srv.Eng.Rule("watched"); had {
		t.Error("the rule is in the engine")
	}
	resp = postAs(t, "alice@example.com", ts.URL+"/resolvers", "application/tony",
		`{name: reviewer, byRef: true, method: {driver: url, url: "https://x"}}`)
	if body := bodyOf(t, resp); resp.StatusCode != http.StatusConflict {
		t.Errorf("a binding whose record was lost: %s %q, want 409", resp.Status, body)
	}
	if _, stands, _ := srv.Store.Get(resolver.Ref("reviewer")); stands {
		t.Error("the binding stands, with no record to say whose")
	}

	// And with the store answering, the same installs land and act as her.
	lose.Store(false)
	if resp := installAs(t, "alice@example.com", ts.URL, rule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the install, with the store answering: %s %s", resp.Status, bodyOf(t, resp))
	}
	if who, err := entity.ActsAs(srv.Store, trigger.CharterRef("watched")); err != nil || who != "user:alice@example.com" {
		t.Errorf("the rule acts as %q (%v), want alice", who, err)
	}
	// What the system installs acts as the system, by its own record.
	if who, err := entity.ActsAs(srv.Store, trigger.CharterRef("clock")); err != nil || !entity.IsSystem(who) {
		t.Errorf("the base charter's clock acts as %q (%v), want the system", who, err)
	}
}

// A store can hold one an install could not have left: written by hand, or by a build from
// before a place needed a record to act. It acts as nobody, which is safe and silent, so the
// start names it — and names nothing the system or a person installed.
func TestTheStartNamesWhatActsAsNobody(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if resp := installAs(t, "alice@example.com", ts.URL, `{
		name: alices,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: seen}},
	}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s %s", resp.Status, bodyOf(t, resp))
	}
	// By hand, as the system, which records nothing.
	orphan := resolver.Ref("by-hand")
	entitytest.Set(t, srv.Store, orphan, entity.Obj{
		"name": "by-hand", "byRef": true, "method": entity.Obj{"driver": "url", "url": "https://x"},
	}.Payload())

	got, err := actingAsNobody(srv.Store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != orphan {
		t.Fatalf("acting as nobody: %v, want the binding written by hand and nothing else "+
			"(not alice's rule, not the base charter's)", got)
	}
}
