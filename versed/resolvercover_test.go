package versed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// askingRun spawns a task that blocks on its first action, so there is a real question to
// look at, and returns its id.
func askingRun(t *testing.T, srv *Server, ts *httptest.Server, id, resolverRef string) {
	t.Helper()
	// The resolver has to EXIST. A spawn naming one that does not is refused — it would
	// otherwise own the question with something that cannot answer it, and shadow whatever
	// standing resolver matches the same subject. These tests are about a spawn naming a
	// resolver, so they install the one they name.
	if resolverRef != "" {
		ref, err := entity.ParseAddr(resolverRef)
		if err != nil || !resolver.IsResolver(ref) {
			t.Fatalf("askingRun: %q is not verse.resolvers.<name>: %v", resolverRef, err)
		}
		installResolver(t, ts.URL, `{name: `+ref.Name()+
			`, byRef: true, method: {driver: url, url: "https://x"}}`).Body.Close()
	}
	spawn := postTony(t, ts.URL+"/tasks", api.Spawn{ID: id, Prompt: "true", Resolver: resolverRef})
	defer spawn.Body.Close()
	if spawn.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(spawn.Body)
		t.Fatalf("spawn %s: %s: %s", id, spawn.Status, strings.TrimSpace(string(raw)))
	}
}

// A spawn naming a resolver mints a COVER on its run and stamps the resolver on its questions.
//
// `launch.Spec.Resolver` has been decoded, bound and then dropped at launchOf all along, so a
// charter could name a resolver on a spawn and have it do nothing at all — silently, which is
// the failure the whole increment is arranged around. This is the test that fails today.
func TestASpawnCoversItsRun(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const ref = "verse.resolvers.security-review"

	askingRun(t, srv, ts, "watched", ref)
	qid := waitQuestion(t, srv, "watched")

	// The question knows who owns it, which is what makes a resolver's docket a plain
	// selection rather than a join against something else.
	q := waitOwned(t, srv, qid)
	if q["resolver"] != ref {
		t.Fatalf("the question names %v as its owner, want %q: %+v", q["resolver"], ref, q)
	}

	// And there is a record of who covers it, minted at spawn rather than at the ask —
	// an answerer reads context BEFORE the question, which is why it is attached at all.
	cover, ok, _ := srv.Store.Get(resolver.CoverRef("security-review", "watched"))
	if !ok {
		t.Fatal("no cover was minted for a run whose spawn named a resolver")
	}
	got := entity.ToObj(cover.Payload)
	if got["resolver"] != ref {
		t.Errorf("the cover does not name its resolver: %+v", got)
	}
	if got["stream"] != mirror.Ref("watched").String() {
		t.Errorf("the cover does not name its gate stream: %+v", got)
	}
	if got["position"] == nil {
		t.Errorf("the cover has no position, so nothing can tell 'has not decided' from "+
			"'has not caught up': %+v", got)
	}
}

// A spawn that names none covers nothing, and its questions say so by ABSENCE.
//
// Absent is not the same as an owner nobody can name: it means the question belongs to whoever
// reads the docket, and a later layer depends on telling those apart.
func TestASpawnWithNoResolverCoversNothing(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	askingRun(t, srv, ts, "unwatched", "")
	qid := waitQuestion(t, srv, "unwatched")

	if q := question(t, srv, qid); q["resolver"] != nil {
		t.Fatalf("an unowned question carries %v as its owner: %+v", q["resolver"], q)
	}
	for _, ent := range entity.Below(srv.Store, resolver.IsCover, 1, entity.VerseRoot, resolver.CoverSystem) {
		t.Fatalf("a spawn naming no resolver minted %s", ent.Ref)
	}
}

// A resolver's docket is a plain selection, which is the whole point of the question being an
// entity: no privileged access, no join, and the same predicate a charter rule would use.
func TestAResolversDocketIsASelection(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const ref = "verse.resolvers.security-review"

	askingRun(t, srv, ts, "watched", ref)
	askingRun(t, srv, ts, "unwatched", "")
	waitQuestion(t, srv, "watched")
	waitQuestion(t, srv, "unwatched")
	time.Sleep(50 * time.Millisecond) // let both records land

	pat := verseio.Cover(entity.VerseRoot, mirror.QuestionSystem, mirror.QuestionKind, "*")
	pat.Predicate = entity.Obj{"resolver": ref}.Payload()
	got := pat.Select(srv.Store)
	if len(got) != 1 {
		t.Fatalf("{resolver: %q} selected %v, want only the watched run's question", ref, got)
	}
	if q, _, _ := srv.Store.Get(got[0]); entity.Str(q.Payload, "task") != "watched" {
		t.Fatalf("it selected a question of %q", entity.Str(q.Payload, "task"))
	}
}

// A spawn may not name a resolver that is not installed.
//
// Left standing it is worse than useless: the question is owned by something that cannot answer
// it, and — the case that makes this a refusal rather than a shrug — it SHADOWS the standing
// resolver matching the same subject, which never sees the question and has no cover through
// which to reach it. A misspelled resolver turns a supervised subject into an unsupervised one,
// silently.
func TestASpawnMayNotNameAResolverThatDoesNotExist(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	resp := postTony(t, ts.URL+"/tasks", api.Spawn{
		ID: "run-1", Prompt: "true", Subject: `change.pr."1"`, Resolver: "verse.resolvers.ghost",
	})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a spawn named a resolver that does not exist: %s %s", resp.Status, raw)
	}
	if !strings.Contains(string(raw), "not installed") {
		t.Fatalf("the refusal does not say what is wrong: %s", raw)
	}
	// And the standing layer still owns what it matches, for a spawn that names nobody.
	spawnFor(t, ts, "run-2", `change.pr."1"`, "")
	if got := waitOwned(t, srv, waitQuestion(t, srv, "run-2"))["resolver"]; got != "verse.resolvers.payments-review" {
		t.Fatalf("the standing layer does not own an unnamed run: %v", got)
	}
}
