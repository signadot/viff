package trigger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"
	"github.com/signadot/verse/entity"
)

// checkLatch reports a spawning action that can never stop spawning. It is advisory today —
// AddTrigger logs it — and is meant to become a refusal once nothing in the tree trips it.
//
// A trigger is evaluated at every commit that touches what it reads, so what ends a firing is
// not the dispatcher but the charter. A store write ends itself: rewriting the same value
// produces no commit, so it raises no next evaluation. A SPAWN does not — it is not a store
// write, nothing about it is inert on repetition, and the engine's dedupe holds only while the
// process is LIVE (Engine.spawnHooked). Once it is reaped, the next commit spawns again, and
// the one after that.
//
// So a spawning action has to take its subject OUT of the selection: write a field the
// subject's predicate constrains, and the next evaluation does not select it. The canonical
// shape already does — spawn, then `onDone: {patch: {stage: reviewed}}` against a subject
// asking for `stage: open` — which is why hooks and sequence elements count as latches: the
// process is live in the meantime, and its completion advances the subject.
//
// The check is over KEYS, not values: a patch's key set is literal even when its values are
// templated ($result), so "does this write something the subject reads" is decidable where
// "does this write something that stops matching" is not. It is therefore conservative in the
// direction that matters — it can pass a charter that writes the same value back, which
// converges anyway by producing no commit, but it cannot pass one with nothing to write.
func checkLatch(c Condition, a Action) error {
	if !spawns(a) {
		return nil
	}
	reads := predicateKeys(c.Subject.Predicate())
	if len(reads) == 0 {
		return fmt.Errorf("this action spawns, and its subject has no payload predicate to " +
			"latch: a spawn is not a store write, so nothing about it is inert on repetition, " +
			"and the engine's dedupe lasts only while the process is live. Give the subject a " +
			"predicate the action advances past — `value: {stage: open}` with a patch (or an " +
			"`onDone`) writing `stage` — so the next evaluation does not select it again")
	}
	writes := map[string]bool{}
	collectWrites(a, writes)
	for k := range reads {
		if writes[k] {
			return nil
		}
	}
	return fmt.Errorf("this action spawns but writes nothing its subject reads (%s), so every "+
		"commit that touches it spawns again: a spawn is not a store write and the engine's "+
		"dedupe lasts only while the process is live. Patch one of those fields — here, or in "+
		"an `onDone`, which is the ordinary shape: spawn the work, and let its completion "+
		"advance the subject past the state that asked for it", keyList(reads))
}

// spawns reports whether an action starts a process anywhere in its own tree — directly, or
// through a sequence element. A hook's spawn is NOT counted: a hook runs once per completion
// of the thing above it, so it is bounded by that thing rather than by the subject predicate.
func spawns(a Action) bool {
	if a.Spawn != nil {
		return true
	}
	for _, el := range a.Seq {
		if spawns(el) {
			return true
		}
	}
	return false
}

// collectWrites gathers the top-level keys an action writes anywhere in its tree — its own
// patch, its sequence elements, and its completion hooks. Hooks count because they are how the
// canonical shape latches: the spawn holds the subject while it runs, and the hook advances it
// when it ends.
func collectWrites(a Action, into map[string]bool) {
	for k := range entity.Fields(a.Patch) {
		into[k] = true
	}
	for _, el := range a.Seq {
		collectWrites(el, into)
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h != nil {
			collectWrites(*h, into)
		}
	}
}

// predicateKeys names the top-level fields a subject's payload predicate reads. A predicate
// that is not an object (`value: 42`, `value: !glob "hotfix-*"`) constrains the whole payload
// rather than a field, and any write at all replaces it — so it is reported as the empty
// name, which every write matches.
func predicateKeys(p *ir.Node) map[string]bool {
	if p == nil {
		return nil
	}
	out := map[string]bool{}
	if p.Type != ir.ObjectType {
		out[""] = true
		return out
	}
	for _, f := range p.Fields {
		out[f.String] = true
	}
	return out
}

// keyList renders a key set for a diagnostic, in a stable order.
func keyList(keys map[string]bool) string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		if k == "" {
			return "the whole payload"
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
