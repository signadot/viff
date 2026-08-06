package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/source/github"
)

func newSource(t *testing.T, f *fakeGitHub, cfg github.Config) *github.GitHub {
	t.Helper()
	cfg.Token = "test-token"
	cfg.Base = f.serve(t)
	src, err := github.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return src
}

func payloads(rn source.Reflection) map[string]entity.Obj {
	out := map[string]entity.Obj{}
	for _, e := range rn.Entities {
		out[e.Ref.String()] = entity.ToObj(e.Payload)
	}
	return out
}

// THE MASS-DELETION TEST, and the reason it is first in this file as well as first in the plan.
//
// A targeted pass answers about one item. If it declared the live cover — `{repo, state: open}`
// — while carrying that one entity, converge would tombstone every other open issue in the
// repository: the cover says the answer is complete for the open set, and the answer holds one
// of them. That is the single mistake in this connector that destroys a slice, and it is
// reachable from an ordinary webhook delivery, so it is pinned rather than reasoned about.
func TestATargetedPassCoversOneItemAndNothingElse(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "first", "open", "2026-01-01T00:00:00Z")
	r.issue(2, "second", "open", "2026-01-02T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), src.ItemRef("issue", "acme/app", 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rn.Entities) != 1 || rn.Entities[0].Ref.ID != "acme/app#2" {
		t.Fatalf("a targeted pass answered with %d entities: %v", len(rn.Entities), payloads(rn))
	}
	if len(rn.Covers) != 1 {
		t.Fatalf("a targeted pass covers %d patterns, want exactly one", len(rn.Covers))
	}
	c := rn.Covers[0]
	if c.Kind != "issue" {
		t.Fatalf("cover kind %q", c.Kind)
	}
	// The cover must name the ITEM. A cover over the open set, or over the repo, is the
	// mass-deletion bug — so this is asserted by what the cover does rather than by its shape:
	// it must not match the issue this pass said nothing about.
	other := entity.Entity{
		Ref:     entity.Ref{System: "github", Kind: "issue", ID: "acme/app#1"},
		Payload: entity.Obj{"repo": "acme/app", "number": 1, "state": "open"}.Payload(),
	}
	if c.MatchEntity(other) {
		t.Fatalf("a pass about #2 covers #1, so converge would tombstone it: %v",
			entity.ToObj(c.Predicate))
	}
	self := entity.Entity{
		Ref:     entity.Ref{System: "github", Kind: "issue", ID: "acme/app#2"},
		Payload: entity.Obj{"repo": "acme/app", "number": 2, "state": "open"}.Payload(),
	}
	if !c.MatchEntity(self) {
		t.Fatalf("a pass about #2 does not cover #2, so a deleted issue would stand forever: %v",
			entity.ToObj(c.Predicate))
	}
}

// And the other half of the same rule: an item GitHub no longer has is covered and absent,
// which is what tombstones it. A targeted pass that answered with no cover at all would leave a
// deleted issue standing forever.
func TestATargetedPassTombstonesAnItemThatIsGone(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "first", "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	r.remove(1)

	rn, err := src.Reflect(context.Background(), src.ItemRef("issue", "acme/app", 1))
	if err != nil {
		t.Fatalf("a deleted item should be an answer, not an error: %v", err)
	}
	if len(rn.Entities) != 0 {
		t.Fatalf("an item that is gone was answered with %v", payloads(rn))
	}
	if len(rn.Covers) != 1 {
		t.Fatal("an item that is gone must still be COVERED, or nothing tombstones it")
	}
}

// A pass that changes nothing writes nothing, which is the rule every payload in this connector
// is shaped by. converge compares the answer against the store, so a list in whatever order the
// API returned it — or anything derived from the time of the read — makes every entity differ on
// every pass: the whole slice rewritten every minute, every trigger over it fired, and
// lastReflect stamped forever.
//
// Driven through a real converge rather than by comparing two Reflections, because it is
// converge's comparison that decides, and this is the one test that would catch a payload that
// looks stable and is not.
func TestASecondPassOverUnchangedDataWritesNothing(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	i := r.issue(1, "first", "open", "2026-01-01T00:00:00Z")
	i.labels = []string{"sdlc", "api", "bug"} // unsorted on purpose
	p := r.pr(2, "a change", "open", "2026-01-02T00:00:00Z", "cafe1234")
	p.labels = []string{"review", "api"}
	r.issue(3, "done", "closed", "2026-01-03T00:00:00Z")

	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	rf := source.NewReflector(src, store, 0)

	wrote := func() int {
		n := -1
		rf.OnPass(func(pass source.Pass) {
			if pass.Err != nil {
				t.Fatalf("pass: %v", pass.Err)
			}
			n = pass.Wrote
		})
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if first := wrote(); first == 0 {
		t.Fatal("the first pass wrote nothing")
	}
	if second := wrote(); second != 0 {
		t.Fatalf("a second pass over unchanged data wrote %d — a payload is not stable", second)
	}
	// And the labels are sorted, which is what makes that true rather than lucky.
	got := entity.Strs(mustGet(t, store, entity.Ref{System: "github", Kind: "issue", ID: "acme/app#1"}), "labels")
	if len(got) != 3 || got[0] != "api" || got[1] != "bug" || got[2] != "sdlc" {
		t.Fatalf("labels = %v, want them sorted so a re-read is not a change", got)
	}
}

// The live set is covered as complete, so a partial list under it tombstones what it merely
// failed to read. -max-open is therefore an error rather than a truncation — and the pass fails
// with nothing written rather than half-answering.
func TestMaxOpenIsAnErrorNotATruncation(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	for n := 1; n <= 5; n++ {
		r.issue(n, "issue", "open", "2026-01-01T00:00:00Z")
	}
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, MaxOpen: 3})

	_, err := src.Reflect(context.Background(), entity.Ref{})
	if err == nil {
		t.Fatal("a repository past -max-open should fail the pass, not answer partially")
	}
	for _, want := range []string{"complete", "max-open"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// GitHub returns pull requests from the issues endpoint, distinguished only by a `pull_request`
// key. Reflecting them as issues would put one thing under two kinds sharing a number, and would
// make "every open issue" a question that quietly includes review traffic.
func TestAPullRequestNeverAppearsAsAnIssue(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "a real issue", "open", "2026-01-01T00:00:00Z")
	r.pr(2, "a proposal", "open", "2026-01-02T00:00:00Z", "cafe1234")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)
	if got["github:issue:acme/app#2"] != nil {
		t.Fatal("a pull request was reflected as an issue")
	}
	if got["github:pr:acme/app#2"] == nil {
		t.Fatalf("the pull request is missing: %v", got)
	}
	if got["github:issue:acme/app#1"] == nil {
		t.Fatalf("the issue is missing: %v", got)
	}
	// The join to a git source is the head sha, which is what makes "the commit this proposes"
	// a field read rather than a key anybody minted.
	if sha := entity.Str(rn.Entities[len(rn.Entities)-1].Payload, "headSha"); sha == "" {
		if got["github:pr:acme/app#2"]["headSha"] != "cafe1234" {
			t.Fatalf("a pull request carries no headSha: %v", got["github:pr:acme/app#2"])
		}
	}
	// A targeted pass at the issue number of a pull request writes nothing under the issue
	// kind, and still covers it — so an issue that became a PR is tombstoned rather than left.
	one, err := src.Reflect(context.Background(), src.ItemRef("issue", "acme/app", 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Entities) != 0 {
		t.Fatalf("a targeted issue pass answered for a pull request: %v", payloads(one))
	}
}

// An item that closes appears in the settled half carrying its new state, and leaves when
// enough newer ones have closed. Both halves are covered or neither works: covering only the
// live set leaves a husk per item ever closed, and covering the repo whole would tombstone the
// back catalogue on the first pass.
func TestAClosedItemIsWindowedAndAgesOut(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "still open", "open", "2026-01-01T00:00:00Z")
	r.issue(2, "just closed", "closed", "2026-01-02T00:00:00Z")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Settled: 1})
	rf := source.NewReflector(src, store, 0)
	pass := func() {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass()

	closed := entity.Ref{System: "github", Kind: "issue", ID: "acme/app#2"}
	if entity.Str(mustGet(t, store, closed), "state") != "closed" {
		t.Fatal("a closed issue is not in the settled half")
	}

	// A newer closure pushes it out of a window of one.
	r.issue(3, "closed later", "closed", "2026-01-03T00:00:00Z")
	pass()
	if _, ok, _ := store.Get(closed); ok {
		t.Fatal("an item that fell out of the settled window was not tombstoned")
	}
	if _, ok, _ := store.Get(entity.Ref{System: "github", Kind: "issue", ID: "acme/app#3"}); !ok {
		t.Fatal("the newer closure is missing")
	}
	// And the open one is untouched by any of it.
	if _, ok, _ := store.Get(entity.Ref{System: "github", Kind: "issue", ID: "acme/app#1"}); !ok {
		t.Fatal("the settled window reached an open issue")
	}
}

// Conditional requests are in this connector from the first commit rather than as a later
// optimisation, because at six requests per repository per pass a personal token is spent
// around a dozen repositories and a 304 does not count against the limit.
func TestASecondPassAsksConditionallyAndIsAnsweredFromTheCache(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "first", "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	if _, err := src.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	_, before := f.counts()
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	_, after := f.counts()
	if after <= before {
		t.Fatal("a second pass asked unconditionally: no request was answered 304")
	}
	// And the answer is the same one, decoded from the cached body rather than lost.
	if payloads(rn)["github:issue:acme/app#1"] == nil {
		t.Fatalf("a 304 lost the answer: %v", payloads(rn))
	}
}

// The system is the operator's to choose, because more than one of these gets deployed and the
// partition between them is drawn by whoever runs them. Everything a source writes and claims
// is spelled from it.
func TestTheSystemIsTheOperatorsToChoose(t *testing.T) {
	f := newFake()
	f.repo("acme/app").issue(1, "first", "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, System: "github-internal"})

	if src.System() != "github-internal" {
		t.Fatalf("System() = %q", src.System())
	}
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rn.Entities {
		if e.Ref.System != "github-internal" {
			t.Fatalf("an entity outside the chosen system: %s", e.Ref)
		}
	}
	for _, c := range rn.Covers {
		if c.System != "github-internal" {
			t.Fatalf("a cover outside the chosen system: %+v", c)
		}
	}
	for _, o := range source.Describe(src).Owns {
		if !strings.HasPrefix(o.Ref, "github-internal:") {
			t.Fatalf("a claim outside the chosen system: %s", o.Ref)
		}
	}
	// Two systems are disjoint on the first segment, which is the whole reason this is a flag:
	// a deployer who wants no coordination owes nobody a disjointness proof.
	other := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	ok, err := source.Disjoint(source.Describe(src).Owns, source.Describe(other).Owns)
	if err != nil || !ok {
		t.Fatalf("two systems were not provably disjoint: ok=%v err=%v", ok, err)
	}
}

// The claim a source declares has to be the claim its reflection actually writes under, or the
// install proves something about a slice nobody touches — and it must not run AHEAD of what the
// connector reflects, or it refuses a neighbour over entities nobody is writing.
func TestOwnsCoversWhatItReflectsAndNoMore(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "first", "open", "2026-01-01T00:00:00Z")
	r.pr(2, "a change", "open", "2026-01-02T00:00:00Z", "cafe1234")
	f.repo("acme/other")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	claims, err := source.ParseOwns(source.Describe(src).Owns)
	if err != nil {
		t.Fatalf("github declares a claim that does not parse: %v", err)
	}
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rn.Entities {
		places := source.Claimed(claims, e.Ref)
		if len(places) != 1 || !places[0].IsEntity() {
			t.Fatalf("%s is reflected; Claimed = %v, want the whole entity", e.Ref, places)
		}
	}
	// Not a neighbour's, and not a kind this build does not write yet.
	for _, ref := range []entity.Ref{
		{System: "github", Kind: "issue", ID: "acme/other#1"},
		{System: "github", Kind: "repo", ID: "acme/other"},
		{System: "github", Kind: "discussion", ID: "acme/app#1"},
		{System: "github", Kind: "run", ID: "acme/app/1"},
	} {
		if places := source.Claimed(claims, ref); len(places) != 0 {
			t.Fatalf("%s is claimed but not written: %v", ref, places)
		}
	}
	// The `#` is what keeps a repository from claiming its neighbour's items.
	two := newSource(t, f, github.Config{Repos: []string{"acme/other"}})
	ok, err := source.Disjoint(source.Describe(src).Owns, source.Describe(two).Owns)
	if err != nil || !ok {
		t.Fatalf("two repositories were not provably disjoint: ok=%v err=%v", ok, err)
	}
}

// The repository entity is here rather than in a later step because it is what makes a
// github-only deployment useful standing alone: the default branch and where it points is the
// git-shaped fact most charters want, and it costs one un-paginated call where taking it from
// git costs a deployed mirror.
func TestTheRepoCarriesTheDefaultBranchAndTheJoin(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)["github:repo:acme/app"]
	if got == nil {
		t.Fatalf("no repo entity: %v", payloads(rn))
	}
	if got["defaultBranch"] != "main" || got["defaultBranchSha"] != "aaaa1111" {
		t.Fatalf("the repo does not say where main is: %v", got)
	}
	// cloneUrl is the join to a git source, whose repo id is the operator's and so lines up
	// with nothing. It is not decoration.
	if got["cloneUrl"] != "https://github.com/acme/app.git" {
		t.Fatalf("the repo carries no cloneUrl: %v", got)
	}
}

// An unborn default branch is a fact about the repository, not a failure to read it: the entity
// is written without a sha rather than the pass failing.
func TestAnUnbornDefaultBranchIsNotAFailedPass(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.sha = ""
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatalf("an unborn branch failed the pass: %v", err)
	}
	got := payloads(rn)["github:repo:acme/app"]
	if got == nil || got["defaultBranchSha"] != nil {
		t.Fatalf("repo = %v, want no sha and no failure", got)
	}
}

// A bad token is a task that does not start, in front of whoever just deployed it, rather than
// a source that installs clean and writes a failure into lastError once a minute forever.
func TestABadCredentialFailsAtStartup(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	_, err := github.New(context.Background(), github.Config{
		Token: "wrong", Base: f.serve(t), Repos: []string{"acme/app"},
	})
	if err == nil {
		t.Fatal("a bad token should fail New, not the first reflect")
	}
	// And a repository the credential cannot see is the same kind of answer.
	_, err = github.New(context.Background(), github.Config{
		Token: "test-token", Base: f.serve(t), Repos: []string{"acme/missing"},
	})
	if err == nil {
		t.Fatal("a repository that is not there should fail New")
	}
}

// A ref this source does not reflect answers with NO covers, which claims nothing and so
// tombstones nothing — the honest answer about somebody else's slice.
func TestAnotherSourcesSliceIsAnsweredWithNothing(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	for _, ref := range []entity.Ref{
		{System: "github", Kind: "repo", ID: "other/app"},
		{System: "github", Kind: "issue", ID: "other/app#1"},
		{System: "linear", Kind: "issue", ID: "ENG-1"},
	} {
		rn, err := src.Reflect(context.Background(), ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if len(rn.Covers) != 0 || len(rn.Entities) != 0 {
			t.Fatalf("%s answered %d covers and %d entities, want nothing",
				ref, len(rn.Covers), len(rn.Entities))
		}
	}
}

func mustGet(t *testing.T, s entity.Store, r entity.Ref) entity.Payload {
	t.Helper()
	e, ok, err := s.Get(r)
	if err != nil || !ok {
		t.Fatalf("get %s: ok=%v err=%v", r, ok, err)
	}
	return e.Payload
}
