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
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/resolver"
)

// spawnFor starts a run about a subject, optionally naming a resolver for it.
func spawnFor(t *testing.T, ts *httptest.Server, id, subject, resolverRef string) {
	t.Helper()
	resp := postTony(t, ts.URL+"/tasks", map[string]any{
		"id": id, "prompt": "true", "subject": subject, "resolver": resolverRef,
	})
	defer resp.Body.Close()
	// Loudly: a swallowed status is how a spawn naming a resolver that does not exist went
	// unnoticed, and a test whose run never started fails three assertions later as a timeout.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("spawn %s: %s: %s", id, resp.Status, strings.TrimSpace(string(raw)))
	}
}

func standing(name, area string) string {
	return `{name: ` + name + `, condition: {subject: {path: "change.pr.*",` +
		` value: {area: ` + area + `}}}, method: {driver: url, url: "https://x"}}`
}

func pr(t *testing.T, srv *Server, id, area string) entity.Ref {
	t.Helper()
	ref := entity.RefAt("change", "pr", id)
	entitytest.Set(t, srv.Store, ref, entity.Obj{"area": area}.Payload())
	return ref
}

// Several standing resolvers may match one subject, and the first BY NAME owns its questions.
//
// Overlap is ordered rather than forbidden. Disjointness of two predicates is undecidable in
// general, so a refusal at install has to be conservative, and a conservative refusal tells
// the operator with `{area: payments}` and `{area: infra}` no for a reason that is not true of
// their case. The design's requirement survives — a question still has exactly one answerer at
// its layer — by construction rather than by prohibition.
func TestTheFirstResolverByNameOwnsASubject(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	installResolver(t, ts.URL, standing("zulu", "payments")).Body.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	subject := pr(t, srv, "1", "payments")

	got, ok := resolver.Owner(srv.Store, subject)
	if !ok || got.Name != "alpha" {
		t.Fatalf("owner = %+v (ok=%v), want alpha — the first by name", got, ok)
	}

	// It is the NAME that decides, not the order of installation: zulu went in first.
	installResolver(t, ts.URL, standing("aardvark", "payments")).Body.Close()
	if got, _ := resolver.Owner(srv.Store, subject); got.Name != "aardvark" {
		t.Fatalf("owner = %q after installing an earlier name, want aardvark", got.Name)
	}
}

// A resolver matches what its condition selects, and nothing else.
func TestAStandingResolverMatchesWhatItSelects(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	if _, ok := resolver.Owner(srv.Store, pr(t, srv, "1", "payments")); !ok {
		t.Fatal("a payments PR is matched by nobody")
	}
	if got, ok := resolver.Owner(srv.Store, pr(t, srv, "2", "infra")); ok {
		t.Fatalf("an infra PR is matched by %q", got.Name)
	}
	// A different kind entirely is untouched.
	other := entity.RefAt("demo", "release", "1")
	entitytest.Set(t, srv.Store, other, entity.Obj{"area": "payments"}.Payload())
	if got, ok := resolver.Owner(srv.Store, other); ok {
		t.Fatalf("a release is matched by %q", got.Name)
	}
}

// Installing REPORTS what a resolver may be ordered against, and refuses nothing.
//
// A cheap syntactic check is exactly right for a notice: a false positive costs a sentence
// rather than an install, so nothing here has to decide whether two predicates can both hold.
func TestInstallingReportsWhatItOverlaps(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	first := installResolver(t, ts.URL, standing("alpha", "payments"))
	var out map[string]any
	decodeResp(t, first, &out)
	first.Body.Close()
	if out["overlaps"] != nil {
		t.Fatalf("the first resolver overlaps %v", out["overlaps"])
	}

	second := installResolver(t, ts.URL, standing("bravo", "infra"))
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("a second resolver over one kind = %s, want 201 — overlap is ordered, not refused",
			second.Status)
	}
	decodeResp(t, second, &out)
	second.Body.Close()
	over, _ := out["overlaps"].([]any)
	if len(over) != 1 || over[0] != "verse.resolvers.alpha" {
		t.Fatalf("overlaps = %v, want the resolver it shares a kind with", out["overlaps"])
	}

	// A resolver over a different kind shares nothing and is told so by silence.
	third := installResolver(t, ts.URL,
		`{name: charlie, condition: {subject: {path: "demo.release.*"}}, `+
			`method: {driver: url, url: "https://x"}}`)
	decodeResp(t, third, &out)
	third.Body.Close()
	if out["overlaps"] != nil {
		t.Fatalf("a resolver over another kind reports %v", out["overlaps"])
	}
}

// Ownership follows from what DECLARED it, layer by layer, and absent layers are skipped.
//
//	a run's question    → the spawn's resolver, else the standing resolver matching its flow, else ⊤
//	a firing's question → the rule's gating.resolver, else the standing resolver, else ⊤
//
// That is a rule about layers and not a ranking between two matches. A standing resolver is not
// a wider claim on a named one's questions; it answers a different set — which is why a run
// whose spawn names a resolver leaves its standing resolver nothing to do rather than competing
// with it.
func TestTheLayerRuleDecidesWhoOwnsAQuestion(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	// A run about a matched subject, whose spawn ALSO names one: the named layer owns it.
	// Installed, because a spawn naming a resolver that does not exist is refused — it would
	// shadow the standing layer below with something that cannot answer.
	installResolver(t, ts.URL, `{name: security-review, byRef: true,`+
		` method: {driver: url, url: "https://x"}}`).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "named", `change.pr."1"`, "verse.resolvers.security-review")
	if got := waitOwned(t, srv, waitQuestion(t, srv, "named"))["resolver"]; got != "verse.resolvers.security-review" {
		t.Fatalf("a named run is owned by %v, want the resolver its spawn named", got)
	}

	// The same run with no name: the standing resolver matching its flow owns it.
	spawnFor(t, ts, "unnamed", `change.pr."1"`, "")
	if got := waitOwned(t, srv, waitQuestion(t, srv, "unnamed"))["resolver"]; got != "verse.resolvers.payments-review" {
		t.Fatalf("an unnamed run over a matched subject is owned by %v, want the standing resolver", got)
	}

	// A run about a subject nobody matches: absent, which is ⊤ — the docket a person reads.
	pr(t, srv, "2", "infra")
	spawnFor(t, ts, "unmatched", `change.pr."2"`, "")
	if got := question(t, srv, waitQuestion(t, srv, "unmatched"))["resolver"]; got != nil {
		t.Fatalf("an unmatched run is owned by %v, want nobody", got)
	}
}

// And the firing side of the same rule: a trigger gate is owned by the resolver its rule
// named, else by the standing resolver matching the subject it is about.
func TestAFiringsGateIsOwnedByItsLayer(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	// A rule that names nobody: the standing resolver matching the subject owns its gate.
	addRule(t, ts, `{
		name: guard,
		condition: {subject: {path: "change.pr.*", value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)
	pr(t, srv, "1", "payments")
	if got := waitOwner(t, srv, "guard"); got != "verse.resolvers.payments-review" {
		t.Fatalf("a firing's gate is owned by %q, want the standing resolver", got)
	}

	// A rule that names one: the named layer owns it, and the standing resolver is left the
	// questions it was already answering rather than competing for these.
	addRule(t, ts, `{
		name: named,
		condition: {subject: {path: "change.pr.*", value: {area: payments}}},
		gating: {resolver: "verse.resolvers.security-review", gates: [{name: sign, mode: ask}]},
		action: {patch: {signed: true}},
	}`)
	pr(t, srv, "2", "payments")
	if got := waitOwner(t, srv, "named"); got != "verse.resolvers.security-review" {
		t.Fatalf("a rule that named its resolver has its gate owned by %q", got)
	}
}

func addRule(t *testing.T, ts *httptest.Server, doc string) {
	t.Helper()
	resp, err := httpPost(ts.URL+"/charter", "application/tony", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("trigger add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("trigger add = %s", resp.Status)
	}
}

// waitOwner waits for a question raised by origin and returns the resolver that owns it.
// It waits for the OWNER, not merely for the question. Those are two writes — the question is
// posted `pending` and the standing layer takes it a moment later — so returning the owner the
// moment the question appears is reading the first write and asserting about the second. Every
// caller wants a resolver, and under load this returned "" for one that arrived milliseconds
// after (git-issue nkptp9b3h12ksx3kg9n0).
func waitOwner(t *testing.T, srv *Server, origin string) string {
	t.Helper()
	for range 400 {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return mirror.IsQuestion(ref)
		}) {
			p := entity.ToObj(ent.Payload)
			if p["origin"] == origin && p["status"] == mirror.QuestionPending {
				if owner, _ := p["resolver"].(string); owner != "" {
					return owner
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never posed a question an owner took", origin)
	return ""
}

// waitFor waits for one named cover and returns them all.
//
// By name rather than by count: a count is a claim about everything in the store, so it fails
// for reasons that have nothing to do with what is being asserted — and under load it fails
// intermittently, which is worse than not asserting at all.
func waitFor(t *testing.T, srv *Server, id string) map[string]map[string]any {
	t.Helper()
	var got map[string]map[string]any
	for range 400 {
		if got = covers(t, srv); got[id] != nil {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no cover %q after waiting: %v", id, got)
	return nil
}

// covering counts the covers whose gate stream is this thing.
func covering(all map[string]map[string]any, stream string) int {
	var n int
	for _, c := range all {
		if c["stream"] == stream {
			n++
		}
	}
	return n
}

func covers(t *testing.T, srv *Server) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, ent := range entity.Below(srv.Store, resolver.IsCover, 1, entity.VerseRoot, resolver.CoverSystem) {
		out[ent.Ref.Name()] = entity.ToObj(ent.Payload)
	}
	return out
}

// A cover follows its STREAM, not its resolver: one resolver covering a subject and the runs
// about it has one cover per gate stream, each living exactly as long as that thing.
//
// That is also what carrying a cursor pair means here — two positions as two records rather
// than two fields, so there is no place for a merged order to be invented and no code that
// could be tempted to invent one. Which kind of stream a cover reads follows from the stream:
// one under `task:instance` is a run, and anything else is a subject.
func TestACoverFollowsItsStream(t *testing.T) {
	srv := askingServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	addRule(t, ts, `{
		name: guard,
		condition: {subject: {path: "change.pr.*", value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)

	// Three firings about one subject — the rule re-fires on every commit that touches it.
	pr(t, srv, "1", "payments")
	waitOwner(t, srv, "guard")
	for range 2 {
		entitytest.Set(t, srv.Store, entity.RefAt("change", "pr", "1"),
			entity.Obj{"area": "payments", "touched": time.Now().UnixNano()}.Payload())
	}
	time.Sleep(150 * time.Millisecond)

	got := covers(t, srv)
	spine := got["payments-review@"+mirror.StreamKey(entity.RefAt("change", "pr", "1"))]
	if spine == nil || spine["stream"] != `change.pr."1"` {
		t.Fatalf("the spine does not cover the subject: %v", got)
	}
	// One spine for the subject however many times its rule fired: a cover lives for the
	// subject, not per firing, which is the spawn-answer-die shape this design avoids.
	if n := covering(got, `change.pr."1"`); n != 1 {
		t.Fatalf("three firings about one subject left %d covers on it: %v", n, got)
	}

	// A run about that same subject, whose gate the resolver owns, gets its own cover
	// beside the spine — a different stream, a different grain, a different position.
	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	waitQuestion(t, srv, "run-1")
	// waitFor, not covers: the question and the cover are written by different paths,
	// so a question that has arrived does not mean the cover beside it has. Reading once
	// here failed about one run in five of the whole package — never alone, only under the
	// load of its neighbours, which is the signature of waiting for the wrong thing.
	got = waitFor(t, srv, "payments-review@run-1")
	fine := got["payments-review@run-1"]
	if fine["stream"] != "verse.task.instance.run-1" {
		t.Fatalf("the run's cover does not cover the run: %v", got)
	}
	if spine["resolver"] != fine["resolver"] {
		t.Fatalf("the two covers belong to different resolvers: %v / %v", spine, fine)
	}

	// And a second subject is a second spine, not a second entry on the first.
	pr(t, srv, "2", "payments")
	got = waitFor(t, srv, "payments-review@"+mirror.StreamKey(entity.RefAt("change", "pr", "2")))
	if covering(got, `change.pr."2"`) != 1 {
		t.Fatalf("the second subject got no spine of its own: %v", got)
	}
	// …and the first subject's spine is untouched by it.
	if covering(got, `change.pr."1"`) != 1 {
		t.Fatalf("a second subject disturbed the first's spine: %v", got)
	}
}

// Minting is create-only: a question arriving for something already covered must not retire
// the progress of an answerer that is reading towards it.
func TestCoveringAgainDoesNotResetAPosition(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	waitQuestion(t, srv, "run-1")
	ref := resolver.CoverRef("payments-review", "run-1")
	// Wait for the MINT, not just the question. The question is written first and the cover
	// is minted right after it, so a position posted on the strength of the question alone
	// races the mint that is meant to be tested against — and this test is about what a mint
	// does to a position that is ALREADY there.
	waitFor(t, srv, ref.Name())
	resp := postTony(t, ts.URL+"/resolvers/cover/"+ref.Name()+"/position",
		api.Position{Position: 9, Notes: "read this far"})
	resp.Body.Close()

	// A second run about the same subject, and a second question on the first run.
	spawnFor(t, ts, "run-2", `change.pr."1"`, "")
	waitQuestion(t, srv, "run-2")
	mirror.Cover(srv.Store, "verse.resolvers.payments-review", "payments-review", mirror.Ref("run-1"))

	e, _, _ := srv.Store.Get(ref)
	got := entity.ToObj(e.Payload)
	if got["position"] != int64(9) || got["notes"] == nil {
		t.Fatalf("covering again reset the cover to %v: %+v", got["position"], got)
	}
}

func removeResolverAt(t *testing.T, url, ref string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url+"/resolvers/"+ref, nil)
	req.Header.Set(api.PrincipalHeader, tester)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %s", resp.Status)
	}
}

// Removing a resolver MOVES what it owned rather than stranding it.
//
// Ownership is stamped when a question is posed, so a removed resolver would otherwise leave
// questions naming something that no longer exists — answerable by nobody, and listed on a
// docket as though somebody were coming. Allowing overlap is what makes this a change of owner
// and not only a loss of one: removing the first name promotes the second.
func TestRemovingAResolverReRoutesWhatItOwned(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	installResolver(t, ts.URL, standing("bravo", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")
	if got := waitOwned(t, srv, qid)["resolver"]; got != "verse.resolvers.alpha" {
		t.Fatalf("owned by %v before removal, want alpha", got)
	}

	removeResolverAt(t, ts.URL, "alpha")

	got := question(t, srv, qid)
	if got["resolver"] != "verse.resolvers.bravo" {
		t.Fatalf("re-owned by %v, want the next matching resolver", got["resolver"])
	}
	// The handover is readable rather than inferred.
	if got["reownedFrom"] != "verse.resolvers.alpha" || got["reownedAt"] == nil {
		t.Fatalf("the handover left no trace: %+v", got)
	}
	// And it is still open — a change of owner is not an answer.
	if got["status"] != mirror.QuestionPending {
		t.Fatalf("removal settled the question: %+v", got)
	}
	// Its covers are gone: machinery for a reader that no longer exists.
	for id, c := range covers(t, srv) {
		if c["resolver"] == "verse.resolvers.alpha" {
			t.Fatalf("the removed resolver still has a cover %q", id)
		}
	}
}

// With nobody left to take it, the question goes to ⊤ — the docket a person reads — rather
// than to a name that no longer resolves.
func TestRemovingTheLastResolverSendsAQuestionToTheDocket(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", `change.pr."1"`, "")
	qid := waitQuestion(t, srv, "run-1")

	cover := resolver.CoverRef("alpha", "run-1")
	removeResolverAt(t, ts.URL, "alpha")

	got := question(t, srv, qid)
	if got["resolver"] != nil {
		t.Fatalf("owned by %v after its only resolver went, want nobody", got["resolver"])
	}
	if got["reownedFrom"] != "verse.resolvers.alpha" {
		t.Fatalf("the handover left no trace: %+v", got)
	}
	// `verse gate` still lists it: unowned is not unasked, and ⊤ is where it belongs.
	var listed bool
	for _, g := range docket(t, srv) {
		if g["id"] == qid {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("the question left the docket when its resolver did: %v", docket(t, srv))
	}

	// A verdict from the removed resolver loses on the ownership check that already exists —
	// no new rule, and its cover is gone besides.
	resp := postTony(t, ts.URL+"/resolvers/cover/"+cover.Name()+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a verdict from a removed resolver = %s, want 404", resp.Status)
	}
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("it answered anyway: %+v", q)
	}
}
