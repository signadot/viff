package versed

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// The engine removes a rule through its own store, which is the system's. So whether the
// REQUESTER may is asked by the engine, before anything is touched: asked nowhere, a stranger
// deletes anybody's rule — the base charter's included — and its owner record with it, and the
// name is theirs to install; asked only of the write that puts a replacement down, a refused
// POST has already removed the rule it is refused over.

func deleteAs(t *testing.T, as, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Verse-Principal", as)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const alicesRule = `{name: alices, condition: {subject: {path: "demo.al.*", value: {stage: open}}}, action: {patch: {stage: seen}}}`

func TestARuleIsRemovedAndReplacedByItsOwnerAlone(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	alice, mallory := "alice@example.com", "mallory@example.com"
	resp := installAs(t, alice, ts.URL, alicesRule)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice's install: %s", resp.Status)
	}
	stands := func(name, want string) {
		t.Helper()
		if _, ok := srv.Eng.Rule(name); !ok {
			t.Fatalf("the engine no longer holds %s", name)
		}
		if _, ok, _ := srv.Store.Get(trigger.CharterRef(name)); !ok {
			t.Fatalf("%s is gone from the store", name)
		}
		if got := owner(t, srv, trigger.CharterRef(name)); got != want {
			t.Fatalf("%s is %q's, want %q", name, got, want)
		}
	}

	// A stranger's removal, of alice's rule and of the system's.
	for _, name := range []string{"alices", "calendar"} {
		resp := deleteAs(t, mallory, ts.URL+"/charter/"+name)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("mallory's DELETE /charter/%s: %s, want 403", name, resp.Status)
		}
	}
	stands("alices", "user:"+alice)
	if _, ok := srv.Eng.Rule("calendar"); !ok {
		t.Error("the base charter's calendar rule is gone")
	}

	// A stranger's REPLACEMENT is refused with the rule still standing, so a second try is
	// refused too rather than finding the name free.
	for i := 0; i < 2; i++ {
		resp := installAs(t, mallory, ts.URL, alicesRule)
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			t.Fatalf("try %d: mallory installed over alice's rule: %s", i, resp.Status)
		}
		stands("alices", "user:"+alice)
	}

	// Its owner replaces it, and removes it.
	resp = installAs(t, alice, ts.URL, alicesRule)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice re-applying her own rule: %s", resp.Status)
	}
	resp = deleteAs(t, alice, ts.URL+"/charter/alices")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice removing her own rule: %s", resp.Status)
	}
	if ok, _ := entity.Holds(srv.Store, "user:"+mallory, entity.ReadWrite, trigger.CharterRef("calendar")); ok {
		t.Error("mallory holds read-write on the system's rule")
	}
}

// A source's removal stops its reflector, and the write that removes the registration comes
// last — so checked only there, a stranger's DELETE is refused with the reflector already
// stopped: installed, not running, its hints answered 503. It is asked first, and answers 403.
func TestAStrangersRemovalLeavesASourceRunning(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	dir := repoWithCommit(t)
	if resp := postTony(t, ts.URL+"/iods", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	t.Cleanup(srv.stopAllIOs)
	running := func() bool {
		srv.srcMu.Lock()
		defer srv.srcMu.Unlock()
		return len(srv.ioRuns) == 1
	}
	if !running() {
		t.Fatal("the fixture is wrong: the source is not running")
	}
	for _, q := range []string{"?keep=true", "?forget=true"} {
		resp := deleteAs(t, "mallory@example.com", ts.URL+"/iods/local"+q)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("mallory's DELETE%s: %s, want 403", q, resp.Status)
		}
		if !running() {
			t.Fatalf("mallory's refused DELETE%s stopped the reflector", q)
		}
	}
}

// A NAME THE SYSTEM INSTALLS AT IS THE SYSTEM'S, whoever got there first. A record cannot be
// put on a name nothing stands at in verse's half, so the way to be there first is to install a
// rule of that name — and a claim that leaves a standing record alone would leave the system's
// rule somebody else's: the system's content, firing as them, and suspendable by nobody, since
// it says the system installed it.
func TestANameTheSystemInstallsAtIsNotKeptBySomebodyWhoTookItFirst(t *testing.T) {
	store := entitytest.Store(t)
	srv := NewWithStore(ScriptedBuilder, store)
	mallory, rule := "user:mallory@example.com", trigger.CharterRef("calendar")
	if err := entity.Claim(entity.As(srv.Store, mallory), rule, mallory); !errors.Is(err, entity.ErrForbidden) {
		t.Fatalf("mallory reserved a rule's name with nothing standing at it: %v", err)
	}
	var squat trigger.Trigger
	if err := codec.Unmarshal([]byte(`{name: calendar, condition: {subject: {path: "demo.m.*", value: {stage: open}}}, action: {patch: {stage: hers}}}`), &squat, codec.Tony); err != nil {
		t.Fatal(err)
	}
	if err := srv.Eng.InstallTriggerAs(mallory, squat); err != nil {
		t.Fatalf("the fixture is wrong: mallory cannot install a rule named calendar: %v", err)
	}
	if got := owner(t, srv, rule); got != mallory {
		t.Fatalf("the fixture is wrong: the rule is %q's", got)
	}
	srv.Start(t.Context())
	if _, ok := srv.Eng.Rule("calendar"); !ok {
		t.Fatal("the base charter's calendar rule did not install")
	}
	if got := owner(t, srv, rule); !entity.IsSystem(got) {
		t.Fatalf("the system's rule is %q's", got)
	}
	if got := srv.Eng.OwnerOf("calendar"); !entity.IsSystem(got) {
		t.Errorf("the system's rule says %q installed it", got)
	}
	if ok, _ := entity.Holds(srv.Store, mallory, entity.ReadWrite, rule); ok {
		t.Error("mallory holds read-write on the system's rule")
	}
	if ok, _ := entity.Holds(srv.Store, entity.Via(rule), entity.OwnerLevel, entity.PermsRef(rule)); !ok {
		t.Error("the system's rule does not fire as the system")
	}
}

// A schedule's timer is this process's and its record is its installer's, so the record goes
// first: a stranger's removal is refused with the schedule still ticking — the base charter's
// own included, which is the calendar every rule reads.
func TestAStrangersRemovalLeavesAScheduleTicking(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if resp := installAs(t, "alice@example.com", ts.URL, `[{name: hourly-mine, every: 1h}]`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	armed := func(id string) bool {
		srv.clocks.mu.Lock()
		defer srv.clocks.mu.Unlock()
		return srv.clocks.timers[id] != nil
	}
	for _, id := range []string{"hourly-mine", "minutely"} {
		if !armed(id) {
			t.Fatalf("the fixture is wrong: %s is not armed", id)
		}
		resp := deleteAs(t, "mallory@example.com", ts.URL+"/charter/"+id)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("mallory's DELETE /charter/%s: %s, want 403", id, resp.Status)
		}
		if !armed(id) {
			t.Errorf("mallory's refused removal stopped %s", id)
		}
	}
	resp := deleteAs(t, "alice@example.com", ts.URL+"/charter/hourly-mine")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || armed("hourly-mine") {
		t.Errorf("alice removing her own schedule: %s, still armed %v", resp.Status, armed("hourly-mine"))
	}
}

// A REPLACEMENT THAT IS REFUSED LEAVES WHAT STOOD, and one that lands does not move whose the
// rule is. A rule's spec is stored raw, so writing over it replaces it whole: nothing is deleted
// first, so the owner record and the grants on it stand through an edit, and a co-owner who
// edits a rule has not become its owner — it goes on firing as whoever installed it.
func TestReplacingARuleKeepsItsOwnerAndARefusedOneKeepsTheRule(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	alice, rule := "alice@example.com", trigger.CharterRef("alices")
	if resp := installAs(t, alice, ts.URL, alicesRule); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s", resp.Status)
	}
	if err := entity.AddGrant(srv.Store, rule, entity.Grant{At: rule.Segs(), To: map[entity.Level][]string{entity.Read: {"user:bob@example.com"}}}); err != nil {
		t.Fatal(err)
	}
	// Her own mistake: the action does nothing.
	resp := installAs(t, alice, ts.URL, `{name: alices, condition: {subject: {path: "demo.al.*", value: {stage: open}}}, action: {}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a rule whose action does nothing: %s, want 400", resp.Status)
	}
	if _, ok := srv.Eng.Rule("alices"); !ok {
		t.Fatal("a refused replacement took the rule out of the engine")
	}
	if _, ok, _ := srv.Store.Get(rule); !ok {
		t.Fatal("a refused replacement removed the rule from the store")
	}
	// The root's owner co-owns it, and edits it.
	resp = installAs(t, AdminPrincipal, ts.URL, `{name: alices, condition: {subject: {path: "demo.al.*", value: {stage: open}}}, action: {patch: {stage: reviewed}}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the admin identity editing a rule it co-owns: %s", resp.Status)
	}
	if got := owner(t, srv, rule); got != "user:"+alice {
		t.Errorf("after a co-owner's edit the rule is %q's, want alice's still", got)
	}
	if got := srv.Eng.OwnerOf("alices"); got != "user:"+alice {
		t.Errorf("after a co-owner's edit the rule was installed by %q, want alice still", got)
	}
	if ok, _ := entity.Holds(srv.Store, "user:bob@example.com", entity.Read, rule); !ok {
		t.Error("the grant on the rule's record did not survive the edit")
	}
	e, _, _ := srv.Store.Get(rule)
	if spec := entity.Get(e.Payload, "spec"); entity.Str(entity.Get(entity.Get(spec, "action"), "patch"), "stage") != "reviewed" {
		t.Errorf("the edit did not land: %v", spec)
	}
}

// A REFUSED FILE PUTS BACK WHAT STOOD, WHOSE IT STOOD. The undo is the engine's own act, and an
// install by nobody is the system's: put back that way, the installer's own level would be
// taken for the system and their next edit of it refused.
func TestARefusedFileLeavesALevelItsInstallers(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	alice, level := "alice@example.com", trigger.StrataRefAt("", "lvl")
	file := func(n int, extra string) string {
		return fmt.Sprintf(`[{name: lvl, level: %d, match: [{path: "demo.lv"}, {path: "demo.other"}]},
			{name: lv-rule, stratum: lvl, condition: {subject: {path: "demo.lv.*", value: {stage: open}}}, action: {patch: {stage: seen}}}%s]`, n, extra)
	}
	if resp := installAs(t, alice, ts.URL, file(0, "")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install: %s %s", resp.Status, readBody(t, resp))
	}
	// Re-applied with the level moved and a third document that is refused.
	resp := installAs(t, alice, ts.URL, file(50, `, {name: bad, condition: {subject: {path: "demo.lv.*"}}, action: {}}`))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("a file with a rule that does nothing installed: %s", resp.Status)
	}
	if got := owner(t, srv, level); got != "user:"+alice {
		t.Fatalf("after a refused file alice's level is %q's", got)
	}
	if st, _ := srv.Eng.Stratum("lvl"); st.Level != 0 {
		t.Errorf("the level was not put back: %d", st.Level)
	}
	// …and it is still hers to edit: one match fewer, which the store holds as written.
	resp = installAs(t, alice, ts.URL, `{name: lvl, level: 7, match: [{path: "demo.lv"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice editing her own level: %s", resp.Status)
	}
	e, _, _ := srv.Store.Get(level)
	if spec := fmt.Sprint(entity.ToObj(e.Payload)["spec"]); !strings.Contains(spec, "demo.lv") || strings.Contains(spec, "demo.other") {
		t.Errorf("the level written over the old one kept the match it lost: %s", spec)
	}
	// A stranger's replacement is a refusal, and says so as one.
	resp = installAs(t, "mallory@example.com", ts.URL, `{name: lvl, level: 9, match: [{path: "demo.lv"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("mallory replacing alice's level: %s, want 403", resp.Status)
	}
}

// A REFUSED FILE UNDOES A TAKE WITH EVERYTHING ELSE IN IT. The system's charter names a rule
// somebody already has, and takes the name — its record with it, which is how the system's rule
// fires as the system. A later declaration in the file is refused, and the undo puts back what
// stood: the person's rule, AND its record. Content alone would leave their rule standing
// under the system's record, firing as the system into places they hold nothing on.
func TestARefusedSystemFileGivesBackTheNameItTook(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	alice, bob := "alice@example.com", "user:bob@example.com"
	for _, file := range []string{
		`{name: review, condition: {subject: {path: "demo.rv.*", value: {stage: open}}}, action: {patch: {stage: alices}}}`,
		`{name: lvl, level: 0, match: [{path: "demo.rv"}]}`,
	} {
		if resp := installAs(t, alice, ts.URL, file); resp.StatusCode != http.StatusCreated {
			t.Fatalf("alice's install: %s %s", resp.Status, readBody(t, resp))
		}
	}
	rule, level := trigger.CharterRef("review"), trigger.StrataRefAt("", "lvl")
	// A closed place of bob's, which alice and her rule hold nothing on.
	vault := entity.RefAt("vault")
	if err := entity.Claim(entity.As(srv.Store, bob), vault, bob); err != nil {
		t.Fatal(err)
	}
	if err := entity.SetGrants(srv.Store, vault, nil); err != nil {
		t.Fatal(err)
	}

	srv.installAsSystem("a system charter that names what alice has", []byte(`[
		{name: lvl, level: 3, match: [{path: "demo.rv"}]},
		{name: review, condition: {subject: {path: "demo.rv.*", value: {stage: open}}}, action: {patch: {stage: systems}}},
		{name: refused, condition: {subject: {path: "demo.rv.*"}}, action: {}},
	]`))

	if _, ok := srv.Eng.Rule("refused"); ok {
		t.Fatal("the fixture is wrong: the file was not refused")
	}
	for what, ref := range map[string]entity.Ref{"rule": rule, "level": level} {
		if got := owner(t, srv, ref); got != "user:"+alice {
			t.Errorf("after the refused file alice's %s is %q's", what, got)
		}
	}
	if got := srv.Eng.OwnerOf("review"); got != "user:"+alice {
		t.Errorf("after the refused file the rule says %q installed it", got)
	}
	if ok, _ := entity.Holds(srv.Store, entity.Via(rule), entity.Read, vault); ok {
		t.Error("alice's rule reads bob's closed place: it acts as the system")
	}
	e, _, _ := srv.Store.Get(rule)
	if got := entity.Str(entity.Get(entity.Get(entity.Get(e.Payload, "spec"), "action"), "patch"), "stage"); got != "alices" {
		t.Errorf("the rule standing patches stage to %q, want alice's", got)
	}
	// And it is still hers to edit, the level too.
	for _, file := range []string{
		`{name: review, condition: {subject: {path: "demo.rv.*", value: {stage: open}}}, action: {patch: {stage: again}}}`,
		`{name: lvl, level: 1, match: [{path: "demo.rv"}]}`,
	} {
		resp := installAs(t, alice, ts.URL, file)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("alice editing what is hers after the refused file: %s", resp.Status)
		}
	}
}
