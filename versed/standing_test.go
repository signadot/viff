package versed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/resolver"
)

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
