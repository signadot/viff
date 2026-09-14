package entity_test

import (
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
		{"runner:coderunner", "runner:coderunner", entity.RunnerKind, "coderunner"},
		{"system:docket", "system:docket", entity.SystemKind, "docket"},
		{"user:*", "user:*", entity.UserKind, "*"},
		{"agent:*", "agent:*", entity.AgentKind, "*"},
		{"runner:*", "runner:*", entity.RunnerKind, "*"},
		{"group:release", "group:release", entity.GroupKind, "release"},
		{"via:verse.trigger.rule.x", "via:verse.trigger.rule.x", entity.ViaKind, ""},
		{"via:verse.trigger.rule.drift:head-moved", "via:verse.trigger.rule.drift:head-moved", entity.ViaKind, ""},
		{"via:verse.sources.git.cluster", "via:verse.sources.git.cluster", entity.ViaKind, ""},
		{"via:verse.sources.*", "via:verse.sources.*", entity.ViaKind, ""},
		{"via:verse.resolvers.security.gate", "via:verse.resolvers.security.gate", entity.ViaKind, ""},
		{`via:verse.sources.git."a.b"`, `via:verse.sources.git."a.b"`, entity.ViaKind, ""},
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
		"user", "coderunner", "system", "",
		"wizard:x",                          // no such kind
		"group:*",                           // a group is named, not listed under
		"user:",                             // an empty id
		"user:a*b",                          // a * that is not the whole id
		"user:a b",                          // an id with a space
		"via:",                              // an empty address
		"via:demo.pr.1",                     // the world's half acts as nobody
		"via:verse.*.x",                     // a * that is not trailing
		"via:verse.*",                       // no system, so nothing that acts
		"via:verse.runners.pool.coderunner", // a runner acts as runner:<identity>
		"via:verse.trigger.package.x",       // a package is not a rule
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
	for _, in := range []string{"*", "user:*", "group:x", "via:verse.sources.*"} {
		if _, err := entity.ParsePrincipal(in); err == nil {
			t.Errorf("ParsePrincipal(%q) parsed a set as one principal", in)
		}
	}
	for _, in := range []string{"user:ana@example.com", "ana@example.com", "runner:coderunner", "via:verse.trigger.rule.x", "system:default"} {
		if _, err := entity.ParsePrincipal(in); err != nil {
			t.Errorf("ParsePrincipal(%q): %v", in, err)
		}
	}
}

// Every part of the system owns the root: the parts say which act wrote, and none is narrower.
func TestEverySystemPartHoldsTheRoot(t *testing.T) {
	system := entity.As(entitytest.Store(t), entity.SystemDefault)
	for _, part := range []string{entity.SystemDefault, entity.SystemDocket, entity.SystemMirror,
		entity.SystemPerms, entity.SystemSeed, entity.SystemCharter} {
		for _, ref := range []entity.Ref{entity.RefAt("demo", "pr", "1"), entity.VerseRef("trigger", "rule", "x"), entity.PermsRef(entity.RefAt("demo"))} {
			if ok, err := entity.Holds(system, part, entity.OwnerLevel, ref); err != nil || !ok {
				t.Errorf("%s does not hold owner at %s: %v", part, ref, err)
			}
		}
	}
}
