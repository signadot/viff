package versed

import (
	"context"
	"github.com/signadot/verse/api"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// A SCOPE'S OWN CHARTER SURVIVES A RESTART, the way baseline's does.
//
// `verse c -a --scope me` writes the rule into the scope — it is durable, it is listed,
// and `verse data trigger.rule.x --scope me` reads it back. What it did not do is RUN
// after a restart: a scope's engine is created on demand and never restored anything, so the
// dev came back to a scope holding a written-down charter that nothing was interpreting. No
// error anywhere; the rule was simply not installed in the engine that would have fired it.
//
// The restart is two servers over one logd, as TestStartRestoresTheCharter does it: the first
// server's memory is gone, and only what the store holds crosses the gap.
func TestAScopesOwnCharterSurvivesARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	firstHTTP := httptest.NewServer(first.Handler())
	defer firstHTTP.Close()
	// The first server's own lifetime, CANCELLED before the second starts. Without that it
	// is not a restart at all: the scope engine the first install created is still watching in
	// this process, and it — not the restored charter — is what advances the entity.
	firstCtx, down := context.WithCancel(t.Context())
	first.Start(firstCtx)

	addScopedRule(t, firstHTTP, "me", `{
		name: scoped-review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	down()
	first.stopAllIOs()

	// Restart: a second server over the same substrate, and a scope engine created fresh.
	store := entitytest.StoreAt(t, addr)
	second := serverWith(t, runnerdtest.Scripted, store)
	second.Start(t.Context())
	secondHTTP := httptest.NewServer(second.Handler())
	defer secondHTTP.Close()

	// The dev comes back and installs a trigger into the scope, which is
	// what creates the scope's engine on the new server — and the moment there is an engine,
	// the charter the dev left behind belongs in it.
	//
	// A scope with nobody connected stays inert, which is the remaining half of this: a
	// restart cannot enumerate the scopes that have a charter, because a scope layer is
	// invisible from baseline and nothing registers one. See docs/reference/scopes.md.
	addScopedRule(t, secondHTTP, "me", `{
		name: connect-probe,
		condition: {subject: {path: "never.never.*"}},
		action: {patch: {seen: true}},
	}`)

	// A dev's write into their scope, which the restored rule should act on.
	scoped := store.Scope("me")
	ref := entity.RefAt("demo", "pr", "1")
	// The dev's, so that their rule — which acts as them — may advance it.
	entitytest.Set(t, asTester(second).Scope("me"), ref, entity.Obj{"stage": "open"}.Payload())

	deadline := time.After(3 * time.Second)
	for {
		if e, ok, _ := scoped.Get(ref); ok && entity.Str(e.Payload, "stage") == "reviewed" {
			break
		}
		select {
		case <-deadline:
			e, _, _ := scoped.Get(ref)
			t.Fatalf("%s did not advance in the scope after the restart — the scope's own rule "+
				"is written down but nothing is running it; have %v", ref, e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
	// …and baseline is untouched: restoring a scope's charter must not install it at baseline.
	if got := userRules(second.Eng); len(got) != 0 {
		t.Fatalf("baseline charter = %v; a scoped rule was restored into baseline", got)
	}
}

func addScopedRule(t *testing.T, ts *httptest.Server, scope, doc string) {
	t.Helper()
	resp, err := httpPost(ts.URL+"/charter?scope="+scope, "application/tony", strings.NewReader(doc))
	if err != nil {
		t.Fatalf("scoped trigger add: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("scoped trigger add = %s", resp.Status)
	}
}

// BASELINE'S RULES ARE NOT INSTALLED INTO A SCOPE, which is the boundary the restore must not
// quietly cross. A scoped store reads baseline layered underneath, so the charter listing a
// scope sees includes rules the dev never installed — and a restore that took them at face
// value would replay the whole baseline charter inside one scope: every entity the
// scope inherits, seeding a firing, spawning into the scope.
//
// Whether a scope SHOULD run baseline's rules is open (docs/reference/scopes.md). This pins
// what it does today, so the answer is chosen rather than fallen into.
func TestAScopeDoesNotInheritBaselinesCharter(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.Start(t.Context())

	addRule(t, ts, `{
		name: baseline-review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: reviewed}},
	}`)
	addScopedRule(t, ts, "me", `{
		name: scoped-only,
		condition: {subject: {path: "demo.bug.*", value: {stage: open}}},
		action: {patch: {stage: triaged}},
	}`)

	eng := srv.engFor("me")
	if eng == nil {
		t.Fatal("no engine for the scope after a scoped install")
	}
	var names []string
	for _, tr := range eng.Triggers() {
		names = append(names, tr.Name)
	}
	if len(names) != 1 || names[0] != "scoped-only" {
		t.Fatalf("the scope's charter = %v; want only the rule installed into it", names)
	}
	// The baseline rule is visible THROUGH the scope as an entity — the listing is not empty,
	// the restore is what passes over it.
	if _, ok, _ := srv.Store.Scope("me").Get(trigger.CharterRef("baseline-review")); !ok {
		t.Fatal("the baseline rule is not visible through the scope — this test proves nothing")
	}
}

// A scope may SHADOW a baseline rule: install its own under that name. The inherited one is
// not removed first and cannot be — a removal from the scope is refused as baseline's
// (owns.go) — and needs no removing, since the scope's engine holds none of baseline's. The
// install lands in the scope's own layer, so after a restart the scope restores its own and
// baseline still holds its.
func TestAScopeShadowsABaselineRuleAcrossARestart(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	firstHTTP := httptest.NewServer(first.Handler())
	defer firstHTTP.Close()
	firstCtx, down := context.WithCancel(t.Context())
	first.Start(firstCtx)

	addRule(t, firstHTTP, `{
		name: review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: baseline-said-so}},
	}`)
	// The dev's first move, refused: the inherited rule is baseline's, not the scope's to drop.
	resp, err := http.NewRequest(http.MethodDelete, firstHTTP.URL+"/charter/review?scope=me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Set(api.PrincipalHeader, tester)
	del, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("scoped remove: %v", err)
	}
	msg, _ := io.ReadAll(del.Body)
	del.Body.Close()
	if del.StatusCode != http.StatusConflict || !strings.Contains(string(msg), "baseline's") {
		t.Fatalf("removing an inherited rule from a scope = %s %q, want 409 naming baseline", del.Status, msg)
	}
	// The dev's move: write their own under its name.
	addScopedRule(t, firstHTTP, "me", `{
		name: review,
		condition: {subject: {path: "demo.pr.*", value: {stage: open}}},
		action: {patch: {stage: mine}},
	}`)
	down()
	first.stopAllIOs()

	store := entitytest.StoreAt(t, addr)
	second := serverWith(t, runnerdtest.Scripted, store)
	second.Start(t.Context())
	secondHTTP := httptest.NewServer(second.Handler())
	defer secondHTTP.Close()
	addScopedRule(t, secondHTTP, "me", `{
		name: connect-probe,
		condition: {subject: {path: "never.never.*"}},
		action: {patch: {seen: true}},
	}`)

	// Baseline restored its own…
	if got := userRules(second.Eng); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("baseline charter after restart = %v", got)
	}
	// …and the scope restored the dev's, once, under the same name.
	eng := second.engFor("me")
	var scoped []string
	for _, tr := range eng.Triggers() {
		scoped = append(scoped, tr.Name)
	}
	if len(scoped) != 2 { // review (the dev's) + connect-probe
		t.Fatalf("the scope's charter after restart = %v; want the shadow and the probe", scoped)
	}
	ref := entity.RefAt("demo", "pr", "1")
	entitytest.Set(t, asTester(second).Scope("me"), ref, entity.Obj{"stage": "open"}.Payload())
	deadline := time.After(3 * time.Second)
	for {
		e, ok, _ := store.Scope("me").Get(ref)
		if ok && entity.Str(e.Payload, "stage") == "mine" {
			return
		}
		if ok && entity.Str(e.Payload, "stage") == "baseline-said-so" {
			t.Fatal("baseline's rule fired in the scope — the shadow did not survive the restart")
		}
		select {
		case <-deadline:
			t.Fatalf("the shadowing rule did not fire in the scope; have %v", e.Payload)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
