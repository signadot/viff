package trigger

import (
	"strings"
	"testing"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
	"go.viff.dev/viff/pkg/task"
)

// A spawn id is `<chain>@<subject>`, and the chain half is a real kpath: the address of the
// spawning action inside its trigger. Parsing it back is what a flow view (and, later, a
// resume that re-derives hooks) needs, so the form has to survive kpath.Parse unquoted.
func TestChainIsAKPath(t *testing.T) {
	ref := entity.RefAt("demo", "pr", "1")
	for _, chain := range []string{"review", "review.onDone", "review.onDone.onFailed"} {
		id := spawnID("", chain, ref)
		got, rest, ok := chainOf(id)
		if !ok || got != chain || rest != ref.Token() {
			t.Errorf("chainOf(%q) = (%q, %q, %v); want (%q, %q, true)",
				id, got, rest, ok, chain, ref.Token())
			continue
		}
		kp, err := kpath.Parse(got)
		if err != nil {
			t.Errorf("chain %q does not parse as a kpath: %v", got, err)
			continue
		}
		if kp.String() != chain {
			t.Errorf("chain %q does not round-trip through kpath: got %q", chain, kp.String())
		}
	}
}

// The root of a chain prefix-matches everything under it, which is how "every task of
// this trigger" is asked without globbing ids. Uses go-tony v0.0.92's MatchesPrefix, so
// this also pins the upstream behaviour viff now depends on (issue 61766xadh12kstqqcnn0).
func TestChainPrefixMatching(t *testing.T) {
	root, err := kpath.Parse("review")
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	for _, chain := range []string{"review", "review.onDone", "review.onDone.onFailed"} {
		kp, err := kpath.Parse(chain)
		if err != nil {
			t.Fatalf("parse %q: %v", chain, err)
		}
		if !root.MatchesPrefix(kp) {
			t.Errorf("root %q does not prefix-match %q", "review", chain)
		}
	}
	other, _ := kpath.Parse("reviewed.onDone") // a different trigger, not a descendant
	if root.MatchesPrefix(other) {
		t.Error("root review prefix-matched a different trigger reviewed.onDone")
	}
}

// A spawn id is pasted back onto a command line constantly (task show, gate resolve,
// task events), so it must survive a shell word unquoted. This is the constraint that
// kept kpath's bracket syntax OUT of the id: under zsh, review.seq[2]@demo.pr.1 dies with
// "no matches found" before viff ever runs. The same principle a task id follows.
func TestSpawnIDIsPasteSafe(t *testing.T) {
	const globs = `*?[]{}()~!$&;'"<>|` + "`\\"
	ids := []string{
		spawnID("", "review", entity.RefAt("demo", "pr", "1")),
		spawnID("", "review.onDone.onFailed", entity.RefAt("demo", "pr", "hotfix-1")),
		spawnID("", "review", entity.RefAt("local", "file", "a/b c")),
	}
	for _, id := range ids {
		if i := strings.IndexAny(id, globs); i >= 0 {
			t.Errorf("spawn id %q contains %q, which a shell reads as punctuation — "+
				"an id that must be quoted to be pasted is an id that will be mistyped",
				id, id[i])
		}
		if strings.Contains(id, task.GateSep) {
			t.Errorf("spawn id %q contains GateSep %q", id, task.GateSep)
		}
	}
	// entity.SafeSeg spells the gate separator out rather than importing it, so that the
	// substrate does not depend on the task package. This is the pairing that keeps the two
	// honest — it is the only place that imports both.
	if entity.SafeSeg(task.GateSep) == task.GateSep {
		t.Errorf("entity.SafeSeg leaves %q alone, and task.GateSep is %q: an id carrying one "+
			"would make an answer split at the wrong place and route to a task that is not there",
			task.GateSep, task.GateSep)
	}
}

// The chain descends by the hook's FIELD name, not by its status: the segment is the thing
// you would name to look the action up in the charter. done→onDone is the pairing that
// makes an id an address rather than a log line.
func TestHookFieldPairsWithHookFor(t *testing.T) {
	a := &Action{
		OnDone:      &Action{},
		OnFailed:    &Action{},
		OnCancelled: &Action{},
	}
	want := map[task.Status]string{
		task.Done:      "onDone",
		task.Failed:    "onFailed",
		task.Cancelled: "onCancelled",
	}
	for st, field := range want {
		if got := hookField(st); got != field {
			t.Errorf("hookField(%s) = %q; want %q", st, got, field)
		}
		if a.hookFor(st) == nil {
			t.Errorf("hookFor(%s) is nil though the action sets every hook", st)
		}
		// the descended chain must stay a parseable kpath
		if _, err := kpath.Parse("review." + hookField(st)); err != nil {
			t.Errorf("descended chain review.%s is not a kpath: %v", hookField(st), err)
		}
	}
	if got := hookField(task.Running); got != "" {
		t.Errorf("hookField(running) = %q; want empty — a non-terminal status has no hook", got)
	}
}

// A chain's root IS the trigger name, so a name that is not a bare kpath field would make
// the id ambiguous (or need quoting, which a URL path cannot carry). This also closes the
// old collision where a trigger named review:done shadowed review's done hook.
func TestTriggerNameValidation(t *testing.T) {
	ok := []string{"review", "ship-reviewed", "review_2", "Review2"}
	for _, n := range ok {
		if err := validTriggerName(n); err != nil {
			t.Errorf("validTriggerName(%q) = %v; want nil", n, err)
		}
	}
	bad := []string{"", "review.onDone", "review@demo", "review*", "review[2]", "a b", `re"view`, "review+1"}
	for _, n := range bad {
		if err := validTriggerName(n); err == nil {
			t.Errorf("validTriggerName(%q) = nil; want an error", n)
		}
	}
}

// THE BOUNDARY IS '@' because a trigger name may carry '-': `ship-reviewed-demo.pr.1` cannot be
// split back into (chain, ref) when the separator is one both halves spend. This is the guard on
// that.
func TestDashInTriggerNameStillDecodes(t *testing.T) {
	ref := entity.RefAt("demo", "pr", "hotfix-1")
	id := spawnID("", "ship-reviewed", ref)
	chain, rest, ok := chainOf(id)
	if !ok || chain != "ship-reviewed" || rest != ref.Token() {
		t.Fatalf("chainOf(%q) = (%q, %q, %v); want (\"ship-reviewed\", %q, true)",
			id, chain, rest, ok, ref.Token())
	}
}

// mirror publishes the chain and its root so "every task of this trigger" is a payload
// predicate (a value, not a fragment of an id) rather than a glob over ids.
func TestTriggerOf(t *testing.T) {
	for chain, want := range map[string]string{
		"review":                 "review",
		"review.onDone":          "review",
		"review.onDone.onFailed": "review",
		"review.seq[2]":          "review", // once actions hold lists
		"":                       "",
	} {
		if got := mirror.TriggerOf(chain); got != want {
			t.Errorf("TriggerOf(%q) = %q; want %q", chain, got, want)
		}
	}
}
