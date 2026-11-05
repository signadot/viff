package io_test

import (
	"context"
	"errors"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"testing"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
	verseiod "github.com/signadot/verse/pkg/io/iod"
)

// serve puts a source behind iod and returns a client for it — the deployed shape, in a
// test's address space.
func serve(t *testing.T, src verseio.Source) *verseio.Client {
	t.Helper()
	ts := httptest.NewServer(verseiod.New(src).Handler())
	t.Cleanup(ts.Close)
	return verseio.HTTP(src.Root(), ts.URL, nil)
}

// The test that earns the transport: ONE verseio.Git, one Reflection, compared across both
// ways of asking. It is the whole claim of this step — that a source deployed elsewhere and
// one linked in are the same source — and it is where the wire encoding of a Reflection
// actually gets pinned, covers, predicates and payloads included.
func TestReflectionIsTheSameAcrossBothTransports(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "a.txt", "hi", "first")
	gitRun(t, dir, "branch", "shiny")

	g := verseio.NewGit()
	g.AddRepo("A", dir)

	direct, err := g.Reflect(ctx, entity.Ref{})
	if err != nil {
		t.Fatalf("reflect in task: %v", err)
	}
	overHTTP, err := serve(t, g).Reflect(ctx, entity.Ref{})
	if err != nil {
		t.Fatalf("reflect over http: %v", err)
	}
	sameReflection(t, direct, overHTTP)

	// And a targeted ref, since that is a parameter the transport has to carry rather than a
	// value it merely echoes: a ref lost on the way would answer for the whole source.
	one, err := serve(t, g).Reflect(ctx, g.RepoRef("A"))
	if err != nil {
		t.Fatalf("targeted reflect over http: %v", err)
	}
	sameReflection(t, direct, one) // one repo is registered, so the targeted answer is the whole
}

func sameReflection(t *testing.T, want, got verseio.Reflection) {
	t.Helper()
	if len(got.Covers) != len(want.Covers) {
		t.Fatalf("covers: got %d, want %d", len(got.Covers), len(want.Covers))
	}
	for i, w := range want.Covers {
		g := got.Covers[i]
		if len(g.Path) != len(w.Path) || g.Op != w.Op {
			t.Fatalf("cover %d: got %+v, want %+v", i, g, w)
		}
		for j := range w.Path {
			if !entity.Equal(g.Path[j], w.Path[j]) || g.Path[j].Tag != w.Path[j].Tag {
				t.Fatalf("cover %d path segment %d: got %v, want %v", i, j, g.Path, w.Path)
			}
		}
		// The predicate is the half a cover would be dangerous without: `{repo: A}` lost on
		// the way turns "this repo's refs" into "every repo's refs", and converge would
		// tombstone the neighbours.
		if !entity.Equal(g.Predicate, w.Predicate) {
			t.Fatalf("cover %d predicate: got %v, want %v",
				i, entity.ToObj(g.Predicate), entity.ToObj(w.Predicate))
		}
	}
	if len(got.Entities) != len(want.Entities) {
		t.Fatalf("entities: got %d, want %d", len(got.Entities), len(want.Entities))
	}
	for i, w := range want.Entities {
		g := got.Entities[i]
		if g.Ref != w.Ref {
			t.Fatalf("entity %d: got ref %s, want %s", i, g.Ref, w.Ref)
		}
		if !entity.Equal(g.Payload, w.Payload) {
			t.Fatalf("entity %s payload: got %v, want %v",
				w.Ref, entity.ToObj(g.Payload), entity.ToObj(w.Payload))
		}
	}
}

// erroring is a source whose external system is not answering — the ordinary state of one,
// and the state this transport must not render as good news.
type erroring struct{ root string }

func (e erroring) Root() string { return e.root }

func (e erroring) Reflect(context.Context, entity.Ref) (verseio.Reflection, error) {
	return verseio.Reflection{}, errors.New("the repo is on a laptop that is asleep")
}

// The sharpest bug this design admits, and it costs three lines to close. An empty Reflection
// is not "nothing to say" — it is an instruction to tombstone everything its covers claim. So
// a source that FAILED must reach verse as an error, never as an empty answer, or a transport
// fault converges as "the external system is empty now" and the whole slice is deleted.
func TestAFailedReflectIsAnErrorAndNotAnEmptyOne(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	standing := entity.RefAt("x", "a", "1")

	// Something the source reflected earlier, standing in the store.
	ok := &fakeSource{root: "x", answer: func() verseio.Reflection {
		rn := verseio.Reflection{Covers: []entity.Pattern{verseio.Cover("x", "a", "*")}}
		rn.Add(standing, entity.Obj{"n": 1})
		return rn
	}}
	if err := verseio.NewReflector(ok, store, 0).Reconcile(ctx, entity.Ref{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rn, err := serve(t, erroring{root: "x"}).Reflect(ctx, entity.Ref{})
	if err == nil {
		t.Fatalf("a failed reflect answered %+v instead of an error", rn)
	}
	// Unreachable, not not-a-source: something is there and it is having a bad day.
	if !errors.Is(err, verseio.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}

	// And the whole point of the above: converge never runs, so what was reflected stands.
	if err := verseio.NewReflector(serve(t, erroring{root: "x"}), store, 0).
		Reconcile(ctx, entity.Ref{}); err == nil {
		t.Fatal("a reconcile over a failing source should report the failure")
	}
	if _, ok, _ := store.Get(standing); !ok {
		t.Fatal("a failed reflect tombstoned the slice: it converged to an empty answer")
	}
}

// A body that does not decode is NOT-a-source rather than unreachable, because waiting will
// not fix a format disagreement — and it is emphatically not an empty reflection.
func TestAnUndecodableAnswerIsRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("<html>a proxy's opinion</html>"))
	}))
	defer ts.Close()

	_, err := verseio.HTTP("x", ts.URL, nil).Reflect(context.Background(), entity.Ref{})
	if !errors.Is(err, verseio.ErrNotSource) {
		t.Fatalf("want ErrNotSource, got %v", err)
	}
}

// The probe's three outcomes, which are the runner's three and for the runner's reasons: they
// differ on whether waiting could change the answer.
func TestProbe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	initRepo(t, dir)
	g := verseio.NewGit()
	g.AddRepo("A", dir)

	ts := httptest.NewServer(verseiod.New(g).Handler())
	defer ts.Close()

	reach, d, err := verseio.Probe(ctx, ts.URL, nil)
	if reach != verseio.IsSource || err != nil {
		t.Fatalf("probe of a source = %v (%v)", reach, err)
	}
	if d.Root != verseio.GitRoot {
		t.Fatalf("descriptor root = %q, want git", d.Root)
	}
	// The ownership claim an install proves disjointness with: one place per repo.
	want := []verseio.Own{{At: "git.A"}}
	// EqualFunc rather than Equal: a claim carries a path, so it holds a slice and is not
	// comparable. Its rendering is the whole of it.
	if !slices.EqualFunc(d.Owns, want, func(a, b verseio.Own) bool { return a.String() == b.String() }) {
		t.Fatalf("descriptor owns = %v, want %v", d.Owns, want)
	}

	// Something that answers and is not this: the mistake an operator makes with a url.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if reach, _, _ := verseio.Probe(ctx, other.URL, nil); reach != verseio.NotSource {
		t.Fatalf("probe of a non-source = %v, want not a source", reach)
	}

	// Nothing there at all — the ordinary state of a source deployed alongside the entity
	// that names it, and the reason an install warns rather than refuses.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if reach, _, _ := verseio.Probe(ctx, dead.URL, nil); reach != verseio.Unreachable {
		t.Fatalf("probe of a dead url = %v, want unreachable", reach)
	}

	// A source having a bad day answers 500, and that is UNREACHABLE rather than
	// not-a-source: refusing the install on it would refuse a fact that fixes itself.
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the disk is full", http.StatusInternalServerError)
	}))
	defer sick.Close()
	if reach, _, _ := verseio.Probe(ctx, sick.URL, nil); reach != verseio.Unreachable {
		t.Fatalf("probe of a failing source = %v, want unreachable", reach)
	}
}

// The wire itself, by name.
//
// Every test above compares a Reflection with a Reflection, which pins that the data survives
// but not what it is CALLED — both ends use the same Go struct, so renaming a tony tag renames
// it symmetrically and nothing notices. The field names are the actual contract: the point of
// a deployed source is that somebody can write one in another language against this document,
// and for them a rename is a break. So this reads the bytes.
func TestTheWireIsWhatTheContractSays(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	commit(t, dir, "a.txt", "hi", "first")
	g := verseio.NewGit()
	g.AddRepo("A", dir)
	ts := httptest.NewServer(verseiod.New(g).Handler())
	defer ts.Close()

	fields := func(path string) entity.Payload {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := stdio.ReadAll(resp.Body)
		node, err := codec.ParseNode(body, codec.Tony)
		if err != nil {
			t.Fatalf("GET %s answered undecodable tony: %v\n%s", path, err, body)
		}
		return node
	}
	has := func(what string, n entity.Payload, want ...string) {
		t.Helper()
		for _, f := range want {
			if entity.Get(n, f) == nil {
				t.Fatalf("%s has no %q field: %v", what, f, entity.ToObj(n))
			}
		}
	}

	d := fields(verseio.DescribePath)
	has("the descriptor", d, "root", "owns")

	rn := fields(verseio.ReflectPath)
	has("a reflection", rn, "covers", "entities")
	covers, entities := entity.Get(rn, "covers"), entity.Get(rn, "entities")
	if len(covers.Values) == 0 || len(entities.Values) == 0 {
		t.Fatalf("expected a cover and an entity to look at: %v", entity.ToObj(rn))
	}
	has("a cover", covers.Values[0], "path")
	has("an entity", entities.Values[0], "at", "payload")

	// An address is one string, a kinded path, everywhere it is written — a charter, a CLI
	// argument, and here. Structurally spelled it would be the one identifier every surface
	// shares having two spellings depending on who wrote it.
	if r := entity.Get(entities.Values[0], "at"); entity.Str(entities.Values[0], "at") == "" {
		t.Fatalf("an address should be spelled as one kinded path, got %v", entity.ToObj(r))
	}
}
