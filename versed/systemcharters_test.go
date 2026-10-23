package versed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/system/repocharters"
	"github.com/signadot/verse/trigger"
)

// A charter handed to the server as text is installed as the system once the sources are
// restored, since its outboxes send to places the iods read, or, refused then for want of them, as
// soon as the iods are registered: the repo-charters reconciler
// `verse up` renders from the config is one, and its rules are the system's, so they fire as the
// system and their asks reach whom the charter names.
func TestSystemChartersAreInstalledAsTheSystemAfterTheIOs(t *testing.T) {
	text, err := repocharters.Render(repocharters.Params{Repo: "r", GitHub: "acme/r", InstallAt: "repos.r", Answerers: []string{"user:*"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := testServer(t)
	srv.SystemCharters = []SystemCharter{{Name: "repo-charters r", Text: text}}
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// With no io registered, the text was refused at start and nothing stands: the outboxes
	// name places no io reads.
	if reconcilerStands(srv) {
		t.Fatal("the reconciler stood before any io was registered")
	}
	for _, io := range []map[string]any{
		{"name": "git", "root": "git", "url": "http://git-iod:9801", "owns": []any{map[string]any{"at": "git.r"}},
			"out": map[string]any{"places": []any{"git.r.out"}, "as": "agent:git-iod"}},
		{"name": "github", "root": "github", "url": "http://github-iod:9801", "owns": []any{map[string]any{"at": "github.acme.r"}},
			"out": map[string]any{"places": []any{"github.acme.r.out"}, "as": "agent:github-iod"}},
	} {
		resp := installIO(t, ts, "user:"+tester, io)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("installing the %s io = %s", io["name"], resp.Status)
		}
	}
	// Registering them is what it was waiting for: installed then, with no restart (a wipe, then
	// repopulate).
	if !reconcilerStands(srv) {
		t.Fatal("the rendered reconciler was not installed as a package once the iods stood")
	}
	for _, eng := range srv.packageEngines() {
		if _, ok := eng.Trigger("propose"); !ok {
			continue
		}
		for _, rule := range []string{"main", "propose", "install", "installed", "install-failed"} {
			if got := eng.OwnerOf(rule); got != entity.SystemCharter {
				t.Errorf("%s is %q's, want the system's", rule, got)
			}
		}
	}
}

// reconcilerStands reports whether the reconciler stands as a package.
func reconcilerStands(srv *Server) bool {
	for _, eng := range srv.packageEngines() {
		if _, ok := eng.Trigger("propose"); ok {
			return true
		}
	}
	return false
}

// recordGets counts the reads of one record a server makes through it.
type recordGets struct {
	entity.Store
	of entity.Ref
	n  int
}

func (r *recordGets) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if ref == r.of {
		r.n++
	}
	return r.Store.Get(ref)
}

// packageFirings is how many times the packages' rules have acted since this server started.
func packageFirings(srv *Server) int {
	n := 0
	for _, eng := range srv.packageEngines() {
		n += eng.Fired()
	}
	return n
}

// A RESTART REPLAYS A SYSTEM CHARTER ONCE, in the restore, however often the config hands it
// over unchanged. Applying the same text over what the restore brought back replayed every rule
// a second time: on staging each repository's charter, eight minutes before the verse was ready.
// A changed text still installs, and replays what it changed.
func TestAnUnchangedSystemCharterIsNotReplayedAtARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	charter := func(stamp string) []SystemCharter {
		return []SystemCharter{{Name: "notes", Text: []byte(`at: notes
rules: [{name: mark,
  condition: {subject: {path: "notes.item.*"}},
  action: {patch: {seen: "` + stamp + `"}}}]
`)}}
	}
	// start stands a server up over the store with the texts, lets the start's firings settle,
	// and answers how many there were and how often the rule's perms record was read.
	start := func(texts []SystemCharter) (*Server, int, int) {
		t.Helper()
		rg := &recordGets{Store: entitytest.StoreAt(t, addr), of: entity.PermsRef(trigger.CharterRefAt("notes", "mark"))}
		srv := NewWithStore(ScriptedBuilder, rg)
		srv.SystemCharters = texts
		rg.n = 0 // the start is what is counted, not the migrations building the server reads through
		ctx, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)
		srv.Start(ctx)
		time.Sleep(300 * time.Millisecond)
		return srv, packageFirings(srv), rg.n
	}

	first, _, _ := start(charter("a"))
	for _, id := range []string{"1", "2", "3"} {
		entitytest.Set(t, first.Store, entity.RefAt("notes", "item", id), entity.Obj{"n": id}.Payload())
	}
	waitUntil(t, "the rule to mark every item", func() bool { return packageFirings(first) >= 3 })

	_, fired, reads := start(charter("a"))
	if fired != 3 {
		t.Errorf("a restart with the text unchanged fired the rule %d times over 3 items, want 3: once in the restore", fired)
	}
	// Nor is the store asked about it: the record a reinstall would snapshot for its undo is
	// read only by an install that happens, and under a busy store each read is a second.
	if reads != 0 {
		t.Errorf("a restart with the text unchanged read the rule's record %d times, want 0", reads)
	}
	changed, _, _ := start(charter("b"))
	waitUntil(t, "a changed text to install and replay", func() bool {
		e, ok, _ := changed.Store.Get(entity.RefAt("notes", "item", "1"))
		return ok && entity.Str(e.Payload, "seen") == "b"
	})
}
