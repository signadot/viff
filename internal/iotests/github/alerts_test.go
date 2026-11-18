package github_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/entity/entitytest"
	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/io/github"
)

// alertAsks is how many Dependabot alert requests a repository has had.
func (f *fakeGitHub) alertAsks(repo string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[repo].alertAsks
}

// coversAnAlert is whether any cover of a reflection reaches an alert of acme/app, open or settled.
func coversAnAlert(rn verseio.Reflection) bool {
	for _, state := range []string{"open", "fixed"} {
		e := entity.Entity{Ref: entity.RefAt("github", "acme", "app", "alert", "5"),
			Payload: entity.Obj{"repo": "acme/app", "number": 5, "state": state}.Payload()}
		for _, c := range rn.Covers {
			if c.MatchEntity(e) {
				return true
			}
		}
	}
	return false
}

// DEPENDABOT ALERTS ARE ISSUES' SHAPE: the open ones covered complete, the settled ones (fixed,
// dismissed, auto_dismissed) as a window of the newest -settled by update. What falls out of the
// window is tombstoned, and the window never reaches an open alert.
func TestAlertsAreTwoCoversAndTheSettledWindowAgesOut(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	open := r.alert(1, "open", "2026-01-01T00:00:00Z")
	open.cve, open.scope, open.relationship = "CVE-2026-0001", "runtime", "direct"
	r.alert(2, "fixed", "2026-01-02T00:00:00Z")
	r.alert(3, "dismissed", "2026-01-03T00:00:00Z")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true, Settled: 2})
	rf := verseio.NewReflector(src, store, 0)
	pass := func() {
		t.Helper()
		if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
			t.Fatal(err)
		}
	}
	pass()

	if got := src.AlertRef("acme/app", 1).String(); got != `github.acme.app.alert."1"` {
		t.Fatalf("alert ref = %s", got)
	}
	a := entity.ToObj(mustGet(t, store, src.AlertRef("acme/app", 1)))
	for k, want := range map[string]any{
		"repo": "acme/app", "state": "open", "severity": "high", "ecosystem": "npm",
		"package": "left-pad", "manifest": "package-lock.json", "relationship": "direct",
		"scope": "runtime", "vulnerableRange": "< 1.3.0", "ghsa": "GHSA-0000-0000-1",
		"cve": "CVE-2026-0001", "summary": "Prototype pollution in left-pad",
		"url":       "https://github.com/x/y/security/dependabot/1",
		"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
	} {
		if a[k] != want {
			t.Errorf("%s = %v, want %v", k, a[k], want)
		}
	}
	if fmt.Sprint(a["number"]) != "1" {
		t.Errorf("number = %v", a["number"])
	}
	// No patched version is no `patched`, and an open alert says nothing of fixing or dismissing.
	for _, k := range []string{"patched", "fixedAt", "dismissedAt", "dismissedReason",
		"dismissedComment", "autoDismissedAt"} {
		if v, ok := a[k]; ok {
			t.Errorf("an open alert with no patched version carries %s: %v", k, v)
		}
	}
	d := entity.ToObj(mustGet(t, store, src.AlertRef("acme/app", 3)))
	if d["state"] != "dismissed" || d["dismissedReason"] != "tolerable_risk" || d["dismissedAt"] != "2026-01-03T00:00:00Z" {
		t.Errorf("a dismissed alert: %v", d)
	}
	if d["dismissedComment"] != "Only reachable in tests.\nNot shipped." {
		t.Errorf("a dismissed alert's comment = %q", d["dismissedComment"])
	}
	if _, ok := d["cve"]; ok {
		t.Errorf("an alert with no CVE carries cve: %v", d["cve"])
	}
	if entity.Str(mustGet(t, store, src.AlertRef("acme/app", 2)), "fixedAt") != "2026-01-02T00:00:00Z" {
		t.Error("a fixed alert carries no fixedAt")
	}

	// Two newer settled alerts push the older two out of the window.
	r.alert(4, "auto_dismissed", "2026-01-04T00:00:00Z")
	r.alert(5, "fixed", "2026-01-05T00:00:00Z")
	pass()
	for _, n := range []int{2, 3} {
		if _, ok, _ := store.Get(src.AlertRef("acme/app", n)); ok {
			t.Errorf("alert %d fell out of the settled window and was not tombstoned", n)
		}
	}
	for _, n := range []int{1, 4, 5} {
		if _, ok, _ := store.Get(src.AlertRef("acme/app", n)); !ok {
			t.Errorf("alert %d is missing", n)
		}
	}
	// An auto-dismissed alert has no dismissed_at, so it says when it left open by its own field.
	auto := entity.ToObj(mustGet(t, store, src.AlertRef("acme/app", 4)))
	if auto["autoDismissedAt"] != "2026-01-04T00:00:00Z" {
		t.Errorf("an auto-dismissed alert carries autoDismissedAt %v", auto["autoDismissedAt"])
	}
	if _, ok := auto["dismissedAt"]; ok {
		t.Errorf("an auto-dismissed alert carries dismissedAt %v", auto["dismissedAt"])
	}
}

// An alert that is fixed stays where it is and says so: it moves from the open half into the
// settled one in place, which is what lets a charter see it go.
func TestAnAlertMovesOpenToFixedInPlace(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	a := r.alert(7, "open", "2026-01-01T00:00:00Z")
	store := entitytest.Store(t)
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true})
	rf := verseio.NewReflector(src, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	ref := src.AlertRef("acme/app", 7)
	if entity.Str(mustGet(t, store, ref), "state") != "open" {
		t.Fatal("the open alert was not reflected")
	}

	a.state, a.updated, a.patched = "fixed", "2026-01-02T00:00:00Z", "1.3.0"
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	got := entity.ToObj(mustGet(t, store, ref))
	if got["state"] != "fixed" || got["fixedAt"] != "2026-01-02T00:00:00Z" || got["patched"] != "1.3.0" {
		t.Fatalf("the fixed alert = %v", got)
	}
}

// The open set is read to its end BY CURSOR, past a page, and refused over -max-open rather than
// cut short. A warm pass answers the same: the first page from the cache, where a 304 carries no
// Link and the next page comes from the one cached with the body, and the later pages afresh,
// since a page keyed by a moving cursor is never cached and so never asked for conditionally.
func TestOpenAlertsAreReadByCursorAndRefusedOverMaxOpen(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	for n := 1; n <= 150; n++ {
		r.alert(n, "open", "2026-01-01T00:00:00Z")
	}
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true})
	count := func(rn verseio.Reflection) int {
		n := 0
		for ref := range payloads(rn) {
			if strings.Contains(ref, ".alert.") {
				n++
			}
		}
		return n
	}
	for _, pass := range []string{"cold", "warm"} {
		rn, err := src.Reflect(context.Background(), entity.Ref{})
		if err != nil {
			t.Fatal(err)
		}
		if got := count(rn); got != 150 {
			t.Fatalf("a %s pass reflected %d of 150 open alerts", pass, got)
		}
	}
	f.mu.Lock()
	first, later := r.alertFirstETags, r.alertLaterETags
	f.mu.Unlock()
	if first == 0 {
		t.Error("the warm pass revalidated no first page: the first page is not cached")
	}
	if later != 0 {
		t.Errorf("a page past the first was asked for conditionally %d times, so it was cached "+
			"under a cursor that moves, and the cache grows by a page each time it does", later)
	}

	var logged []string
	small := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true, MaxOpen: 120,
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	rn, err := small.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatalf("too many open alerts should drop the alerts, not fail the pass: %v", err)
	}
	if got := count(rn); got != 0 {
		t.Fatalf("over -max-open, %d alerts were answered: a partial list under a complete cover", got)
	}
	if coversAnAlert(rn) {
		t.Fatal("over -max-open, the alerts are still covered")
	}
	if payloads(rn)[`github.acme.app.issue."1"`] == nil {
		t.Fatal("refusing the alerts lost the rest of the repository")
	}
	if !strings.Contains(strings.Join(logged, "\n"), "more than 120 open Dependabot alerts") {
		t.Fatalf("the refusal was not reported: %v", logged)
	}
}

// A TARGETED PASS COVERS ONE ALERT, the mass-deletion trap one kind over, and an alert GitHub no
// longer has is covered and absent, which tombstones it.
func TestATargetedAlertPassCoversOneAndTombstonesAGoneOne(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.alert(1, "open", "2026-01-01T00:00:00Z")
	r.alert(2, "open", "2026-01-02T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true})

	rn, err := src.Reflect(context.Background(), src.AlertRef("acme/app", 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rn.Entities) != 1 || rn.Entities[0].Ref != src.AlertRef("acme/app", 2) {
		t.Fatalf("a targeted alert pass answered %v", payloads(rn))
	}
	if len(rn.Covers) != 1 {
		t.Fatalf("a targeted alert pass covers %d patterns, want one", len(rn.Covers))
	}
	other := entity.Entity{Ref: src.AlertRef("acme/app", 1),
		Payload: entity.Obj{"repo": "acme/app", "number": 1, "state": "open"}.Payload()}
	if rn.Covers[0].MatchEntity(other) {
		t.Fatal("a pass about alert 2 covers alert 1, so converge would tombstone it")
	}
	self := entity.Entity{Ref: src.AlertRef("acme/app", 2),
		Payload: entity.Obj{"repo": "acme/app", "number": 2, "state": "open"}.Payload()}
	if !rn.Covers[0].MatchEntity(self) {
		t.Fatal("a pass about alert 2 does not cover it")
	}

	r.alerts = r.alerts[:1]
	gone, err := src.Reflect(context.Background(), src.AlertRef("acme/app", 2))
	if err != nil {
		t.Fatalf("a gone alert should be an answer, not an error: %v", err)
	}
	if len(gone.Entities) != 0 || len(gone.Covers) != 1 {
		t.Fatalf("a gone alert answered %d entities under %d covers", len(gone.Entities), len(gone.Covers))
	}
}

// A REFUSED READ IS NOT "NO ALERTS". A credential without Dependabot alerts read, or a
// repository with them switched off, answers 403; the pass drops the alert covers with the
// alerts, says why, and finishes, so what stands stays. The targeted pass does the same.
func TestARefusedAlertReadLeavesWhatStands(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.issue(1, "an issue", "open", "2026-01-01T00:00:00Z")
	r.alert(5, "open", "2026-01-01T00:00:00Z")
	store := entitytest.Store(t)
	var logged []string
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true,
		Log: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	rf := verseio.NewReflector(src, store, 0)
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatal(err)
	}
	ref := src.AlertRef("acme/app", 5)
	if _, ok, _ := store.Get(ref); !ok {
		t.Fatal("the alert was not reflected in the first place")
	}

	r.alertsRefused = true
	r.issue(2, "a newer issue", "open", "2026-01-02T00:00:00Z")
	if err := rf.Reconcile(context.Background(), entity.Ref{}); err != nil {
		t.Fatalf("a refused alert read should not fail the pass: %v", err)
	}
	if _, ok, _ := store.Get(ref); !ok {
		t.Fatal("a refused alert read was taken as no alerts and deleted the alert")
	}
	if _, ok, _ := store.Get(src.ItemRef("issue", "acme/app", 2)); !ok {
		t.Fatal("a refused alert read stopped the rest of the pass")
	}
	if !strings.Contains(strings.Join(logged, "\n"), "Dependabot alerts are disabled") {
		t.Fatalf("the refusal was not reported as one: %v", logged)
	}

	one, err := src.Reflect(context.Background(), ref)
	if err != nil {
		t.Fatalf("a refused targeted read should not fail: %v", err)
	}
	if len(one.Covers) != 0 || len(one.Entities) != 0 {
		t.Fatalf("a refused targeted read answered %d covers and %d entities, want nothing",
			len(one.Covers), len(one.Entities))
	}
}

// -alerts off, the default, reads nothing and costs no request: no alert is asked about on a full
// pass or a targeted one, nothing is covered, and a dependabot_alert delivery hints nothing.
func TestAlertsOffReadNothingAndCostNoRequest(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.alert(5, "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	rn, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	for ref := range payloads(rn) {
		if strings.Contains(ref, ".alert.") {
			t.Fatalf("alerts off reflected %s", ref)
		}
	}
	if coversAnAlert(rn) {
		t.Fatal("alerts off covers alerts, which would tombstone what an earlier -alerts wrote")
	}
	one, err := src.Reflect(context.Background(), src.AlertRef("acme/app", 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Covers) != 0 || len(one.Entities) != 0 {
		t.Fatalf("alerts off, a targeted pass answered %d covers and %d entities",
			len(one.Covers), len(one.Entities))
	}
	if n := f.alertAsks("acme/app"); n != 0 {
		t.Fatalf("alerts off asked GitHub about alerts %d times", n)
	}

	h := &hinted{}
	post(t, src.Webhook(hookSecret, h.hint), "dependabot_alert", map[string]any{
		"repository": map[string]any{"full_name": "acme/app"}, "alert": map[string]any{"number": 5}})
	if len(h.calls) != 0 {
		t.Fatalf("alerts off, a dependabot_alert delivery hinted %v", h.calls)
	}
}

// A dependabot_alert delivery hints the alert it is about.
func TestADependabotAlertDeliveryHintsTheAlert(t *testing.T) {
	f := newFake()
	f.repo("acme/app")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true})
	h := &hinted{}
	resp := post(t, src.Webhook(hookSecret, h.hint), "dependabot_alert", map[string]any{
		"action": "fixed", "repository": map[string]any{"full_name": "acme/app"},
		"alert": map[string]any{"number": 5, "state": "fixed"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if refs := h.only(t); len(refs) != 1 || refs[0].String() != `github.acme.app.alert."5"` {
		t.Fatalf("hinted %v, want the alert", refs)
	}
}

// A full pass and a targeted pass agree about an alert, every field of it, so the two never
// rewrite one another's answer.
func TestAFullAndATargetedAlertPassAgree(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	rich := r.alert(1, "dismissed", "2026-01-02T00:00:00Z")
	rich.patched, rich.cve, rich.scope, rich.relationship = "1.3.0", "CVE-2026-0001", "development", "transitive"
	r.alert(2, "open", "2026-01-01T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}, Alerts: true})

	full, err := src.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatal(err)
	}
	whole := payloads(full)
	for _, n := range []int{1, 2} {
		ref := src.AlertRef("acme/app", n)
		one, err := src.Reflect(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		got, want := payloads(one)[ref.String()], whole[ref.String()]
		if got == nil || want == nil {
			t.Fatalf("%s: full=%v targeted=%v", ref, want, got)
		}
		if !entity.Equal(got.Payload(), want.Payload()) {
			t.Fatalf("%s differs between a full pass and a targeted one:\n  full     %v\n  targeted %v",
				ref, want, got)
		}
	}
	if p := whole[`github.acme.app.alert."1"`]; p["patched"] != "1.3.0" || p["scope"] != "development" {
		t.Fatalf("the rich alert = %v", p)
	}
}
