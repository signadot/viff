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
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// askingRun spawns a task that blocks on its first action, so there is a real question to
// look at, and returns its id.
func askingRun(t *testing.T, srv *Server, ts *httptest.Server, id, resolverRef string) {
	t.Helper()
	// The binding has to EXIST. A spawn naming one that does not is refused now — it would
	// otherwise own the question with something that cannot answer it, and shadow whatever
	// standing binding covers the same subject. These tests are about a spawn naming a
	// binding, so they install the one they name.
	if resolverRef != "" {
		parts := strings.Split(strings.TrimPrefix(resolverRef, "resolvers:"), ":")
		if len(parts) != 2 {
			t.Fatalf("askingRun: %q is not resolvers:<kind>:<name>", resolverRef)
		}
		installResolver(t, ts.URL, `{name: `+parts[1]+`, kind: `+parts[0]+
			`, byRef: true, method: {driver: url, url: "https://x"}}`).Body.Close()
	}
	spawn := postTony(t, ts.URL+"/tasks", api.Spawn{ID: id, Prompt: "true", Resolver: resolverRef})
	defer spawn.Body.Close()
	if spawn.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(spawn.Body)
		t.Fatalf("spawn %s: %s: %s", id, spawn.Status, strings.TrimSpace(string(raw)))
	}
}

// A spawn that names a resolver binds an INSTANCE and stamps the binding on its questions.
//
// `launch.Spec.Resolver` has been decoded, bound and then dropped at launchOf all along, so a
// charter could name a resolver on a spawn and have it do nothing at all — silently, which is
// the failure the whole increment is arranged around. This is the test that fails today.
func TestASpawnBindsItsResolver(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const ref = "resolvers:security:security-review"

	askingRun(t, srv, ts, "watched", ref)
	qid := waitQuestion(t, srv, "watched")

	// The question knows who owns it, which is what makes a resolver's docket a plain
	// selection rather than a join against something else.
	q := question(t, srv, qid)
	if q["resolver"] != ref {
		t.Fatalf("the question names %v as its owner, want %q: %+v", q["resolver"], ref, q)
	}

	// And there is a record of who is watching, minted at spawn rather than at the ask —
	// a resolver reads context BEFORE the question, which is why it is attached at all.
	inst, ok, _ := srv.Store.Get(resolver.InstanceRef("security-review", "watched"))
	if !ok {
		t.Fatal("no instance was minted for a run whose spawn named a resolver")
	}
	got := entity.ToObj(inst.Payload)
	if got["binding"] != ref {
		t.Errorf("the instance does not name its binding: %+v", got)
	}
	if got["origin"] != mirror.Ref("watched").String() {
		t.Errorf("the instance does not name what it is watching: %+v", got)
	}
	if got["position"] == nil {
		t.Errorf("the instance has no position, so nothing can tell 'has not decided' from "+
			"'has not caught up': %+v", got)
	}
}

// A spawn that names none binds nothing, and its questions say so by ABSENCE.
//
// Absent is not the same as an owner nobody can name: it means the question belongs to whoever
// reads the docket, and a later layer depends on telling those apart.
func TestASpawnWithNoResolverBindsNothing(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	askingRun(t, srv, ts, "unwatched", "")
	qid := waitQuestion(t, srv, "unwatched")

	if q := question(t, srv, qid); q["resolver"] != nil {
		t.Fatalf("an unbound question carries %v as its owner: %+v", q["resolver"], q)
	}
	for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
		return ref.System == resolver.System && ref.Kind == resolver.InstanceKind
	}) {
		t.Fatalf("an unbound spawn minted %s", ent.Ref)
	}
}

// A resolver's docket is a plain selection, which is the whole point of the question being an
// entity: no privileged access, no join, and the same predicate a charter rule would use.
func TestAResolversDocketIsASelection(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	const ref = "resolvers:security:security-review"

	askingRun(t, srv, ts, "watched", ref)
	askingRun(t, srv, ts, "unwatched", "")
	waitQuestion(t, srv, "watched")
	waitQuestion(t, srv, "unwatched")
	time.Sleep(50 * time.Millisecond) // let both records land

	pat := entity.Pattern{
		System: mirror.QuestionSystem, Kind: mirror.QuestionKind,
		Predicate: entity.Obj{"resolver": ref}.Payload(),
	}
	got := pat.Select(srv.Store)
	if len(got) != 1 {
		t.Fatalf("{resolver: %q} selected %v, want only the watched run's question", ref, got)
	}
	if q, _, _ := srv.Store.Get(got[0]); entity.Str(q.Payload, "task") != "watched" {
		t.Fatalf("it selected a question of %q", entity.Str(q.Payload, "task"))
	}
}

// A spawn may not name a binding that is not installed.
//
// Left standing it is worse than useless: the question is owned by something that cannot answer
// it, and — the case that makes this a refusal rather than a shrug — it SHADOWS the standing
// binding covering the same subject, which never sees the question and has no instance through
// which to reach it. A misspelled binding turns a supervised subject into an unsupervised one,
// silently.
func TestASpawnMayNotNameABindingThatDoesNotExist(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	resp := postTony(t, ts.URL+"/tasks", api.Spawn{
		ID: "run-1", Prompt: "true", Subject: "change:pr:1", Resolver: "resolvers:security:ghost",
	})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a spawn named a binding that does not exist: %s %s", resp.Status, raw)
	}
	if !strings.Contains(string(raw), "not installed") {
		t.Fatalf("the refusal does not say what is wrong: %s", raw)
	}
	// And the standing layer still owns what it covers, for a spawn that names nobody.
	spawnFor(t, ts, "run-2", "change:pr:1", "")
	if got := question(t, srv, waitQuestion(t, srv, "run-2"))["resolver"]; got != "resolvers:security:payments-review" {
		t.Fatalf("the standing layer does not own an unnamed run: %v", got)
	}
}
