package versed

import (
	"context"
	"errors"
	"github.com/signadot/verse/runner/runnerd/runnerdtest"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/trigger"
)

// declareReads posts a package's `reads:` declaration, which a rule reading outside its own
// address needs installed first.
func declareReads(t *testing.T, url, at string, imports ...string) (int, string) {
	t.Helper()
	var q []string
	for _, im := range imports {
		q = append(q, im)
	}
	node, err := codec.ParseNode([]byte("{reads: ["+strings.Join(q, ", ")+"]}"), codec.Tony)
	if err != nil {
		t.Fatalf("parse reads: %v", err)
	}
	resp := postTony(t, url+"/charter?at="+at, node)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// installAt posts a charter into the package at an address, answering with the status so a test
// can assert a refusal as readily as an install.
func installAt(t *testing.T, url, at, src string) (int, string) {
	t.Helper()
	node, err := codec.ParseNode([]byte(src), codec.Tony)
	if err != nil {
		t.Fatalf("parse charter: %v", err)
	}
	resp := postTony(t, url+"/charter?at="+at, node)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// A charter installed at an address runs in an engine of its own and writes inside that
// address. This is the whole shape end to end: install, fire, and the entity lands.
func TestACharterPackageRunsAtItsAddress(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing a package = %d: %s", code, msg)
	}

	entitytest.Set(t, srv.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the package's rule to fire", func() bool {
		e, ok, _ := srv.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})

	// The record says which package it belongs to, so a reader can tell whose rule it is
	// without inferring it from which listing it turned up in.
	rule, ok, _ := srv.Store.Get(trigger.CharterRefAt("notes", "note-the-head"))
	if !ok {
		t.Fatal("the package's rule was not written down")
	}
	if got := entity.Str(rule.Payload, trigger.AtField); got != "notes" {
		t.Fatalf("the record says at=%q, want notes", got)
	}
}

// A package may not install a rule that writes outside its address — the isolation, refused at
// the moment somebody writes it rather than discovered when it fires.
func TestACharterPackageCannotReachOutsideItsAddress(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, msg := installAt(t, ts.URL, "notes", `{
		name: reach,
		condition: {subject: {path: "demo.pr.*"}},
		action: {patch: {tampered: true}},
	}`)
	if code == 201 {
		t.Fatal("a package installed a rule writing outside its address")
	}
	if !strings.Contains(msg, "demo.pr") || !strings.Contains(msg, "notes") {
		t.Errorf("the refusal names neither the address nor the package: %s", msg)
	}
}

// Verse's own half is nobody's to install a charter into: a package there could write the
// records verse keeps about its own running, which is the boundary P1b drew.
func TestACharterPackageMayNotBeInstalledInVersesHalf(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, msg := installAt(t, ts.URL, "verse.task", `{
		name: sneaky,
		condition: {subject: {path: "verse.task.instance.*"}},
		action: {patch: {status: done}},
	}`)
	if code == 201 {
		t.Fatal("a charter installed itself into verse's own half")
	}
	if !strings.Contains(msg, "verse") {
		t.Errorf("the refusal does not say why: %s", msg)
	}
}

// A package comes back at a restart, and comes back as ITS OWN charter rather than as part of
// baseline's — which is what the address stamped on the record is for.
//
// It is the half a scope cannot have: a scope's layer is invisible from baseline, so a
// scope waits for its owner to come back, while a package is a stamp anyone can list.
func TestACharterPackageIsRestoredAtItsAddress(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	ts.Close()
	stop()
	time.Sleep(50 * time.Millisecond)

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// The baseline charter did NOT take it: a package's rules are not baseline's, and a
	// restart that folded them in would hand one group's rules the whole verse to write in.
	for _, tr := range second.Eng.Triggers() {
		if tr.Name == "note-the-head" {
			t.Fatal("the package's rule was restored into the baseline charter")
		}
	}

	// And it runs: the package's own engine came back with it.
	entitytest.Set(t, second.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the restored package to fire", func() bool {
		e, ok, _ := second.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})
}

// charterListings counts the listings of the charter's slice a server makes through it.
type charterListings struct {
	entity.Store
	n int
}

func (c *charterListings) count(at []string) {
	if slices.Equal(at, []string{entity.VerseRoot, trigger.CharterSystem}) {
		c.n++
	}
}
func (c *charterListings) ListAt(at []string, depth int, pred func(entity.Ref) bool) []entity.Entity {
	c.count(at)
	return c.Store.ListAt(at, depth, pred)
}
func (c *charterListings) ListAtErr(at []string, depth int, pred func(entity.Ref) bool) ([]entity.Entity, error) {
	c.count(at)
	return entity.ListAtErr(c.Store, at, depth, pred)
}

// A RESTART READS THE CHARTER A FIXED NUMBER OF TIMES, however many packages it brings back:
// one listing, and every package's engine takes its own strata and rules from it. Read per
// engine, staging's restart read the whole charter twice for each of its packages, one after
// another, while every restored rule was firing.
func TestARestartReadsTheCharterOnceForEveryPackage(t *testing.T) {
	addr := entitytest.Addr(t)
	// restart stands a server up over the store, installs the packages named, stops it, and
	// answers how many charter listings its Start made and how many package rules it runs.
	restart := func(ats ...string) (int, int) {
		t.Helper()
		cl := &charterListings{Store: entitytest.StoreAt(t, addr)}
		srv := serverWith(t, runnerdtest.Scripted, cl)
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		srv.Start(ctx)
		listings := cl.n
		rules := 0
		for _, eng := range srv.packageEngines() {
			rules += len(eng.Triggers())
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		for _, at := range ats {
			if code, msg := installAt(t, ts.URL, at, `{
				name: note,
				condition: {subject: {path: "`+at+`.seen.*"}},
				action: {patch: {saw: "yes"}},
			}`); code != 201 {
				t.Fatalf("installing at %s = %d: %s", at, code, msg)
			}
		}
		return listings, rules
	}
	restart("drift", "lint", "docs")
	three, rules3 := restart("audit", "deps", "fmt")
	six, rules6 := restart()
	if rules3 != 3 || rules6 != 6 {
		t.Fatalf("restarts brought back %d and %d package rules, want 3 and 6", rules3, rules6)
	}
	if six != three {
		t.Errorf("a restart listed the charter %d times over three packages and %d over six, want the same", three, six)
	}
}

// A package reads its own address and what it DECLARES, and nothing else. The refusal names the
// line to add, because a rule reaching past its imports is a dependency somebody has not written
// down rather than a mistake in the rule.
func TestAPackageReadsOnlyWhatItDeclares(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	rule := `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`
	code, msg := installAt(t, ts.URL, "notes", rule)
	if code == 201 {
		t.Fatal("a package read a system it never declared")
	}
	if !strings.Contains(msg, "does not declare") || !strings.Contains(msg, "reads: [git.head]") {
		t.Errorf("the refusal does not say what to add: %s", msg)
	}

	// Declared, it installs — the same rule, one line of declaration apart.
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring imports = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", rule); code != 201 {
		t.Fatalf("installing after the declaration = %d: %s", code, msg)
	}
}

// THE POINT OF THE WHOLE MECHANISM: two packages that feed each other are refused at install,
// rather than throttled later in a chain that crosses two engines and belongs to neither
// charter's author.
func TestAnImportCycleBetweenPackagesIsRefused(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "a", "b"); code != 201 {
		t.Fatalf("a importing b = %d: %s", code, msg)
	}
	code, msg := declareReads(t, ts.URL, "b", "a")
	if code == 201 {
		t.Fatal("two packages reading each other installed")
	}
	if !strings.Contains(msg, "cycle") || !strings.Contains(msg, "a → b → a") {
		t.Errorf("the refusal does not show the loop: %s", msg)
	}

	// And the shape that is NOT a loop still installs: both reading one source is the ordinary
	// case, and a chain a → b → c is a dependency order rather than a cycle.
	if code, msg := declareReads(t, ts.URL, "b", "git"); code != 201 {
		t.Fatalf("b importing a source = %d: %s", code, msg)
	}
}

// A declaration is durable and comes back BEFORE the rules it allows: a restore that loaded the
// charter first would refuse the very rules it is bringing back.
func TestAPackagesImportsAreRestored(t *testing.T) {
	addr := entitytest.Addr(t)
	first := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	ctx, stop := context.WithCancel(t.Context())
	first.Start(ctx)
	ts := httptest.NewServer(first.Handler())
	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	ts.Close()
	stop()
	time.Sleep(50 * time.Millisecond)

	second := serverWith(t, runnerdtest.Scripted, entitytest.StoreAt(t, addr))
	second.Start(t.Context())

	// It runs, which is the whole assertion: the rule reads `git`, and it could only be
	// restored into an engine that had the declaration back first.
	entitytest.Set(t, second.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the restored package to fire", func() bool {
		e, ok, _ := second.Store.Get(entity.RefAt("notes", "seen", "head"))
		return ok && entity.Str(e.Payload, "saw") == "yes"
	})
}

// Narrowing must not leave a rule standing outside what its package declares — re-installing a
// charter with a line deleted is exactly how that would happen.
func TestNarrowingImportsIsRefusedWhileARuleNeedsThem(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}
	code, msg := declareReads(t, ts.URL, "notes")
	if code == 201 {
		t.Fatal("the declaration narrowed out from under an installed rule")
	}
	if !strings.Contains(msg, "note-the-head") {
		t.Errorf("the refusal does not name the rule that still needs it: %s", msg)
	}
}

// A store that cannot be READ answers the empty slice, which is what a verse holding nothing
// answers — so a listing built on `Store.List` reports "there is nothing here" for a store that
// is simply unreadable. That is how an unreplayable patch in the log presents: `verse data`
// empty, the charter tree showing no rules, every write still reporting success.
//
// The surfaces answer 503 instead. `storeUnreadable` is the store-wide question — right for a
// caller with no read of its own to report, which is what this asks about; a listing that CAN
// report its own read uses entity.ListErr, for the reason that helper gives.
func TestAnUnreadableStoreIsNotAnEmptyVerse(t *testing.T) {
	if got := storeUnreadable(entitytest.Store(t)); got != nil {
		t.Fatalf("a healthy store reports %v", got)
	}
	if got := storeUnreadable(unreadableStore{}); got == nil {
		t.Fatal("a store that cannot be read reported no error")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/state", nil)
	if !unreadable(rec, req, storeUnreadable(unreadableStore{})) {
		t.Fatal("the read failure did not answer")
	}
	if rec.Code != 503 {
		t.Errorf("answered %d, want 503 — the request is fine and the store is not", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "empty verse") {
		t.Errorf("the body does not say what would otherwise be reported: %s", rec.Body.String())
	}
}

// unreadableStore is a reader whose reads fail, which is the only way to have one: the guard in
// entity/raw.go means no write can create the real thing any more.
type unreadableStore struct{ entity.Reader }

func (unreadableStore) ReadError() error { return errors.New("replay failed at commit 2277") }

// A package comes off whole: its rules, its levels, the declaration of what it declares it reads, and the
// engine running them. Before this there was no way to remove one at all — rules went one at a
// time, and the levels, the declaration and the engine stayed behind whatever you did.
func TestAPackageCanBeUninstalled(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "notes", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	if code, msg := installAt(t, ts.URL, "notes", `{
		name: note-the-head,
		condition: {when: {path: "git.head.*"}, subject: {path: "notes.seen.head"}},
		action: {patch: {saw: "yes"}},
	}`); code != 201 {
		t.Fatalf("installing = %d: %s", code, msg)
	}

	if err := srv.removePackage(srv.Store, "notes", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, ok, _ := srv.Store.Get(trigger.CharterRefAt("notes", "note-the-head")); ok {
		t.Error("the rule outlived the package")
	}
	if _, ok, _ := srv.Store.Get(trigger.PackageRef("notes")); ok {
		t.Error("the declaration of what it declares it reads outlived the package")
	}
	if len(srv.packagesSeen()) != 0 {
		t.Errorf("the package is still listed: %v", srv.packagesSeen())
	}

	// AND IT STOPPED. A cancelled engine left in the registry would be found by the next
	// install at this address and observe nothing, which is the failure that looks like a
	// charter installing fine and never firing.
	entitytest.Set(t, srv.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	time.Sleep(200 * time.Millisecond)
	if _, ok, _ := srv.Store.Get(entity.RefAt("notes", "seen", "head")); ok {
		t.Error("the uninstalled package fired")
	}

	// What it WROTE is not its to take away, which is why nothing here asserts otherwise:
	// a charter's writes are the world's, and `verse data rm` is how they go.
}

// A charter under an address is a package of its own, so removing the one above it is refused
// until somebody says they mean it.
func TestUninstallingRefusesToSweepUpWhatIsUnderIt(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, at := range []string{"demo", "demo.pr"} {
		if code, msg := installAt(t, ts.URL, at, `{
			name: r-`+strings.ReplaceAll(at, ".", "-")+`,
			condition: {subject: {path: "demo.pr.*"}},
			action: {patch: {seen: true}},
		}`); code != 201 {
			t.Fatalf("installing at %s = %d: %s", at, code, msg)
		}
	}
	err := srv.removePackage(srv.Store, "demo", false)
	if err == nil {
		t.Fatal("removing a charter swept up the one under it")
	}
	if !strings.Contains(err.Error(), "demo.pr") || !strings.Contains(err.Error(), "recurse") {
		t.Errorf("the refusal does not name what is under it, or the way to mean it: %v", err)
	}
	if err := srv.removePackage(srv.Store, "demo", true); err != nil {
		t.Fatalf("--recurse: %v", err)
	}
	if len(srv.packagesSeen()) != 0 {
		t.Errorf("--recurse left charters behind: %v", srv.packagesSeen())
	}
}

// A CHARTER MAY BE INSTALLED AT ANY DEPTH, and writes where it is. That is the whole point of
// the entity tree: `verse-dev.drift` keeps its tip fact and its ledger of findings as two levels
// UNDER its own address, which a two-segment address could not hold — the address ended at
// `<system>.<kind>` and the confinement was the whole of it.
func TestACharterInstallsAtAnyDepthAndWritesWhereItIs(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := declareReads(t, ts.URL, "verse-dev.drift", "git"); code != 201 {
		t.Fatalf("declaring = %d: %s", code, msg)
	}
	for _, def := range []string{
		`{name: reflected, level: 0, match: {path: "git"}}`,
		`{name: tip, level: 10, match: {path: "verse-dev.drift.head"}}`,
		`{name: head-moved, stratum: reflected, condition: {subject: "git.head.verse"},
		  action: {derive: [{path: "verse-dev.drift.head", value: {sha: $subject.value.sha}}]}}`,
	} {
		if code, msg := installAt(t, ts.URL, "verse-dev.drift", def); code != 201 {
			t.Fatalf("installing %s = %d: %s", def[:24], code, msg)
		}
	}

	entitytest.Set(t, srv.Store, entity.RefAt("git", "head", "verse"), entity.Obj{"sha": "abc"}.Payload())
	waitUntil(t, "the deep package to derive", func() bool {
		return len(srv.Store.ListAt([]string{"verse-dev", "drift", "head"}, 4, nil)) > 0
	})

	// And the confinement holds at that depth: a derive outside the address does not install,
	// however deep the address is.
	code, msg := installAt(t, ts.URL, "verse-dev.drift", `{
		name: reach, stratum: tip,
		condition: {subject: {path: "verse-dev.drift.head.*"}},
		action: {derive: [{path: "somewhere.else", value: {x: 1}}]},
	}`)
	if code == 201 {
		t.Fatal("a derive outside a deep address installed")
	}
	if !strings.Contains(msg, "outside") && !strings.Contains(msg, "stratum") {
		t.Errorf("the refusal says neither why nor where: %s", msg)
	}
}

// A PACKAGE WITH LEVELS AND NO RULES IS STILL A PACKAGE. That is what a half-applied charter
// leaves behind — the levels install, a rule is refused — and it was invisible to everything
// that lists or removes one, because "which packages exist" was answered by walking the RULES.
//
// So `verse charter uninstall` said "no charter is installed" about a charter whose records were
// sitting in the store, and a restart would not have brought it back to be removed either.
func TestAPackageWithNoRulesIsStillAPackage(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := installAt(t, ts.URL, "notes", `{name: lvl, level: 0, match: {path: "git"}}`); code != 201 {
		t.Fatalf("installing a level = %d: %s", code, msg)
	}
	if got := srv.packagesSeen(); len(got) != 1 || got[0] != "notes" {
		t.Fatalf("packagesSeen = %v, want the package its levels belong to", got)
	}
	if err := srv.removePackage(srv.Store, "notes", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := srv.packagesSeen(); len(got) != 0 {
		t.Errorf("the package is still listed after uninstall: %v", got)
	}
	if _, ok, _ := srv.Store.Get(trigger.StrataRefAt("notes", "lvl")); ok {
		t.Error("the level outlived the package")
	}
}

// A RULE THE ENGINE NEVER INSTALLED IS STILL THE PACKAGE'S, and uninstall has to reach it.
//
// The removal walked eng.Triggers() — the rules that made it in — so a rule REFUSED at install
// survived its own uninstall. Because packagesSeen reads the RECORDS, the package went on
// existing afterwards: it could not be installed (the rule is still refused), could not be
// removed (the removal cannot see it), and still counted. There was no way out of that state
// but wiping the store, which is how it was found — a rename of the `reads:` declaration
// refused every outward-reading rule at restart, and uninstalling the packages left their
// rules behind on staging.
func TestUninstallReachesARuleTheEngineNeverInstalled(t *testing.T) {
	srv := testServer(t)
	srv.Start(t.Context())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code, msg := installAt(t, ts.URL, "notes", `{name: lvl, level: 0, match: {path: "notes"}}`); code != 201 {
		t.Fatalf("installing a level = %d: %s", code, msg)
	}
	// The record a refused rule leaves: written at the address, never in any engine. Written
	// by hand because that is exactly the state — the install path refuses it.
	stray := trigger.CharterRefAt("notes", "refused")
	entitytest.Set(t, srv.Store, stray, entity.Obj{
		"at": "notes", "name": "refused",
		"condition": entity.Obj{"subject": "git.head.*"},
		"action":    entity.Obj{"patch": entity.Obj{"seen": true}},
	}.Payload())
	if got := srv.packagesSeen(); len(got) != 1 {
		t.Fatalf("packagesSeen = %v, want the package the records belong to", got)
	}

	if err := srv.removePackage(srv.Store, "notes", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, ok, _ := srv.Store.Get(stray); ok {
		t.Error("the refused rule outlived the uninstall, so the package cannot be taken down")
	}
	if got := srv.packagesSeen(); len(got) != 0 {
		t.Errorf("the package is still listed after uninstall: %v", got)
	}
}
