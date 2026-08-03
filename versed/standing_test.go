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
	resp := postTony(t, ts.URL+"/processes", map[string]any{
		"id": id, "task": "true", "subject": subject, "resolver": resolverRef,
	})
	defer resp.Body.Close()
	// Loudly: a swallowed status is how a spawn bound to a binding that does not exist went
	// unnoticed, and a test whose run never started fails three assertions later as a timeout.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("spawn %s: %s: %s", id, resp.Status, strings.TrimSpace(string(raw)))
	}
}

func standing(name, area string) string {
	return `{name: ` + name + `, kind: security, condition: {subject: {system: change, kind: pr,` +
		` value: {area: ` + area + `}}}, method: {driver: url, url: "https://x"}}`
}

func pr(t *testing.T, srv *Server, id, area string) entity.Ref {
	t.Helper()
	ref := entity.Ref{System: "change", Kind: "pr", ID: id}
	entitytest.Set(t, srv.Store, ref, entity.Obj{"area": area}.Payload())
	return ref
}

// Several standing bindings may cover one subject, and the first BY NAME owns its questions.
//
// Overlap is ordered rather than forbidden. Disjointness of two predicates is undecidable in
// general, so a refusal at install has to be conservative, and a conservative refusal tells
// the operator with `{area: payments}` and `{area: infra}` no for a reason that is not true of
// their case. The design's requirement survives — a question still has exactly one answerer at
// its layer — by construction rather than by prohibition.
func TestTheFirstBindingByNameOwnsASubject(t *testing.T) {
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

// A binding covers what its condition selects, and nothing else.
func TestAStandingBindingCoversWhatItSelects(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	if _, ok := resolver.Owner(srv.Store, pr(t, srv, "1", "payments")); !ok {
		t.Fatal("a payments PR is covered by nobody")
	}
	if got, ok := resolver.Owner(srv.Store, pr(t, srv, "2", "infra")); ok {
		t.Fatalf("an infra PR is covered by %q", got.Name)
	}
	// A different kind entirely is untouched.
	other := entity.Ref{System: "demo", Kind: "release", ID: "1"}
	entitytest.Set(t, srv.Store, other, entity.Obj{"area": "payments"}.Payload())
	if got, ok := resolver.Owner(srv.Store, other); ok {
		t.Fatalf("a release is covered by %q", got.Name)
	}
}

// Installing REPORTS what a binding may be ordered against, and refuses nothing.
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
		t.Fatalf("the first binding overlaps %v", out["overlaps"])
	}

	second := installResolver(t, ts.URL, standing("bravo", "infra"))
	if second.StatusCode != http.StatusCreated {
		t.Fatalf("a second binding over one kind = %s, want 201 — overlap is ordered, not refused",
			second.Status)
	}
	decodeResp(t, second, &out)
	second.Body.Close()
	over, _ := out["overlaps"].([]any)
	if len(over) != 1 || over[0] != "resolvers:security:alpha" {
		t.Fatalf("overlaps = %v, want the binding it shares a kind with", out["overlaps"])
	}

	// A binding over a different kind shares nothing and is told so by silence.
	third := installResolver(t, ts.URL,
		`{name: charlie, kind: cost, condition: {subject: {system: demo, kind: release}}, `+
			`method: {driver: url, url: "https://x"}}`)
	decodeResp(t, third, &out)
	third.Body.Close()
	if out["overlaps"] != nil {
		t.Fatalf("a binding over another kind reports %v", out["overlaps"])
	}
}

// Ownership follows from what DECLARED it, layer by layer, and absent layers are skipped.
//
//	a run's question    → the spawn's resolver, else the standing binding covering its flow, else ⊤
//	a firing's question → the rule's gating.resolver, else the standing binding, else ⊤
//
// That is a rule about layers and not a ranking between two matches. A standing binding is not
// a wider claim on a named one's questions; it answers a different set — which is why a run
// whose spawn names a resolver leaves its standing binding nothing to do rather than competing
// with it.
func TestTheLayerRuleDecidesWhoOwnsAQuestion(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	// A run about a covered subject, whose spawn ALSO names one: the named layer owns it.
	// Installed, because a spawn naming a binding that does not exist is refused — it would
	// shadow the standing layer below with something that cannot answer.
	installResolver(t, ts.URL, `{name: security-review, kind: security, byRef: true,`+
		` method: {driver: url, url: "https://x"}}`).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "named", "change:pr:1", "resolvers:security:security-review")
	if got := question(t, srv, waitQuestion(t, srv, "named"))["resolver"]; got != "resolvers:security:security-review" {
		t.Fatalf("a named run is owned by %v, want the binding its spawn named", got)
	}

	// The same run with no name: the standing binding covering its flow owns it.
	spawnFor(t, ts, "unnamed", "change:pr:1", "")
	if got := question(t, srv, waitQuestion(t, srv, "unnamed"))["resolver"]; got != "resolvers:security:payments-review" {
		t.Fatalf("an unnamed run over a covered subject is owned by %v, want the standing binding", got)
	}

	// A run about a subject nobody covers: absent, which is ⊤ — the docket a person reads.
	pr(t, srv, "2", "infra")
	spawnFor(t, ts, "uncovered", "change:pr:2", "")
	if got := question(t, srv, waitQuestion(t, srv, "uncovered"))["resolver"]; got != nil {
		t.Fatalf("an uncovered run is owned by %v, want nobody", got)
	}
}

// And the firing side of the same rule: a trigger gate is owned by the resolver its rule
// named, else by the standing binding covering the subject it is about.
func TestAFiringsGateIsOwnedByItsLayer(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()

	// A rule that names nobody: the standing binding covering the subject owns its gate.
	addRule(t, ts, `{
		name: guard,
		condition: {subject: {system: change, kind: pr, value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)
	pr(t, srv, "1", "payments")
	if got := waitOwner(t, srv, "guard"); got != "resolvers:security:payments-review" {
		t.Fatalf("a firing's gate is owned by %q, want the standing binding", got)
	}

	// A rule that names one: the named layer owns it, and the standing binding is left the
	// questions it was already answering rather than competing for these.
	addRule(t, ts, `{
		name: named,
		condition: {subject: {system: change, kind: pr, value: {area: payments}}},
		gating: {resolver: "resolvers:security:security-review", gates: [{name: sign, mode: ask}]},
		action: {patch: {signed: true}},
	}`)
	pr(t, srv, "2", "payments")
	if got := waitOwner(t, srv, "named"); got != "resolvers:security:security-review" {
		t.Fatalf("a rule that named its resolver has its gate owned by %q", got)
	}
}

func addRule(t *testing.T, ts *httptest.Server, doc string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/triggers", "application/tony", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("trigger add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("trigger add = %s", resp.Status)
	}
}

// waitOwner waits for a question raised by origin and returns the binding that owns it.
func waitOwner(t *testing.T, srv *Server, origin string) string {
	t.Helper()
	for range 400 {
		for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
			return ref.System == mirror.QuestionSystem && ref.Kind == mirror.QuestionKind
		}) {
			p := entity.ToObj(ent.Payload)
			if p["origin"] == origin && p["status"] == mirror.QuestionPending {
				owner, _ := p["resolver"].(string)
				return owner
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never posed a question", origin)
	return ""
}

// waitFor waits for one named instance and returns them all.
//
// By name rather than by count: a count is a claim about everything in the store, so it fails
// for reasons that have nothing to do with what is being asserted — and under load it fails
// intermittently, which is worse than not asserting at all.
func waitFor(t *testing.T, srv *Server, id string) map[string]map[string]any {
	t.Helper()
	var got map[string]map[string]any
	for range 400 {
		if got = instances(t, srv); got[id] != nil {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no instance %q after waiting: %v", id, got)
	return nil
}

// watching counts the instances whose origin is this thing.
func watching(all map[string]map[string]any, origin string) int {
	var n int
	for _, inst := range all {
		if inst["origin"] == origin {
			n++
		}
	}
	return n
}

func instances(t *testing.T, srv *Server) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, ent := range srv.Store.List(func(ref entity.Ref) bool {
		return ref.System == resolver.System && ref.Kind == resolver.InstanceKind
	}) {
		out[ent.Ref.ID] = entity.ToObj(ent.Payload)
	}
	return out
}

// An instance follows its ORIGIN, not its binding: one binding watching a subject and the runs
// about it has one instance per thing it watches, each living exactly as long as that thing.
//
// That is also what carrying a cursor pair means here — two positions as two records rather
// than two fields, so there is no place for a merged order to be invented and no code that
// could be tempted to invent one. Which plane an instance is on follows from its origin: one
// under `process:instance` is a run, and anything else is a subject.
func TestAnInstanceFollowsItsOrigin(t *testing.T) {
	srv := askingServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	addRule(t, ts, `{
		name: guard,
		condition: {subject: {system: change, kind: pr, value: {area: payments}}},
		gates:  [{name: approval, mode: ask}],
		action: {patch: {stage: reviewed}},
	}`)

	// Three firings about one subject — the rule re-fires on every commit that touches it.
	pr(t, srv, "1", "payments")
	waitOwner(t, srv, "guard")
	for range 2 {
		entitytest.Set(t, srv.Store, entity.Ref{System: "change", Kind: "pr", ID: "1"},
			entity.Obj{"area": "payments", "touched": time.Now().UnixNano()}.Payload())
	}
	time.Sleep(150 * time.Millisecond)

	got := instances(t, srv)
	spine := got["payments-review@change:pr:1"]
	if spine == nil || spine["origin"] != "change:pr:1" {
		t.Fatalf("the spine does not watch the subject: %v", got)
	}
	// One spine for the subject however many times its rule fired: an instance lives for the
	// subject, not per firing, which is the spawn-answer-die shape this design avoids.
	if n := watching(got, "change:pr:1"); n != 1 {
		t.Fatalf("three firings about one subject left %d instances watching it: %v", n, got)
	}

	// A run about that same subject, whose gate the binding owns, gets its own instance
	// beside the spine — a different origin, a different plane, a different position.
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	waitQuestion(t, srv, "run-1")
	got = instances(t, srv)
	fine := got["payments-review@run-1"]
	if fine == nil || fine["origin"] != "process:instance:run-1" {
		t.Fatalf("the run instance does not watch the run: %v", got)
	}
	if spine["binding"] != fine["binding"] {
		t.Fatalf("the two instances belong to different bindings: %v / %v", spine, fine)
	}

	// And a second subject is a second spine, not a second entry on the first.
	pr(t, srv, "2", "payments")
	got = waitFor(t, srv, "payments-review@change:pr:2")
	if watching(got, "change:pr:2") != 1 {
		t.Fatalf("the second subject got no spine of its own: %v", got)
	}
	// …and the first subject's spine is untouched by it.
	if watching(got, "change:pr:1") != 1 {
		t.Fatalf("a second subject disturbed the first's spine: %v", got)
	}
}

// Minting is create-only: a question arriving for something already watched must not retire
// the progress of a resolver that is reading towards it.
func TestWatchingAgainDoesNotResetAPosition(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("payments-review", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	spawnFor(t, ts, "run-1", "change:pr:1", "")
	waitQuestion(t, srv, "run-1")
	ref := resolver.InstanceRef("payments-review", "run-1")
	resp := postTony(t, ts.URL+"/resolvers/instance/"+ref.ID+"/position",
		api.Position{Position: 9, Notes: "read this far"})
	resp.Body.Close()

	// A second run about the same subject, and a second question on the first run.
	spawnFor(t, ts, "run-2", "change:pr:1", "")
	waitQuestion(t, srv, "run-2")
	mirror.Watch(srv.Store, "resolvers:security:payments-review", "payments-review", mirror.Ref("run-1"))

	e, _, _ := srv.Store.Get(ref)
	got := entity.ToObj(e.Payload)
	if got["position"] != int64(9) || got["notes"] == nil {
		t.Fatalf("watching again reset the instance to %v: %+v", got["position"], got)
	}
}

func removeResolverAt(t *testing.T, url, ref string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url+"/resolvers/"+ref, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove = %s", resp.Status)
	}
}

// Removing a binding MOVES what it owned rather than stranding it.
//
// Ownership is stamped when a question is posed, so a removed binding would otherwise leave
// questions naming something that no longer exists — answerable by nobody, and listed on a
// docket as though somebody were coming. Allowing overlap is what makes this a change of owner
// and not only a loss of one: removing the first name promotes the second.
func TestRemovingABindingReRoutesWhatItOwned(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	installResolver(t, ts.URL, standing("bravo", "payments")).Body.Close()
	pr(t, srv, "1", "payments")

	spawnFor(t, ts, "run-1", "change:pr:1", "")
	qid := waitQuestion(t, srv, "run-1")
	if got := question(t, srv, qid)["resolver"]; got != "resolvers:security:alpha" {
		t.Fatalf("owned by %v before removal, want alpha", got)
	}

	removeResolverAt(t, ts.URL, "security/alpha")

	got := question(t, srv, qid)
	if got["resolver"] != "resolvers:security:bravo" {
		t.Fatalf("re-owned by %v, want the next covering binding", got["resolver"])
	}
	// The handover is readable rather than inferred.
	if got["reownedFrom"] != "resolvers:security:alpha" || got["reownedAt"] == nil {
		t.Fatalf("the handover left no trace: %+v", got)
	}
	// And it is still open — a change of owner is not an answer.
	if got["status"] != mirror.QuestionPending {
		t.Fatalf("removal settled the question: %+v", got)
	}
	// Its instances are gone: machinery for a reader that no longer exists.
	for id, inst := range instances(t, srv) {
		if inst["binding"] == "resolvers:security:alpha" {
			t.Fatalf("the removed binding still has an instance %q", id)
		}
	}
}

// With nobody left to take it, the question goes to ⊤ — the docket a person reads — rather
// than to a name that no longer resolves.
func TestRemovingTheLastBindingSendsAQuestionToTheDocket(t *testing.T) {
	srv := askingServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	installResolver(t, ts.URL, standing("alpha", "payments")).Body.Close()
	pr(t, srv, "1", "payments")
	spawnFor(t, ts, "run-1", "change:pr:1", "")
	qid := waitQuestion(t, srv, "run-1")

	inst := resolver.InstanceRef("alpha", "run-1")
	removeResolverAt(t, ts.URL, "security/alpha")

	got := question(t, srv, qid)
	if got["resolver"] != nil {
		t.Fatalf("owned by %v after its only binding went, want nobody", got["resolver"])
	}
	if got["reownedFrom"] != "resolvers:security:alpha" {
		t.Fatalf("the handover left no trace: %+v", got)
	}
	// `verse gates` still lists it: unowned is not unasked, and ⊤ is where it belongs.
	var listed bool
	for _, g := range docket(t, srv) {
		if g["id"] == qid {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("the question left the docket when its resolver did: %v", docket(t, srv))
	}

	// A verdict from the removed binding loses on the ownership check that already exists —
	// no new rule, and its instance is gone besides.
	resp := postTony(t, ts.URL+"/resolvers/instance/"+inst.ID+"/verdicts",
		api.Verdict{ID: qid, Verdict: "proceed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a verdict from a removed binding = %s, want 404", resp.Status)
	}
	if q := question(t, srv, qid); q["status"] != mirror.QuestionPending {
		t.Fatalf("it answered anyway: %+v", q)
	}
}
