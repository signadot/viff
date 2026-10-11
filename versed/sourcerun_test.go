package versed

import (
	"context"
	"errors"
	"fmt"
	"github.com/signadot/verse/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	verseio "github.com/signadot/verse/io"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repoWithCommit is a repo with something in it, so a reflection has entities to write.
func repoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initRepo(t, dir)
	gitIn(t, dir, "commit", "--allow-empty", "-m", "first")
	return dir
}

func waitForRef(t *testing.T, store entity.Reader, ref entity.Ref) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, _ := store.Get(ref); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", ref)
}

// An installed source REFLECTS, with no restart. A registration that installed and then did
// nothing until somebody bounced the server would be a silence an operator cannot debug.
func TestAnInstalledSourceReflects(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	if resp := postTony(t, ts.URL+"/io", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s: %s", resp.Status, bodyOf(t, resp))
	}
	waitForRef(t, srv.Store, entity.RefAt(verseio.GitRoot, baseOf(dir), "head"))

	// …and removing it STOPS the reflector. A reflector outliving its registration is a writer
	// on a slice nothing claims — the second writer the install check exists to prevent,
	// arriving through the back door.
	// --forget, because by now it HAS reflected something and a bare remove is refused until
	// told which (the fork is TestRemovingASourceForksOnWhatItReflected); what is being
	// asserted here is that either answer stops the reflector.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/io/local?forget=true", nil)
	req.Header.Set(api.PrincipalHeader, tester)
	resp, err := sendAsTester(req)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove = %s: %s", resp.Status, bodyOf(t, resp))
	}
	srv.srcMu.Lock()
	n := len(srv.ioRuns)
	srv.srcMu.Unlock()
	if n != 0 {
		t.Fatalf("%d reflector(s) still running after the registration went away", n)
	}
}

// A restart brings back the program it went down with. The registration is durable, so the
// reflector has to be reconstructed from it — there is nothing else to reconstruct it from.
func TestSourcesAreRestoredOnRestart(t *testing.T) {
	requireGit(t)
	store := entitytest.Store(t)
	dir := repoWithCommit(t)

	first := NewWithStore(ScriptedBuilder, store)
	ts := httptest.NewServer(first.Handler())
	if resp := postTony(t, ts.URL+"/io", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	head := entity.RefAt(verseio.GitRoot, baseOf(dir), "head")
	waitForRef(t, store, head)
	ts.Close()
	first.stopSource(verseio.Ref("local"))

	// The world moves while verse is down, which is the case a restart has to survive: a
	// level-triggered pass reads what IS, so a branch made in the gap is not a missed event.
	gitIn(t, dir, "branch", "while-down")

	// A new server over the same store: the registration is all it has.
	second := NewWithStore(ScriptedBuilder, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.ctx = ctx
	second.restoreSources()
	t.Cleanup(second.stopAllIOs)

	second.srcMu.Lock()
	n := len(second.ioRuns)
	second.srcMu.Unlock()
	if n != 1 {
		t.Fatalf("%d reflectors after restart, want 1", n)
	}
	// And it is really reflecting rather than merely running: the restored reflector's own
	// first pass picks up what happened while it was gone.
	waitForRef(t, store, entity.RefAt(verseio.GitRoot, baseOf(dir), "ref", "while-down"))
}

// What a pass writes about ITSELF, and what it deliberately does not.
//
// The anti-churn assertion is the one that matters and the one most likely to be broken later
// by somebody stamping a heartbeat here: a pass that changed nothing must leave the
// registration byte-identical, or every source writes to `sources:*` once per resync interval
// forever and anything watching that slice fires on an idle verse.
func TestAPassRecordsItselfWithoutChurn(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	dir := repoWithCommit(t)
	ref := verseio.Ref("local")
	e := verseio.Entity{Name: "local", Root: "git", Dir: dir}

	// As an install leaves it: written as its installer, so the registration is theirs — a
	// source reflects as whoever owns its registration, and one nobody owns reflects as nobody.
	entitytest.Set(t, asTester(srv), ref, entity.Obj{
		"name": "local", "root": "git", "dir": dir,
	}.Payload())
	if err := srv.startSource(e); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.stopSource(ref)

	// The first pass wrote the repo, so it stamps the cursor.
	waitForRef(t, srv.Store, entity.RefAt(verseio.GitRoot, baseOf(dir), "head"))
	deadline := time.Now().Add(5 * time.Second)
	var stamped string
	for time.Now().Before(deadline) && stamped == "" {
		reg, _, _ := srv.Store.Get(ref)
		stamped = entity.Str(reg.Payload, "lastReflect")
		time.Sleep(10 * time.Millisecond)
	}
	if stamped == "" {
		t.Fatal("a pass that wrote the slice should stamp lastReflect — it is the poll cursor")
	}

	// A quiet pass writes NOTHING to the registration.
	before, _, _ := srv.Store.Get(ref)
	rev := srv.Store.Rev()
	if err := srv.reconcileOnce(t, e); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	after, _, _ := srv.Store.Get(ref)
	if !entity.Equal(before.Payload, after.Payload) {
		t.Fatalf("a quiet pass rewrote the registration:\n before %v\n after  %v",
			entity.ToObj(before.Payload), entity.ToObj(after.Payload))
	}
	if srv.Store.Rev() != rev {
		t.Fatal("a quiet pass moved the store — nothing changed, so nothing should have")
	}
}

// A failure is recorded once and cleared by the recovery. Written on TRANSITION, so a source
// failing every minute writes once rather than every minute.
func TestLastErrorIsWrittenOnTransition(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("gone")
	// A url nothing answers: every pass fails the same way.
	e := verseio.Entity{Name: "gone", Root: "git", URL: "http://127.0.0.1:1"}
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "gone", "root": "git", "url": e.URL,
	}.Payload())

	run := &sourceRun{}
	fail := verseio.Pass{Err: errFor("unreachable")}
	srv.recordPass(t.Context(), srv.Store, ref, run, fail)
	reg, _, _ := srv.Store.Get(ref)
	if failureText(reg.Payload) == "" {
		t.Fatal("a failing pass should record why")
	}

	// The same failure again: no write, because nothing changed about it.
	rev := srv.Store.Rev()
	srv.recordPass(t.Context(), srv.Store, ref, run, fail)
	if srv.Store.Rev() != rev {
		t.Fatal("the same failure twice should write once — lastError is a transition")
	}

	// And the recovery CLEARS it, which is the half an error-only hook could never do: the
	// registration stops carrying an error rather than carrying an empty one.
	srv.recordPass(t.Context(), srv.Store, ref, run, verseio.Pass{})
	reg, _, _ = srv.Store.Get(ref)
	if _, ok := entity.Fields(reg.Payload)["lastError"]; ok {
		t.Fatalf("a recovery should remove lastError, got %v", entity.ToObj(reg.Payload))
	}
}

// baseOf is the repo id a `dir` registration gets: the directory's base name, which is what
// Git.AddRepo defaults to.
func baseOf(dir string) string { return filepath.Base(filepath.Clean(dir)) }

func errFor(s string) error { return errors.New(s) }

// reconcileOnce drives one pass through the same path Run does, so a test asserting what a pass
// writes is asserting about the real one.
func (s *Server) reconcileOnce(t *testing.T, e verseio.Entity) error {
	t.Helper()
	src, err := s.sourceFor(e)
	if err != nil {
		return err
	}
	ref := verseio.Ref(e.Name)
	run := &sourceRun{}
	rf := verseio.NewReflector(src, s.Store, 0)
	rf.OnPass(func(p verseio.Pass) { s.recordPass(t.Context(), s.Store, ref, run, p) })
	return rf.Reconcile(context.Background(), entity.Ref{})
}

// A pass that lands after the registration went away must not bring it back.
//
// This is a race a removal cannot win on its own: a write at a FIELD of an absent entity
// CREATES it, so a pass already in flight when the source was removed resurrects it as a stub
// carrying nothing but a timestamp — an installed-looking source with no url and no ownership,
// which a restart would then try to restore. It happened, in the CLI test, before the
// precondition existed.
//
// The precondition asks about `name` rather than about the entity, and that detail is the
// point: entity.Present() is a null pattern, and a tombstone IS a null value at the entity's
// own path, so a presence test on the ref is satisfied by the very tombstone it should notice.
func TestAPassAfterRemovalDoesNotResurrectTheRegistration(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("gone")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "gone", "root": "git", "url": "http://127.0.0.1:1",
	}.Payload())

	// Removed, exactly as removeIO does it.
	if _, _, err := srv.Store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// …and now a pass that was already in flight reports itself, both ways.
	run := &sourceRun{}
	srv.recordPass(t.Context(), srv.Store, ref, run, verseio.Pass{Err: errFor("unreachable")})
	srv.recordPass(t.Context(), srv.Store, ref, run, verseio.Pass{Wrote: 3})

	if e, ok, _ := srv.Store.Get(ref); ok {
		t.Fatalf("a pass after removal resurrected the registration as %v", entity.ToObj(e.Payload))
	}
}

// A SUCCEEDING pass reported after its reflector was stopped writes nothing.
//
// This is the half the precondition above cannot cover, and it is a different failure wearing
// the same clothes. `stillInstalled` is a compare-and-swap, and a compare-and-swap can only be
// ordered against other WRITES — a scope delete is not one. So the CAS reads `name` present,
// the overlay is dropped underneath it, and the patch lands anyway and re-creates the
// registration in a scope that no longer exists, as a stub carrying nothing but a timestamp.
// TestDeletingAScopeTakesItsSources caught it about one run in five.
//
// Nothing about such a pass is an error — it succeeded, moments before the stop — so the
// cancelled-pass guard does not see it either. The reflector's own context is the only thing
// that knows, and once it is done this run has no business writing anywhere.
func TestAPassReportedAfterTheStopWritesNothing(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("stopped")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "stopped", "root": "git", "url": "http://127.0.0.1:1",
	}.Payload())

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the reflector has been stopped; a pass that was already in flight now reports

	rev := srv.Store.Rev()
	run := &sourceRun{}
	srv.recordPass(ctx, srv.Store, ref, run, verseio.Pass{Wrote: 3})
	srv.recordPass(ctx, srv.Store, ref, run, verseio.Pass{Err: errFor("unreachable")})

	if srv.Store.Rev() != rev {
		reg, _, _ := srv.Store.Get(ref)
		t.Fatalf("a pass reported after the stop wrote to the store: %v", entity.ToObj(reg.Payload))
	}
}

// A hint reaches the reflector and reconciles the slice it names — the low-latency half, which
// is the whole reason a source exists rather than a cron job.
//
// The periodic net is a minute away here, so anything that lands promptly landed because of the
// hint.
func TestAHintReconcilesTheSliceItNames(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	if resp := postTony(t, ts.URL+"/io", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	waitForRef(t, srv.Store, entity.RefAt(verseio.GitRoot, baseOf(dir), "head"))

	gitIn(t, dir, "branch", "hinted")
	hint := ts.URL + "/io/local/hint?ref=" +
		url.QueryEscape(verseio.RepoRef(baseOf(dir)).String())
	resp, err := ts.Client().Post(hint, "", nil)
	if err != nil {
		t.Fatalf("hint: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hint = %s, want 204", resp.Status)
	}
	waitForRef(t, srv.Store, entity.RefAt(verseio.GitRoot, baseOf(dir), "ref", "hinted"))

	// A hint naming nothing is a full resync, which is what a delivery that could not say which
	// slice asks for.
	gitIn(t, dir, "branch", "resynced")
	if resp, err := ts.Client().Post(ts.URL+"/io/local/hint", "", nil); err != nil ||
		resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bare hint = %v (%v)", resp.Status, err)
	}
	waitForRef(t, srv.Store, entity.RefAt(verseio.GitRoot, baseOf(dir), "ref", "resynced"))
}

// What a hint may NOT do, and what it must not do silently.
func TestHintRefusals(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A hint for a registration nobody installed is a 404, not a quiet 204. A sender told
	// nothing goes on believing its changes are landing.
	resp, err := ts.Client().Post(ts.URL+"/io/nope/hint", "", nil)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("hint at an unknown source = %v (%v), want 404", resp.Status, err)
	}

	dir := repoWithCommit(t)
	if resp := postTony(t, ts.URL+"/io", map[string]any{
		"root": "git", "name": "local", "dir": dir,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	// A ref that is not one is a 400 rather than a resync: a delivery that meant something
	// specific must not silently become "reflect everything".
	bad := ts.URL + "/io/local/hint?at=" + url.QueryEscape("a..b")
	if resp, err := ts.Client().Post(bad, "", nil); err != nil ||
		resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hint with a malformed ref = %v (%v), want 400", resp.Status, err)
	}

	// Installed but not running: said out loud, because a swallowed hint looks exactly like one
	// that worked.
	srv.stopSource(verseio.Ref("local"))
	if resp, err := ts.Client().Post(ts.URL+"/io/local/hint", "", nil); err != nil ||
		resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("hint at a stopped source = %v (%v), want 503", resp.Status, err)
	}
}

// removeIO answers the question a delete cannot: what happens to what it reflected.
//
// Tombstoning always is right about the common case and wrong about the one that will happen —
// moving a repo to another host is a remove and an add of the same physical truth, and
// tombstoning it fires every branch-deleted trigger in the verse and then every branch-created
// one behind it. So a removal with a non-empty reflected slice is refused until told which.
func TestRemovingASourceForksOnWhatItReflected(t *testing.T) {
	requireGit(t)
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := repoWithCommit(t)
	install := func(name string) {
		t.Helper()
		if resp := postTony(t, ts.URL+"/io", map[string]any{
			"root": "git", "name": name, "dir": dir,
		}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("install %s = %s: %s", name, resp.Status, bodyOf(t, resp))
		}
	}
	remove := func(name, flag string) *http.Response {
		t.Helper()
		u := ts.URL + "/io/" + name
		if flag != "" {
			u += "?" + flag + "=true"
		}
		req, _ := http.NewRequest(http.MethodDelete, u, nil)
		req.Header.Set(api.PrincipalHeader, tester)
		resp, err := sendAsTester(req)
		if err != nil {
			t.Fatalf("remove: %v", err)
		}
		return resp
	}

	install("local")
	head := entity.RefAt(verseio.GitRoot, baseOf(dir), "head")
	waitForRef(t, srv.Store, head)

	// Bare: refused, and the message names both flags, because neither default survives
	// contact with the case it is wrong about.
	resp := remove("local", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("bare remove with a reflected slice = %s, want 409", resp.Status)
	}
	body := bodyOf(t, resp)
	for _, want := range []string{"--forget", "--keep"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the refusal should offer %s, got: %s", want, body)
		}
	}

	// --keep leaves the last known state, now frozen: the repo is moving, not vanishing.
	if resp := remove("local", "keep"); resp.StatusCode != http.StatusOK {
		t.Fatalf("--keep = %s", resp.Status)
	}
	if _, ok, _ := srv.Store.Get(head); !ok {
		t.Fatal("--keep tombstoned the slice; it is supposed to leave it standing")
	}

	// …and then --forget, on a re-install of the same truth, does take it away.
	install("local")
	waitForRef(t, srv.Store, head)
	if resp := remove("local", "forget"); resp.StatusCode != http.StatusOK {
		t.Fatalf("--forget = %s", resp.Status)
	}
	if _, ok, _ := srv.Store.Get(head); ok {
		t.Fatal("--forget left the slice standing")
	}
}

// A removal with nothing reflected needs no flag, there being nothing to be wrong about.
func TestRemovingASourceThatReflectedNothingNeedsNoFlag(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Unreachable, so it installs (with a warning) and reflects nothing.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if resp := postTony(t, ts.URL+"/io", map[string]any{
		"root": "git", "name": "never", "url": dead.URL,
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("install = %s", resp.Status)
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/io/never", nil)
	req.Header.Set(api.PrincipalHeader, tester)
	resp, err := sendAsTester(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("remove = %v (%v), want 200 with no flag", resp.Status, err)
	}
}

// A --forget is bounded by what the install PROVED this source was the single writer of, and by
// nothing else. Deleting a neighbour's slice on the way out would be the disjointness check
// undone at the last moment.
func TestForgetTombstonesOnlyWhatThisSourceClaimed(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("a")
	e := verseio.Entity{Name: "a", Root: "git", URL: "http://x:1",
		Owns: []verseio.Own{{At: "git.A"}}}
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "a", "root": "git", "url": e.URL, "owns": e.Owns,
	}.Payload())

	mine := entity.RefAt("git", "A", "ref", "main")
	theirs := entity.RefAt("git", "B", "ref", "main")
	entitytest.Set(t, srv.Store, mine, entity.Obj{"repo": "A"}.Payload())
	entitytest.Set(t, srv.Store, theirs, entity.Obj{"repo": "B"}.Payload())

	got, err := reflectedBy(srv.Store, e)
	if err != nil {
		t.Fatal(err)
	}
	// A literal claim is its own place: the repository's node, whatever stands beneath it.
	if len(got) != 1 || got[0].Ref != entity.RefAt("git", "A") || got[0].Access != "" {
		t.Fatalf("reflectedBy = %v, want just the place git.A that holds %v", got, mine)
	}
}

// A source that owns a place INSIDE a shared thing is removed from that place, and the thing —
// which other owners are still writing — stays. This is the disjointness check honoured at the
// last moment rather than undone by it: what a --forget deletes is what the install proved this
// source was the single writer of, which here is a node under the project rather than the
// project.
func TestForgetTakesTheOwnedPlaceAndLeavesTheSharedNode(t *testing.T) {
	srv := testServer(t)
	e := verseio.Entity{Name: "eng", Root: "linear", URL: "http://x:1",
		Owns: []verseio.Own{{At: "linear.project.*.owners.ENG"}}}
	entitytest.Set(t, srv.Store, verseio.Ref("eng"), entity.Obj{
		"name": "eng", "root": "linear", "url": e.URL, "owns": e.Owns,
	}.Payload())

	shared := entity.RefAt("linear", "project", "p1")
	entitytest.Set(t, srv.Store, shared, entity.Obj{"owners": map[string]any{
		"ENG": map[string]any{"name": "Migration"},
		"OPS": map[string]any{"name": "Migration"},
	}}.Payload())

	got, err := reflectedBy(srv.Store, e)
	if err != nil {
		t.Fatal(err)
	}
	place := entity.RefAt("linear", "project", "p1", "owners", "ENG")
	if len(got) != 1 || got[0].Ref != place {
		t.Fatalf("reflectedBy = %v, want ENG's place under %v and not the project itself", got, shared)
	}
	if want := `linear.project.p1.owners.ENG`; got[0].String() != want {
		t.Fatalf("the place is %s, want %s", got[0], want)
	}
	// And the removal writes there: ENG's place goes, the project and the other owner stand.
	if _, _, err := srv.Store.Write(entity.Write{At: got[0]}); err != nil {
		t.Fatal(err)
	}
	p, ok, err := srv.Store.Get(shared)
	if err != nil || !ok {
		t.Fatalf("the shared project went with ENG's place: ok=%v err=%v", ok, err)
	}
	owners := entity.Fields(entity.Get(p.Payload, "owners"))
	if _, still := owners["ENG"]; still {
		t.Fatalf("ENG's place survived a --forget: %v", owners)
	}
	if _, other := owners["OPS"]; !other {
		t.Fatalf("a --forget on ENG took OPS's place with it: %v", owners)
	}
}

// A clean shutdown is not a failure, and must not be recorded as one.
//
// Cancelling a reflector fails the pass in flight with context.Canceled. Written down, that
// would outlive the task: every source would come back from a clean stop carrying it as
// lastError, and an operator reading the listing after a restart would see a verse full of
// broken sources that are all fine.
func TestACleanShutdownIsNotRecordedAsAFailure(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("local")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "local", "root": "git", "url": "http://x:1",
	}.Payload())

	run := &sourceRun{}
	srv.recordPass(t.Context(), srv.Store, ref, run, verseio.Pass{
		Err: fmt.Errorf("reflect git: %w", context.Canceled),
	})
	reg, _, _ := srv.Store.Get(ref)
	if _, ok := entity.Fields(reg.Payload)["lastError"]; ok {
		t.Fatalf("a cancelled pass should record nothing, got %v", entity.ToObj(reg.Payload))
	}
}

// failWritesTo fails writes AT one path and passes everything else through — a store that is
// struggling exactly where the status write lands.
type failWritesTo struct {
	entity.Store
	at   string
	fail bool
}

func (f *failWritesTo) Write(w entity.Write) (bool, int64, error) {
	if f.fail && w.At.String() == f.at {
		return false, 0, errFor("context deadline exceeded")
	}
	return f.Store.Write(w)
}

// A CLEARING WRITE THAT FAILS IS RETRIED, rather than being remembered as done.
//
// lastError is written on transition, and the transition is measured against what this run
// believes is RECORDED. Advancing that belief before the store accepts the write means a
// failed write is never retried: the next pass compares equal, writes nothing, and the
// registration carries an error it recovered from for good.
//
// Staging did exactly this — recovered, failed to clear (`recording error: … context deadline
// exceeded`), and reported the source in error two passes later with the entity it had failed
// on present and correct.
func TestAFailedClearingWriteIsRetried(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("flaky")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "flaky", "root": "git", "url": "http://127.0.0.1:1",
	}.Payload())

	fs := &failWritesTo{Store: srv.Store, at: ref.At("lastError").String()}
	run := &sourceRun{}

	// A failure, recorded.
	srv.recordPass(t.Context(), fs, ref, run, verseio.Pass{Err: errFor("unreachable")})
	reg, _, _ := srv.Store.Get(ref)
	if failureText(reg.Payload) == "" {
		t.Fatal("a failing pass should record why")
	}

	// The recovery — but the clearing write fails.
	fs.fail = true
	srv.recordPass(t.Context(), fs, ref, run, verseio.Pass{})
	reg, _, _ = srv.Store.Get(ref)
	if failureText(reg.Payload) == "" {
		t.Fatal("the clearing write was supposed to fail; the fixture is not testing anything")
	}

	// The store recovers, and the NEXT pass must try again rather than believe it already did.
	fs.fail = false
	srv.recordPass(t.Context(), fs, ref, run, verseio.Pass{})
	reg, _, _ = srv.Store.Get(ref)
	if _, ok := entity.Fields(reg.Payload)["lastError"]; ok {
		t.Fatalf("lastError stuck after the store recovered: %v — a source reporting an error "+
			"it is not having is the one an operator acts on", entity.ToObj(reg.Payload))
	}
}

// failureText reads the message out of the lastError RECORD — {error, when} since it became a
// verseio.Failure, so that the timestamp can say when the failure began and a reader can tell an
// error that is happening from one the source recovered from long ago.
func failureText(p entity.Payload) string {
	n, ok := entity.Fields(p)["lastError"]
	if !ok || n == nil {
		return ""
	}
	return entity.Str(entity.Payload(n), "error")
}

// failureWhen is failureText's other half.
func failureWhen(p entity.Payload) string {
	n, ok := entity.Fields(p)["lastError"]
	if !ok || n == nil {
		return ""
	}
	return entity.Str(entity.Payload(n), "when")
}

// WHEN IS THE ONSET, not the last repetition. A source failing the same way every minute keeps
// the timestamp it started with, so the gap between it and lastPass says how long this has been
// going on. Refreshing it per pass would make every failure look like it just started, which is
// the reading that let a hours-old error pass for a live one.
func TestFailureWhenMarksTheOnsetAndNotEachRepeat(t *testing.T) {
	srv := testServer(t)
	clock := clockOf(t, srv)
	ref := verseio.Ref("onset")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "onset", "root": "git", "url": "http://127.0.0.1:1",
	}.Payload())
	run := &sourceRun{}
	fail := verseio.Pass{Err: errFor("unreachable")}

	srv.recordPass(t.Context(), srv.Store, ref, run, fail)
	reg, _, _ := srv.Store.Get(ref)
	first := failureWhen(reg.Payload)
	if first == "" {
		t.Fatal("a new failure should carry when it began")
	}

	// The same failure again, later. No write at all, so the onset cannot move.
	clock.Advance(2 * time.Second)
	srv.recordPass(t.Context(), srv.Store, ref, run, fail)
	reg, _, _ = srv.Store.Get(ref)
	if got := failureWhen(reg.Payload); got != first {
		t.Fatalf("when moved on a repeat: %s → %s; it marks the onset, not the latest pass", first, got)
	}

	// A DIFFERENT failure is a new failure, and takes a new onset.
	srv.recordPass(t.Context(), srv.Store, ref, run, verseio.Pass{Err: errFor("refused")})
	reg, _, _ = srv.Store.Get(ref)
	if got := failureWhen(reg.Payload); got == first {
		t.Fatalf("a different error kept the old onset %s — it is a new failure", first)
	}
	if got := failureText(reg.Payload); got == "" || got == "unreachable" {
		t.Fatalf("the new failure's text did not land: %q", got)
	}
}

// A RESTART MUST NOT STRAND AN ERROR. run.lastErr is a belief about what the store holds, and
// it used to start empty on every restart — so a registration carrying an error met a healthy
// pass, both sides read "no error", nothing was written, and the stored error became permanent.
// Staging reported a context-deadline error for hours that way about a source that was fine.
func TestARestartClearsAnErrorTheSourceHasRecoveredFrom(t *testing.T) {
	srv := testServer(t)
	ref := verseio.Ref("restarted")
	entitytest.Set(t, srv.Store, ref, entity.Obj{
		"name": "restarted", "root": "git", "url": "http://127.0.0.1:1",
	}.Payload())

	// It failed before the restart, and the registration still says so.
	srv.recordPass(t.Context(), srv.Store, ref, &sourceRun{}, verseio.Pass{Err: errFor("unreachable")})
	reg, _, _ := srv.Store.Get(ref)
	if failureText(reg.Payload) == "" {
		t.Fatal("the fixture needs a stored error to strand")
	}

	// The restart: a fresh run, seeded from the registration the way startSource seeds it.
	var e verseio.Entity
	if err := codec.FromNode(reg.Payload, &e); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	var seeded string
	if e.LastError != nil {
		seeded = e.LastError.Error
	}
	if seeded == "" {
		t.Fatal("the registration did not decode its stored failure — nothing would be seeded")
	}
	fresh := &sourceRun{lastErr: seeded}

	// A healthy pass now clears it, because memory and store agree about what stands.
	srv.recordPass(t.Context(), srv.Store, ref, fresh, verseio.Pass{})
	reg, _, _ = srv.Store.Get(ref)
	if _, ok := entity.Fields(reg.Payload)["lastError"]; ok {
		t.Fatalf("a recovered source came back still carrying its old error: %v", entity.ToObj(reg.Payload))
	}
}

// A REGISTRATION WRITTEN BEFORE THE RECORD STILL DECODES, and keeps its message. It carries
// lastError as a bare STRING, which gomap will not coerce into a record — it fails the whole
// entity ("expected object, got String"), and verseio.ListErr SKIPS what it cannot decode, so
// every source carrying an error at the moment of an upgrade would have dropped out of the
// listing and not been restored. Failure.FromTonyIR reads both spellings, which is what makes
// the field name reusable and the migration unnecessary.
func TestALegacyStringLastErrorDecodesWithNoWhen(t *testing.T) {
	p := entity.Obj{
		"name": "old", "root": "git", "url": "http://x",
		"lastError": "unreachable",
	}.Payload()
	var e verseio.Entity
	if err := codec.FromNode(p, &e); err != nil {
		t.Fatalf("a registration written by an older verse no longer decodes: %v", err)
	}
	if e.Name != "old" || e.URL != "http://x" {
		t.Fatalf("decoded the wrong thing: %+v", e)
	}
	// The custom decoder READS it rather than ignoring it: the message survives, and When is
	// nil because the old spelling never recorded one. A zero timestamp here would claim the
	// failure began at midnight in 1970.
	if e.LastError == nil {
		t.Fatal("a legacy string lastError should still be read, not dropped")
	}
	if e.LastError.Error != "unreachable" {
		t.Fatalf("the legacy message did not survive: %+v", e.LastError)
	}
	if e.LastError.When != nil {
		t.Fatalf("when should be nil for a spelling that never carried one, got %v", *e.LastError.When)
	}
}
