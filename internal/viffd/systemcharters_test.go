package viffd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.viff.dev/viff/internal/trigger/charter"
	"go.viff.dev/viff/pkg/runner/runnerd/runnerdtest"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/internal/repocharters"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A charter handed to the server as text is installed as the system once the sources are
// restored, since its outboxes send to places the iods read, or, refused then for want of them, as
// soon as the iods are registered: the repo-charters reconciler
// `viff up` renders from the config is one, and its rules are the system's, so they fire as the
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
	of     entity.Ref // the perms record read ahead of an install
	rule   entity.Ref // the rule's own record, written by an install or a rewrite
	n      int
	writes int
}

func (r *recordGets) Get(ref entity.Ref) (entity.Entity, bool, error) {
	if ref == r.of {
		r.n++
	}
	return r.Store.Get(ref)
}

// Write counts the writes at rule, the rule's own record: what an install or a rewrite of the
// rule's words writes, and nothing a restart with the text unchanged should.
func (r *recordGets) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref == r.rule && w.At.Access == "" {
		r.writes++
	}
	return r.Store.Write(w)
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
// a second time: on staging each repository's charter, eight minutes before the viff was ready.
// A changed text still installs, and replays what it changed.
func TestAnUnchangedSystemCharterIsNotReplayedAtARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	charters := func(stamp string) []SystemCharter {
		return []SystemCharter{{Name: "notes", Text: []byte(`at: notes
rules: [{name: mark,
  condition: {subject: {path: "notes.item.*"}},
  action: {patch: {seen: "` + stamp + `"}}}]
`)}}
	}
	// start stands a server up over the store with the texts, lets the start's firings settle,
	// and answers how many there were, how often the rule's perms record was read, and how often
	// the rule's record was written.
	start := func(texts []SystemCharter) (*Server, int, int, int) {
		t.Helper()
		rule := charter.RuleRef("notes", "mark")
		rg := &recordGets{Store: entitytest.StoreAt(t, addr), of: entity.PermsRef(rule), rule: rule}
		srv := serverWith(t, runnerdtest.Scripted, rg)
		srv.SystemCharters = texts
		rg.n, rg.writes = 0, 0 // the start is what is counted, not the migrations building the server reads through
		ctx, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)
		srv.Start(ctx)
		time.Sleep(300 * time.Millisecond)
		return srv, packageFirings(srv), rg.n, rg.writes
	}

	first, _, _, _ := start(charters("a"))
	for _, id := range []string{"1", "2", "3"} {
		entitytest.Set(t, first.Store, entity.RefAt("notes", "item", id), entity.Obj{"n": id}.Payload())
	}
	waitUntil(t, "the rule to mark every item", func() bool { return packageFirings(first) >= 3 })

	_, fired, reads, writes := start(charters("a"))
	if fired != 3 {
		t.Errorf("a restart with the text unchanged fired the rule %d times over 3 items, want 3: once in the restore", fired)
	}
	// Nor is the store asked about it: the record a reinstall would snapshot for its undo is
	// read only by an install that happens, and under a busy store each read is a second.
	if reads != 0 {
		t.Errorf("a restart with the text unchanged read the rule's record %d times, want 0", reads)
	}
	// Nor is the record written: the rule's words follow its text (sameText), and a text that
	// has not moved is the same words — as parsed from the config and as restored from the store.
	if writes != 0 {
		t.Errorf("a restart with the text unchanged wrote the rule's record %d times, want 0", writes)
	}
	changed, _, _, _ := start(charters("b"))
	waitUntil(t, "a changed text to install and replay", func() bool {
		e, ok, _ := changed.Store.Get(entity.RefAt("notes", "item", "1"))
		return ok && entity.Str(e.Payload, "seen") == "b"
	})
}

// A SYSTEM RULE'S WORDS FOLLOW ITS TEXT WITHOUT A REPLAY. The record is the rule as its author
// left it, comments included; a config whose only change is the comment above a rule changes what
// the record should say and nothing about what the rule does. The record follows the words, in the
// engine and in the store, and the rule fires for nothing.
func TestASystemRulesWordsFollowItsTextWithoutAReplay(t *testing.T) {
	addr := entitytest.Addr(t)
	charters := func(words string) []SystemCharter {
		return []SystemCharter{{Name: "notes", Text: []byte("at: notes\nrules:\n" + words + "\n" +
			"- name: mark\n  condition: {subject: {path: \"notes.item.*\"}}\n  action: {patch: {seen: a}}\n")}}
	}
	start := func(texts []SystemCharter) *Server {
		t.Helper()
		srv := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
		srv.SystemCharters = texts
		ctx, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)
		srv.Start(ctx)
		return srv
	}
	// words is the comment the engine holds above the rule, "" while the rule is not installed.
	words := func(srv *Server) string {
		for _, eng := range srv.packageEngines() {
			if r, ok := eng.Rule("mark"); ok {
				return strings.Join(r.Doc, "\n")
			}
		}
		return ""
	}
	first := start(charters("# the first words"))
	waitUntil(t, "the rule to install with its words", func() bool { return words(first) == "# the first words" })
	for _, id := range []string{"1", "2", "3"} {
		entitytest.Set(t, first.Store, entity.RefAt("notes", "item", id), entity.Obj{"n": id}.Payload())
	}
	waitUntil(t, "the rule to mark every item", func() bool { return packageFirings(first) >= 3 })

	second := start(charters("# the second words"))
	waitUntil(t, "the restore to replay the rule once", func() bool { return packageFirings(second) >= 3 })
	waitUntil(t, "the engine's rule to follow the words", func() bool { return words(second) == "# the second words" })
	if fired := packageFirings(second); fired != 3 {
		t.Errorf("a change to the words above the rule fired it %d times over 3 items, want 3: the restore's", fired)
	}
	rec, ok, err := second.Store.Get(charter.RuleRef("notes", "mark"))
	if err != nil || !ok {
		t.Fatalf("the rule's record: ok=%v err=%v", ok, err)
	}
	if !mentions(rec.Payload, "# the second words") {
		t.Errorf("the record does not carry the words the author left: %v", entity.ToObj(rec.Payload))
	}
}

// mentions reports whether any comment in the node, at any depth, carries the line.
func mentions(n *ir.Node, line string) bool {
	if n == nil {
		return false
	}
	if n.Comment != nil && mentions(n.Comment, line) {
		return true
	}
	for _, l := range n.Lines {
		if l == line {
			return true
		}
	}
	for _, f := range n.Fields {
		if mentions(f, line) {
			return true
		}
	}
	for _, v := range n.Values {
		if mentions(v, line) {
			return true
		}
	}
	return false
}

// A SYSTEM RULE INSTALLED OVER DATA THAT STANDS ACTS ON IT, reading what its condition reads. The
// rule acts as its record, and its record has its owner from the write that makes it: never a
// moment standing with none, armed and acting as nobody, its first firings refused and the rule
// left waiting for a change that never comes, as the repo-charters reconcilers were on staging
// when they were installed afresh over the data they left.
func TestASystemRuleInstalledOverDataThatStandsActsOnIt(t *testing.T) {
	addr := entitytest.Addr(t)
	start := func(texts []SystemCharter) *Server {
		t.Helper()
		// A store as slow to take a perms record as staging's: a record written apart from its
		// node, after it, lands after the engine has followed the node and armed the rule.
		store := &slowRecords{Store: entitytest.StoreAt(t, addr), of: entity.PermsRef(charter.RuleRef("notes", "mark"))}
		srv := serverWith(t, runnerdtest.Scripted, store)
		srv.SystemCharters = texts
		ctx, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)
		srv.Start(ctx)
		return srv
	}
	// The data first, with no charter: what a removed charter leaves behind.
	bare := start(nil)
	entitytest.Set(t, bare.Store, entity.RefAt("notes", "state"), entity.Obj{"flag": "up"}.Payload())
	for _, id := range []string{"1", "2", "3"} {
		entitytest.Set(t, bare.Store, entity.RefAt("notes", "item", id), entity.Obj{"n": id}.Payload())
	}
	srv := start([]SystemCharter{{Name: "notes", Text: []byte(`at: notes
rules:
- name: mark
  condition:
    subject:
      path: "notes.item.*"
      value: !let
        let:
        - flag: !get-path {at: [notes, state], path: flag}
        in: {n: !glob "*"}
  action: {patch: {seen: yes}}
`)}})
	waitUntil(t, "the rule to mark every item that stood before it", func() bool {
		for _, id := range []string{"1", "2", "3"} {
			e, ok, _ := srv.Store.Get(entity.RefAt("notes", "item", id))
			if !ok || entity.Str(e.Payload, "seen") != "yes" {
				return false
			}
		}
		return true
	})
}

// slowRecords is a store that takes a while to write one perms record, as a store across a
// network does any write: what the engine does in the meantime is what it does with the record
// not there.
type slowRecords struct {
	entity.Store
	of entity.Ref
}

func (s *slowRecords) Write(w entity.Write) (bool, int64, error) {
	if w.At.Ref == s.of {
		time.Sleep(300 * time.Millisecond)
	}
	return s.Store.Write(w)
}
