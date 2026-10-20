package entity_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
)

// Every spelling the grammar has reads back as itself, and a bare email reads as the person.
func TestGranteeSpellings(t *testing.T) {
	for _, tc := range []struct {
		in, out  string
		kind, id string
	}{
		{"*", "*", entity.Everyone, ""},
		{"user:walter@example.com", "user:walter@example.com", entity.UserKind, "walter@example.com"},
		{"walter@example.com", "user:walter@example.com", entity.UserKind, "walter@example.com"},
		{"agent:harness", "agent:harness", entity.AgentKind, "harness"},
		{"runner:verse-runner", "runner:verse-runner", entity.RunnerKind, "verse-runner"},
		{"system:docket", "system:docket", entity.SystemKind, "docket"},
		{"user:*", "user:*", entity.UserKind, "*"},
		{"agent:*", "agent:*", entity.AgentKind, "*"},
		{"runner:*", "runner:*", entity.RunnerKind, "*"},
		{"group:release", "group:release", entity.GroupKind, "release"},
		{"via:verse.trigger.rule.x", "via:verse.trigger.rule.x", entity.ViaKind, ""},
		{"via:verse.trigger.rule.drift:head-moved", "via:verse.trigger.rule.drift:head-moved", entity.ViaKind, ""},
		{"via:verse.iod.cluster", "via:verse.iod.cluster", entity.ViaKind, ""},
		{"via:verse.iod.*", "via:verse.iod.*", entity.ViaKind, ""},
		{"via:verse.resolvers.gate", "via:verse.resolvers.gate", entity.ViaKind, ""},
		{`via:verse.iod."a.b"`, `via:verse.iod."a.b"`, entity.ViaKind, ""},
	} {
		g, err := entity.ParseGrantee(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if g.Kind != tc.kind || g.ID != tc.id || g.String() != tc.out {
			t.Errorf("%s: parsed %+v, rendered %q; want kind %q id %q rendered %q", tc.in, g, g.String(), tc.kind, tc.id, tc.out)
		}
		back, err := entity.ParseGrantee(g.String())
		if err != nil || back.String() != g.String() {
			t.Errorf("%s: the rendering %q did not read back: %+v %v", tc.in, g.String(), back, err)
		}
	}
	if got := entity.Via(entity.VerseRef("trigger", "rule", "x")); got != "via:verse.trigger.rule.x" {
		t.Errorf("Via: %q", got)
	}
}

// What the grammar refuses, it refuses by name and quotes itself. The two spellings the issue
// began from — a kind word and a group's address, both bare — are among them.
func TestGranteeRefusals(t *testing.T) {
	for _, in := range []string{
		"agent", // a kind word, which a principal could once be named
		"verse.principal.group.release",
		"user", "verse-runner", "system", "",
		"wizard:x",                       // no such kind
		"group:*",                        // a group is named, not listed under
		"user:",                          // an empty id
		"user:a*b",                       // a * that is not the whole id
		"user:a b",                       // an id with a space
		"via:",                           // an empty address
		"via:demo.pr.1",                  // the world's half acts as nobody
		"via:verse.*.x",                  // a * that is not trailing
		"via:verse.*",                    // no system, so nothing that acts
		"via:verse.runners.verse-runner", // a runner acts as runner:<identity>
		"via:verse.trigger.package.x",    // a package is not a rule
		"verse.covers.review@run-1",      // a cover's address is no person
	} {
		_, err := entity.ParseGrantee(in)
		if err == nil {
			t.Errorf("%q parsed", in)
			continue
		}
		if !strings.Contains(err.Error(), entity.Grammar()) {
			t.Errorf("%q: the refusal does not quote the grammar: %v", in, err)
		}
	}
	for _, in := range []string{"*", "user:*", "group:x", "via:verse.iod.*"} {
		if _, err := entity.ParsePrincipal(in); err == nil {
			t.Errorf("ParsePrincipal(%q) parsed a set as one principal", in)
		}
	}
	for _, in := range []string{"user:ana@example.com", "ana@example.com", "runner:verse-runner", "via:verse.trigger.rule.x", "system:default"} {
		if _, err := entity.ParsePrincipal(in); err != nil {
			t.Errorf("ParsePrincipal(%q): %v", in, err)
		}
	}
}

// Every part of the system owns the root: the parts say which act wrote, and none is narrower.
func TestEverySystemPartHoldsTheRoot(t *testing.T) {
	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	for _, part := range []string{entity.SystemDefault, entity.SystemDocket, entity.SystemMirror,
		entity.SystemPerms, entity.SystemSeed, entity.SystemCharter} {
		for _, ref := range []entity.Ref{entity.RefAt("demo", "pr", "1"), entity.VerseRef("trigger", "rule", "x"), entity.PermsRef(entity.RefAt("demo"))} {
			if ok, err := entity.Holds(system, part, entity.OwnerLevel, ref); err != nil || !ok {
				t.Errorf("%s does not hold owner at %s: %v", part, ref, err)
			}
		}
	}
}

// The match reads each name by its structure. A kind admits by the kind a name carries, so a
// person named agent is not an agent; a place admits what acts as it, and beneath it under a
// trailing `*`; and a name the grammar does not read admits nobody, is counted, and is said.
func TestTheMatchReadsTheGrammar(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	system := entity.As(entitytest.OpenStore(t), entity.SystemDefault)
	entity.PolicyOf(system).SetRootChildGrants(nil)
	grant := func(name string, to ...string) entity.Ref {
		t.Helper()
		ref := entity.RefAt("demo", "pr", name)
		if err := entity.Claim(system, ref, "user:owner@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := entity.AddGrant(system, ref, entity.Grant{At: ref.Segs(), To: map[entity.Level][]string{entity.Read: to}}); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	reads := func(who string, ref entity.Ref) bool {
		t.Helper()
		ok, err := entity.Holds(system, who, entity.Read, ref)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	agents := grant("agents", "agent:*")
	if !reads("agent:harness", agents) || reads("user:agent", agents) {
		t.Error("agent:* reads the kind off the name")
	}
	person := grant("person", "user:agent")
	if !reads("user:agent", person) || reads("agent:agent", person) {
		t.Error("user:agent admits exactly that principal")
	}
	// Each place is somebody's, so what it holds is what the grant holds out and not the
	// system's: a place with no owner on record acts as the system, which holds everything.
	for _, place := range []entity.Ref{entity.VerseRef("iod", "cluster"), entity.VerseRef("trigger", "rule", "x"), entity.VerseRef("iod")} {
		if err := entity.Establish(system, place, "user:installer@example.com", entity.Ref{}, []entity.Grant{}); err != nil {
			t.Fatal(err)
		}
	}
	sources := grant("sources", "via:verse.iod.*")
	if !reads("via:verse.iod.cluster", sources) || reads("via:verse.trigger.rule.x", sources) ||
		reads("via:verse.sources", sources) {
		t.Error("via:verse.iod.* admits what acts beneath it, and nothing else")
	}

	stale := grant("stale", "verse.principal.group.release")
	before := entity.PolicyOf(system).Unparsed()
	if reads("user:anyone@example.com", stale) {
		t.Error("a group's bare address admitted somebody")
	}
	if entity.PolicyOf(system).Unparsed() <= before {
		t.Error("a name that does not parse was not counted")
	}
	if !strings.Contains(logged.String(), "verse.principal.group.release") || !strings.Contains(logged.String(), "demo.pr.stale") {
		t.Errorf("the log does not name the line and its record:\n%s", logged.String())
	}
}
