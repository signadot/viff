package trigger

import (
	"fmt"
	"hash/fnv"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// Running a sequence.
//
// The facts below are not decoration. A parallel branch is woken by a `when`, and a `when`
// matches facts — so a sequence must PUBLISH its position or the breadth axis (independent
// triggers) cannot observe the depth axis (this) at all. They are also where re-fire
// idempotence stands: between two elements NO process is live, so a registry-only check has a
// hole at exactly every step boundary, which is where a re-fire is most likely to land.
//
// Everything lands under one reserved root on the subject, keyed by trigger:
//
//	verse:
//	  review:
//	    step:  2         elements completed
//	    steps: 3         len(seq), known at install
//	    shape: "1f2c…"   hash of the declared sequence
//	    done:  true      latched when step reaches steps
//
// One reserved name rather than a family of flat keys, because the payload is the AUTHOR's
// document — `flow` on a process entity is the same kind of reservation. See docs/seq-plan.md.

// VerseRoot is the reserved payload key the engine publishes its own bookkeeping under. It is
// the one name a charter may not use for its own data.
const VerseRoot = "verse"

// Progress field names under verse.<trigger>.
const (
	seqStep  = "step"
	seqSteps = "steps"
	seqShape = "shape"
	seqDone  = "done"
)

// startSeq begins carrier's sequence about subj, unless one is already running for it.
func (e *Engine) startSeq(chain string, carrier Action, ev entity.Event, subj entity.Ref) {
	shape := e.shapeOf(chain)
	if e.seqSettled(chain, subj, shape) {
		// A re-fire while the sequence is mid-flight is a NO-OP, not a second run. Note what
		// this does not do: once the sequence has finished, a re-fire starts it again, exactly
		// as a re-fired trigger has always re-acted — preventing THAT is the `when`'s job (match
		// a state field the action advances), not the engine's.
		e.tracef("seq %s: already run for %s (in flight, or finished); not restarting", chain, subj)
		return
	}
	e.publishSeq(chain, subj, entity.Obj{
		seqStep:  0,
		seqSteps: len(carrier.Seq),
		seqShape: shape,
	})
	e.advanceSeq(chain, carrier, 0, ev, subj)
}

// advanceSeq runs elements from i onward until one SPAWNS — at which point the sequence waits
// for that process to complete — or until the sequence ends.
//
// The loop exists because an element need not spawn: `seq: [{patch: …}, {patch: …}]` is two
// ordered writes, and an element that starts no process completes immediately. Without the
// loop such an element would stall the sequence forever, waiting for a completion that no
// process will ever report.
func (e *Engine) advanceSeq(chain string, carrier Action, i int, ev entity.Event, subj entity.Ref) {
	for ; i < len(carrier.Seq); i++ {
		el := carrier.Seq[i]
		h, err := el.applyOne(seqChain(chain, i), ev, subj, e)
		if err != nil {
			// A failed write ends the sequence where it stands. The step count is left at the
			// last element that really completed, so the facts do not claim progress that did
			// not happen.
			e.tracef("seq %s: element %d failed (%v); sequence stops", chain, i, err)
			return
		}
		if h != nil {
			return // in flight; its completion resumes us
		}
		e.publishSeq(chain, subj, entity.Obj{seqStep: i + 1})
	}
	e.finishSeq(chain, carrier, ev, subj)
}

// finishSeq latches `done` and fires the carrier's own onDone — the hook that means "after the
// whole sequence" rather than "after one process".
//
// `done` is PUBLISHED rather than derived, because a match compares a value against a pattern
// and never one field of a payload against another: a join written as `step == steps` cannot
// be expressed, and one written as `step: 3` silently stops meaning "finished" the moment the
// sequence gains an element.
func (e *Engine) finishSeq(chain string, carrier Action, ev entity.Event, subj entity.Ref) {
	e.publishSeq(chain, subj, entity.Obj{seqStep: len(carrier.Seq), seqDone: true})
	if carrier.OnDone != nil {
		carrier.OnDone.apply(chain+"."+"onDone", ev, []entity.Ref{subj}, e)
	}
}

// resumeSeq continues the sequence a just-completed element belonged to. Returns false if the
// chain does not address a sequence element, in which case the ordinary hook path applies.
func (e *Engine) resumeSeq(chain string, subj entity.Ref, ev entity.Event) bool {
	carrierChain, idx, ok := seqIndexOf(chain)
	if !ok {
		return false
	}
	t, ok := e.triggerNamed(mirror.TriggerOf(chain))
	if !ok {
		e.tracef("seq %s: trigger gone; sequence cannot continue", chain)
		return true // it WAS a sequence element; there is just nothing left to advance into
	}
	carrier, ok := navigate(t, carrierChain)
	if !ok {
		e.tracef("seq %s: carrier %s no longer resolves; sequence stops", chain, carrierChain)
		return true
	}
	// The charter may have moved under a running sequence — an inserted element shifts every
	// index below it, so the successor at seq[idx+1] would be a DIFFERENT action than the one
	// this sequence started under. The shape hash is what notices.
	if want := e.shapeOf(carrierChain); !e.shapeMatches(carrierChain, subj, want) {
		e.tracef("seq %s: the trigger's sequence changed while it was running; stopping rather "+
			"than running whatever now sits at element %d", carrierChain, idx+1)
		return true
	}
	e.publishSeq(carrierChain, subj, entity.Obj{seqStep: idx + 1})
	e.advanceSeq(carrierChain, *carrier, idx+1, ev, subj)
	return true
}

// seqSettled reports whether this sequence has already run for this subject — either it is
// still going, or it finished. A firing that finds it settled does nothing.
//
// It is asked of the sequence NODE rather than of any one element, and it has three parts
// because each covers a window the others cannot:
//
//   - the LIVE registry, while an element's process is alive;
//   - the `step` fact, BETWEEN elements — where nothing is live at all, which is where a
//     re-fire is most likely to land;
//   - the `done` fact, AFTER the sequence finished.
//
// The last one is not what docs/seq-plan.md predicted. The plan said a completed sequence may
// simply run again, leaving repeat-suppression to the `when` (match a state field the action
// advances — the latch the tutorial teaches). Building it showed why that cannot stand: a
// sequence's own progress writes are real changes to the subject, so they re-fire the very
// trigger that made them, and dispatch is asynchronous — by the time an early bookkeeping
// event is processed, the sequence may already have finished, at which point "may run again"
// means "runs twice, always". Latching on `done` is what makes a non-latched charter behave.
//
// Re-running is therefore EXPLICIT: clear the facts (`patch: {verse: {review: !delete null}}`,
// newly sayable) or change the sequence, which changes its shape hash. See the note in
// docs/seq-plan.md.
func (e *Engine) seqSettled(chain string, subj entity.Ref, shape string) bool {
	// live: any process whose chain lies under this node's `seq`
	prefix := chain + "." + seqField
	e.liveMu.Lock()
	for _, fp := range e.live {
		if fp.subject == subj.String() && chainUnder(prefix, fp.chain) {
			e.liveMu.Unlock()
			return true
		}
	}
	e.liveMu.Unlock()

	// between elements, or after the end: any progress recorded for THIS shape settles it.
	// Scoped to the shape so that editing the sequence frees it to run again — the same
	// signal that stops a live sequence from walking into a charter that moved under it.
	p := e.seqFacts(chain, subj)
	if p == nil {
		return false
	}
	if entity.Get(p, seqStep) == nil {
		return false
	}
	// A DIFFERENT shape means the charter changed since that run: this is a new sequence
	// that happens to share a name, so what the old one recorded does not settle it.
	if got := entity.Str(p, seqShape); got != "" && got != shape {
		return false
	}
	return true
}

// seqFacts reads verse.<trigger-of-chain> off the subject, or nil.
func (e *Engine) seqFacts(chain string, subj entity.Ref) entity.Payload {
	ent, ok, err := e.store.Get(subj)
	if err != nil || !ok {
		return nil
	}
	return entity.Get(entity.Get(ent.Payload, VerseRoot), mirror.TriggerOf(chain))
}

// shapeMatches reports whether the sequence a subject is partway through is still the one the
// charter declares.
func (e *Engine) shapeMatches(chain string, subj entity.Ref, want string) bool {
	p := e.seqFacts(chain, subj)
	if p == nil {
		return true // nothing recorded: nothing to contradict
	}
	got := entity.Str(p, seqShape)
	return got == "" || got == want
}

// publishSeq patches progress under the reserved root. It goes through the same patch path an
// action's own write does, which is exactly why `patch` had to replace `set`: a shallow
// overlay here would replace the whole `verse` key and erase a sibling trigger's progress.
func (e *Engine) publishSeq(chain string, subj entity.Ref, fields entity.Obj) {
	patch := entity.Obj{VerseRoot: entity.Obj{mirror.TriggerOf(chain): fields}}.Payload()
	if err := e.patchEntity(subj, patch); err != nil {
		e.tracef("seq %s: publishing progress on %s failed: %v", chain, subj, err)
	}
}

// recordShapes fingerprints every sequence in a trigger, ONCE, at install — keyed by the
// chain of the action carrying it.
//
// Computed here rather than on each completion for two reasons. The shape is a constant of the
// charter, so recomputing it per step is work for nothing; and — the reason it is not merely
// tidy — the generated ToTonyIR runs the action's nodes through ir.FromMap, which RE-PARENTS
// them. Hashing an installed trigger from the completion path therefore mutates nodes another
// goroutine may be reading, which the race detector duly caught. At install the trigger is not
// yet live and nothing else holds it.
func (e *Engine) recordShapes(t Trigger) {
	shapes := map[string]string{}
	collectShapes(t.Name, &t.Action, shapes)
	if len(shapes) == 0 {
		return
	}
	e.shapeMu.Lock()
	for k, v := range shapes {
		e.shapes[k] = v
	}
	e.shapeMu.Unlock()
}

// forgetShapes drops a removed trigger's fingerprints.
func (e *Engine) forgetShapes(name string) {
	e.shapeMu.Lock()
	for k := range e.shapes {
		if mirror.TriggerOf(k) == name {
			delete(e.shapes, k)
		}
	}
	e.shapeMu.Unlock()
}

// shapeOf returns the fingerprint recorded for a sequence carrier, or "" if it holds none.
func (e *Engine) shapeOf(chain string) string {
	e.shapeMu.Lock()
	defer e.shapeMu.Unlock()
	return e.shapes[chain]
}

// collectShapes walks an action tree, recording a hash at every chain that carries a sequence.
// A trigger may hold more than one — nesting a seq inside a seq is rejected, but a completion
// hook of the root action may carry its own.
func collectShapes(chain string, a *Action, into map[string]string) {
	if len(a.Seq) > 0 {
		into[chain] = seqShapeHash(a.Seq)
	}
	for i := range a.Seq {
		collectShapes(seqChain(chain, i), &a.Seq[i], into)
	}
	for _, h := range []struct {
		field string
		act   *Action
	}{
		{"onDone", a.OnDone}, {"onFailed", a.OnFailed},
		{"onCancelled", a.OnCancelled}, {"onOrphaned", a.OnOrphaned},
	} {
		if h.act != nil {
			collectShapes(chain+"."+h.field, h.act, into)
		}
	}
}

// seqShapeHash fingerprints a declared sequence, so a charter edit under a RUNNING sequence is
// detectable. A hash rather than a stored copy of the actions: these processes can run for a
// very long time, and a retained snapshot per live sequence would be a retention floor the
// store could never compact past. Detecting that the shape moved is all refusing to continue
// needs — reconstructing the old one is not.
func seqShapeHash(seq []Action) string {
	h := fnv.New32a()
	for i, el := range seq {
		n, err := el.ToTonyIR()
		if err != nil {
			// Unrenderable is its own fingerprint: it must not collide with a real shape.
			fmt.Fprintf(h, "%d:<unrenderable>", i)
			continue
		}
		b, err := codec.Marshal(n, codec.Tony)
		if err != nil {
			fmt.Fprintf(h, "%d:<unmarshalable>", i)
			continue
		}
		fmt.Fprintf(h, "%d:%s", i, b)
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// chainUnder reports whether target lies at or below prefix, comparing SEGMENTS rather than
// characters — so `review.seq` does not match a trigger named `review.sequel` (which cannot
// exist today, since a trigger name may not contain a dot, but the check should not depend on
// that being true forever).
func chainUnder(prefix, target string) bool {
	p, err := kpath.Parse(prefix)
	if err != nil {
		return false
	}
	t, err := kpath.Parse(target)
	if err != nil {
		return false
	}
	return p.MatchesPrefix(t)
}
