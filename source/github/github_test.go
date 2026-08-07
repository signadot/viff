package github_test

import (
	"context"
	"fmt"
	"net/http"
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
	r.run(4, "ci", "completed", "success", "cafe1234")
	r.discussion(5, "a question", false, "2026-01-04T00:00:00Z")

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
	r.run(7, "ci", "completed", "success", "cafe1234")
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
		{System: "github", Kind: "issue", ID: "other/app#1"},
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

// Runs are a WINDOW and nothing else: the last `-runs` created are kept and what falls out is
// tombstoned. Slack's messages rather than issues' two halves — a completed run is never
// interesting again, and an unbounded stream of them is the warehouse a source is not meant to
// be.
func TestTheRunWindowSlidesAndTombstones(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.run(1, "ci", "completed", "success", "aaaa")
	r.run(2, "ci", "completed", "failure", "bbbb")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Runs: 2})
	rf := source.NewReflector(src, store, 0)
	pass := func() {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	if got := entity.Str(mustGet(t, store, src.RunRef("acme/app", 2)), "conclusion"); got != "failure" {
		t.Fatalf("run 2 conclusion = %q", got)
	}

	r.run(3, "ci", "completed", "success", "cccc")
	pass()
	if _, ok, _ := store.Get(src.RunRef("acme/app", 1)); ok {
		t.Fatal("a run that fell out of the window was not tombstoned")
	}
	for _, id := range []int64{2, 3} {
		if _, ok, _ := store.Get(src.RunRef("acme/app", id)); !ok {
			t.Fatalf("run %d is missing from the window", id)
		}
	}
}

// The raggedness the window costs, asserted rather than left to be discovered: a run still in
// flight when more than `-runs` newer ones have been created falls out and is tombstoned, so a
// charter waiting on its conclusion never sees one.
//
// It is here because it cannot be designed away with the API GitHub offers — the runs listing
// has no ordering of its own and its `status` filter takes a single value, so there is no read
// that returns "everything not finished", and an enumeration of statuses would leave whatever
// GitHub adds next uncovered and standing forever. This is Slack's aged-out thread parent, and
// the lever is the same: `-runs` wants to be comfortably more than a repository creates while
// its longest job runs.
func TestALongRunningRunFallsOutOfASmallWindow(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.run(1, "nightly", "in_progress", "", "aaaa") // still going
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Runs: 1})
	rf := source.NewReflector(src, store, 0)
	pass := func() {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	if got := entity.Str(mustGet(t, store, src.RunRef("acme/app", 1)), "status"); got != "in_progress" {
		t.Fatalf("the in-flight run = %q", got)
	}
	r.run(2, "ci", "completed", "success", "bbbb")
	pass()
	if _, ok, _ := store.Get(src.RunRef("acme/app", 1)); ok {
		t.Skip("the window now keeps in-flight runs; this raggedness is fixed and the note wants updating")
	}
	// Documented, not desired. A larger window is the whole of the mitigation.
	src2 := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Runs: 10})
	rn, err := src2.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	if payloads(rn)["github:run:acme/app/1"] == nil {
		t.Fatal("a larger window does not keep the in-flight run, so the lever does not work")
	}
}

// CI state lands on the pull request as a field, and costs no request: it is derived from the
// runs already read, by head sha. Asking GitHub per pull request would cost one call each and
// put the budget somewhere it does not belong.
func TestTheCheckStateLandsOnThePullRequest(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.pr(1, "a change", "open", "2026-01-01T00:00:00Z", "cafe1234")
	r.run(10, "ci", "completed", "failure", "cafe1234")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	got := func() entity.Obj {
		rn, err := src.Reflect(context.Background(), entity.Ref{})
		if err != nil {
			t.Fatal(err)
		}
		return payloads(rn)["github:pr:acme/app#1"]
	}
	if c := got()["checks"]; c != "failure" {
		t.Fatalf("the PR's checks = %v, want the run's conclusion", c)
	}
	// A re-run over the same sha is a newer run, and its answer is the current one.
	r.run(11, "ci", "completed", "success", "cafe1234")
	if c := got()["checks"]; c != "success" {
		t.Fatalf("after a re-run the PR's checks = %v, want the newest run's answer", c)
	}
	// A run still going says so rather than saying nothing.
	r.run(12, "ci", "in_progress", "", "cafe1234")
	if c := got()["checks"]; c != "in_progress" {
		t.Fatalf("while CI runs the PR's checks = %v", c)
	}
	// And no check entities were invented to carry any of it.
	rn, _ := src.Reflect(context.Background(), entity.Ref{})
	for _, e := range rn.Entities {
		if e.Ref.Kind == "check" || e.Ref.Kind == "checkRun" {
			t.Fatalf("a check kind appeared: %s", e.Ref)
		}
	}
}

// A pull request whose head has no run in the window carries no `checks` field — absent rather
// than wrong, which is the difference between "CI has not spoken about this" and "CI passed".
func TestAPullRequestWithNoRunCarriesNoCheckState(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.pr(1, "a change", "open", "2026-01-01T00:00:00Z", "cafe1234")
	r.run(10, "ci", "completed", "success", "0000other")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := payloads(rn)["github:pr:acme/app#1"]["checks"]; ok {
		t.Fatalf("a PR with no run of its own carries checks = %v", c)
	}
}

// Discussions have their own number space, so `github:issue:org/repo#1` and
// `github:discussion:org/repo#1` are different things carrying the same number. That is fine —
// a ref is (system, kind, id) — and worth pinning because `#1` alone reads like an identifier
// and is not one.
func TestADiscussionDoesNotCollideWithAnIssue(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.discussion(1, "a discussion", false, "2026-01-02T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	got := payloads(rn)
	if got["github:issue:acme/app#1"]["title"] != "an issue" {
		t.Fatalf("the issue is wrong: %v", got["github:issue:acme/app#1"])
	}
	if got["github:discussion:acme/app#1"]["title"] != "a discussion" {
		t.Fatalf("the discussion is wrong: %v", got["github:discussion:acme/app#1"])
	}
}

// THE ASYMMETRY THIS STEP EXISTS FOR. A repository with Discussions turned off answers with an
// empty connection, exactly as one with no discussions does — so a source that took the list at
// face value would tombstone every discussion the repository had, the day somebody switched the
// feature off. It says nothing instead: the covers are dropped AND the entities with them, so
// whatever is standing stays.
//
// The issues get no such treatment, and that difference is the whole of what Covers is for.
func TestDiscussionsTurnedOffLeaveWhatIsStandingAlone(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.discussion(9, "a discussion", false, "2026-01-02T00:00:00Z")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	rf := source.NewReflector(src, store, 0)
	pass := func() {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatalf("a repository without discussions should still reflect: %v", err)
		}
	}
	pass()
	disc := entity.Ref{System: "github", Kind: "discussion", ID: "acme/app#9"}
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("the discussion was not reflected in the first place")
	}

	// Somebody turns the feature off.
	r.discussionsOff = true
	pass()
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("discussions turned off deleted what was already reflected")
	}
	// And the rest of the repository still reflects, which is the other half: a pass that can
	// say nothing about discussions still has everything else to say.
	if _, ok, _ := store.Get(entity.Ref{System: "github", Kind: "issue", ID: "acme/app#1"}); !ok {
		t.Fatal("a repository with discussions off stopped reflecting its issues")
	}
}

// The same asymmetry for the other way a discussions read can decline: GraphQL answers HTTP 200
// with an errors array, which a client that checked only the status would read as a repository
// with no discussions in it — and an empty answer under a cover is an instruction to tombstone
// everything beneath it. source/linear/api.go names this trap first; this is the second
// connector to meet it.
func TestAGraphQLRefusalIsNotAnEmptyAnswer(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.discussion(9, "a discussion", false, "2026-01-02T00:00:00Z")
	store := entitytest.Store(t)
	var logged []string
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"},
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	rf := source.NewReflector(src, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	disc := entity.Ref{System: "github", Kind: "discussion", ID: "acme/app#9"}
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("the discussion was not reflected in the first place")
	}

	r.graphqlError = "Resource not accessible by integration"
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatalf("a refused discussions read should not fail the pass: %v", err)
	}
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("a GraphQL refusal was read as an empty answer and deleted the discussion")
	}
	// AND IT SAYS WHY, which is the half the entity surviving does not prove. With `errors`
	// unchecked the request still fails safe — `data` is null, so hasDiscussionsEnabled reads
	// false and the covers drop — but the operator is told the feature is switched off when
	// their token is the problem. Found by removing the check and watching this test pass
	// anyway, which is what the message assertion is for.
	if !strings.Contains(strings.Join(logged, "\n"), "Resource not accessible") {
		t.Fatalf("the refusal was not reported as one: %v", logged)
	}
}

// A discussion that closes appears in the settled half carrying its new state, and leaves when
// enough newer ones have closed — the same two-cover shape as an issue, since it is the same
// shape of thing.
func TestAClosedDiscussionIsWindowed(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.discussion(1, "still open", false, "2026-01-01T00:00:00Z")
	r.discussion(2, "answered", true, "2026-01-02T00:00:00Z")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Settled: 1})
	rf := source.NewReflector(src, store, 0)
	pass := func() {
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	closed := entity.Ref{System: "github", Kind: "discussion", ID: "acme/app#2"}
	if entity.Str(mustGet(t, store, closed), "state") != "closed" {
		t.Fatal("a closed discussion is not in the settled half")
	}
	r.discussion(3, "closed later", true, "2026-01-03T00:00:00Z")
	pass()
	if _, ok, _ := store.Get(closed); ok {
		t.Fatal("a discussion that fell out of the settled window was not tombstoned")
	}
	if _, ok, _ := store.Get(entity.Ref{System: "github", Kind: "discussion", ID: "acme/app#1"}); !ok {
		t.Fatal("the settled window reached an open discussion")
	}
}

// ── the budget ───────────────────────────────────────────────────────────────

// THE THRESHOLDS, NAMED BEFORE THE NUMBERS ARE READ, because one chosen after seeing the data is
// not a threshold. A personal token has 5,000 requests an hour and verse resyncs every minute,
// so 60 passes an hour share that: about 83 chargeable requests per pass across every repository
// a deployment reflects.
//
//   - A WARM pass — nothing changed since the last one — must cost at most 3 chargeable
//     requests per repository. GitHub does not count a 304 against the primary limit, and the
//     whole reason conditional requests were built in at step 1 rather than bolted on at this
//     one is that a quiet repository should cost nearly nothing. Anything above this means an
//     endpoint is not caching and the arithmetic in the note collapses.
//   - A COLD pass — the first one, or after a restart — must cost at most 12. It happens once,
//     and it is what a cache costs to fill.
//
// Below those, the note's ~20 repositories per token stands; above them it does not, and GitHub
// App auth stops being deferred and becomes the next piece of work.
const (
	warmBudgetPerRepo = 3
	coldBudgetPerRepo = 12
)

// TestTheBudgetAPassCosts measures what a pass spends, warm and cold, and holds it to the
// thresholds above.
//
// It counts CHARGEABLE requests — issued less those answered 304 — because that is what the rate
// limit counts and what decides how many repositories a token covers. Requests issued is the
// wrong number to optimise: a pass that asks ten times and is answered 304 ten times has spent
// nothing.
func TestTheBudgetAPassCosts(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.pr(2, "a change", "open", "2026-01-02T00:00:00Z", "cafe1234")
	r.run(3, "ci", "completed", "success", "cafe1234")
	r.discussion(4, "a question", false, "2026-01-03T00:00:00Z")

	// The credential check at startup fills part of the cache, so the cold pass is measured
	// from a source that has just been built — which is what a restart looks like.
	before, beforeNM := f.counts()
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	if _, err := src.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	after, afterNM := f.counts()
	cold := (after - before) - (afterNM - beforeNM)

	before, beforeNM = after, afterNM
	if _, err := src.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	after, afterNM = f.counts()
	warm := (after - before) - (afterNM - beforeNM)
	issued := after - before

	t.Logf("per repository per pass: cold %d chargeable; warm %d chargeable of %d issued",
		cold, warm, issued)
	t.Logf("at a one-minute resync that is %d chargeable an hour warm, so a 5,000/hour token "+
		"covers about %d repositories", warm*60, 5000/max(warm*60, 1))

	if cold > coldBudgetPerRepo {
		t.Fatalf("a cold pass costs %d chargeable requests, over the %d named above",
			cold, coldBudgetPerRepo)
	}
	if warm > warmBudgetPerRepo {
		t.Fatalf("a warm pass costs %d chargeable requests, over the %d named above — an "+
			"endpoint is not caching, and the note's arithmetic does not hold", warm, warmBudgetPerRepo)
	}
	if warm >= issued {
		t.Fatal("no request was answered 304, so nothing is being cached at all")
	}
}

// A warm pass answers what the cold one did. A cache that drifted from the truth would be worse
// than no cache: converge compares the answer against the store, so a stale 304 body is an
// entity quietly frozen at whatever it was.
func TestAWarmPassAnswersWhatTheColdOneDid(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.pr(2, "a change", "open", "2026-01-02T00:00:00Z", "cafe1234")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	first, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := payloads(first), payloads(second)
	if len(a) != len(b) {
		t.Fatalf("cold answered %d entities, warm answered %d", len(a), len(b))
	}
	for ref, want := range a {
		if !entity.Equal(want.Payload(), b[ref].Payload()) {
			t.Fatalf("%s differs between a cold and a warm pass:\n cold %v\n warm %v", ref, want, b[ref])
		}
	}

	// And a cold source reflecting the same fixture answers the same thing, which is what makes
	// losing the cache cost work rather than correctness.
	cold := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	third, err := cold.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	c := payloads(third)
	for ref, want := range a {
		if !entity.Equal(want.Payload(), c[ref].Payload()) {
			t.Fatalf("%s differs on a source with no cache:\n warm %v\n cold %v", ref, want, c[ref])
		}
	}
}

// A FAILURE IS NEVER CACHED, and never answered from the cache either. This is the law the whole
// seam turns on: an empty answer carrying covers is an instruction to tombstone everything under
// them, so a source that turned a bad minute into "the repository has no issues" would delete a
// slice — and one that cached that would keep deleting it.
func TestAFailureIsNotCachedAndDoesNotAnswerFromTheCache(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	if _, err := src.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}

	// GitHub starts refusing. The pass fails rather than answering from what it holds.
	f.failWith = http.StatusForbidden
	if _, err := src.Reflect(context.Background(), entity.Ref{}); err == nil {
		t.Fatal("a refused pass answered anyway, from the cache")
	}

	// And the refusal left the cache alone: when GitHub recovers, the entity is still there.
	f.failWith = 0
	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	if payloads(rn)["github:issue:acme/app#1"] == nil {
		t.Fatalf("a failure poisoned the cache: %v", payloads(rn))
	}
}

// The lever the budget measurement turned up: discussions are the whole of a warm pass's cost,
// because they are GraphQL and a POST carries no ETag. Switching them off takes a quiet
// repository to nothing at all.
//
// And it must not read as "there are no discussions": a source saying nothing about them drops
// the covers with the entities, or turning this on would tombstone every discussion it had
// already reflected.
func TestSkippingDiscussionsCostsNothingAndDeletesNothing(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.discussion(9, "a question", false, "2026-01-02T00:00:00Z")
	store := entitytest.Store(t)

	with := newSource(t, f, github.Config{Repos: []string{"acme/app"}})
	rf := source.NewReflector(with, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	disc := entity.Ref{System: "github", Kind: "discussion", ID: "acme/app#9"}
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("the discussion was not reflected in the first place")
	}

	// The operator switches them off, and what was reflected stays.
	without := newSource(t, f, github.Config{Repos: []string{"acme/app"}, SkipDiscussions: true})
	rf2 := source.NewReflector(without, store, 0)
	if err := rf2.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(disc); !ok {
		t.Fatal("switching discussions off deleted what was already reflected")
	}

	// And a warm pass now costs nothing chargeable at all.
	if _, err := without.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	before, beforeNM := f.counts()
	if _, err := without.Reflect(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	after, afterNM := f.counts()
	chargeable := (after - before) - (afterNM - beforeNM)
	t.Logf("warm pass with discussions off: %d chargeable of %d issued", chargeable, after-before)
	if chargeable != 0 {
		t.Fatalf("a warm pass with discussions off still costs %d chargeable requests", chargeable)
	}
}
