package trigger

import (
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// A motion is durable and its AWAITER is not. The channel entity, the ballots cast into it and
// the deadline it closes on all live in the store; what watches them is a goroutine inside the
// firing that opened it. So a restart leaves every open motion on `verse docket` with nothing
// reading the answer — the ballot lands, the tally never moves, and the work behind it never
// starts. This file is the other half: at startup, every open motion either gets its awaiter
// back or is settled, so the docket lists only decisions somebody can still make.
//
// It is the same rule reconcileQuestions applies to the other docket (versed/server.go), and
// the two dockets reach it from opposite ends. A posed QUESTION is settled and comes back by
// being asked again, because a firing that still matches re-poses it under the same
// deterministic id. A MOTION cannot rely on that: the who-axis opens after the action has
// already written its latch, so the rule no longer matches and nothing re-fires. What the
// motion has instead is its `chain` — the address of the action it is blocking — which is
// enough to walk back into the charter and re-enter the continuation the ballot was for.

// motionOrphaned is a motion nobody is waiting on any more. It is not an outcome: the channel
// keeps no verdict, because none was reached, and a re-fire that reopens the channel clears it
// (openChannel writes `status: open` over anything that has not committed). Terminal-once
// belongs to `outcome` alone.
const motionOrphaned = "orphaned"

// ResumeMotions re-attaches an awaiter to the motions a restart left open, and settles the
// ones nothing can answer any more. It returns how many were resumed.
//
// Call it ONCE at startup, after the charter is loaded — it navigates back into the rules —
// and after the task reconcile, so a resumed selection sees the live registry rather than
// respawning alongside a task that survived.
//
// The two axes need different things, because their continuations do:
//
//   - A WHETHER-motion blocks the firing itself, and the firing comes back on its own: the
//     rule's action never ran (that is what the gate is holding), so the subject still matches
//     and the trigger's replay re-monitors the same channel over the same ballots. Nothing to
//     do here beyond noticing when the rule it belongs to is gone from the charter.
//   - A WHO-motion blocks a spawn that the action already latched its way past. Nothing will
//     re-fire it, so this re-enters the selection itself, through the same launchSpawn a
//     firing uses. Both paths are idempotent by construction: the channel id is derived from
//     (subject, spec), the commit is terminal-once, and the spawn id is deterministic — so a
//     rule that DOES re-fire alongside this resolves one motion and starts one task.
func (e *Engine) ResumeMotions() int {
	resumed := 0
	for _, ent := range e.store.List(func(r entity.Ref) bool {
		return r.System == motionSystem && r.Kind == motionChannelKind
	}) {
		p := ent.Payload
		if entity.Str(p, "outcome") != "" {
			continue // closed: terminal-once, and nothing is waiting on a decided motion
		}
		if entity.Str(p, "axis") != "select" {
			// A whether-motion, whose awaiter is the firing. It comes back with the rule, so
			// the only thing to settle is a motion whose rule is not there any more: a
			// listing asking the room to approve something no charter will act on.
			if name := entity.Str(p, "trigger"); name != "" && !e.knowsTrigger(name) {
				e.orphanMotion(ent.Ref, "the rule that opened it is no longer in the charter")
			}
			continue
		}
		chain := entity.Str(p, "chain")
		if chain == "" {
			// A who-motion from a verse that did not record its chain — every one opened
			// before motions carried it. There is no way back to the action it blocks.
			e.orphanMotion(ent.Ref, "it does not record which action it belongs to")
			continue
		}
		t, ok := e.triggerNamed(mirror.TriggerOf(chain))
		if !ok {
			// A rule HELD for a stratum that has not arrived is not a rule that is gone: it
			// installs the moment the stratum does. Nothing can be navigated through it in the
			// meantime, so the motion is left standing rather than resumed or settled.
			if e.knowsTrigger(mirror.TriggerOf(chain)) {
				continue
			}
			e.orphanMotion(ent.Ref, "the rule that opened it is no longer in the charter")
			continue
		}
		act, ok := navigate(t, chain)
		if !ok || act.Spawn == nil || act.Spawn.Provider == nil {
			// The charter was edited under a live motion: the action moved, or no longer
			// selects a doer. Same answer as a rehydrated hook that no longer resolves —
			// there is nothing left to run, and saying so beats a ballot into a void.
			e.orphanMotion(ent.Ref, "the action that opened it is no longer in the charter")
			continue
		}
		subj, err := entity.ParseRef(entity.Str(p, "subject"))
		if err != nil {
			e.orphanMotion(ent.Ref, "it does not name a subject this verse can read")
			continue
		}
		// The subject itself, because the ballot is about work on it: an entity REMOVED while
		// the motion stood leaves a question about something that is not there, and answering
		// it would start a task about a ghost. A store that cannot answer is a different
		// matter — it is left alone rather than guessed about, exactly as a question whose
		// task cannot be read is.
		if _, found, err := e.store.Get(subj); err == nil && !found {
			e.orphanMotion(ent.Ref, "the subject it is about is gone")
			continue
		}
		e.tracef("resuming select %s: %s on %s", ent.Ref.ID, chain, subj)
		go e.resumeSelection(*act, chain, subj)
		resumed++
	}
	// At Info, not Debug: work is waiting on somebody, and "there are two motions on the
	// docket that this verse just picked back up" is the kind of thing an operator reading a
	// startup log should not have to turn tracing on to learn.
	if resumed > 0 {
		e.logger.Info("resumed open motions from the docket", "count", resumed)
	}
	return resumed
}

// resumeSelection re-enters the spawn a who-motion is blocking: it awaits the same channel
// over the ballots already cast, and launches what the winner names.
//
// What it CANNOT recover is the occasion — the commit that woke the original firing died with
// it. Two consequences, both deliberate and both visible: `$references` in the spawn resolve
// against the subject's payload as it stands now, which is what a firing about a subject reads
// anyway and is a re-read rather than a guess for one caused by a hook; and the run carries no
// occurrence, because a restart is the world coming back rather than a commit causing
// something (see applyOne on a hook's Rev 0).
func (e *Engine) resumeSelection(a Action, chain string, subj entity.Ref) {
	var root entity.Payload
	if ent, ok, err := e.store.Get(subj); err == nil && ok {
		root = ent.Payload
	}
	if _, err := e.launchSpawn(a, chain, subj, root, 0); err != nil {
		e.tracef("resuming select on %s: %v", subj, err)
	}
}

// orphanMotion settles a motion nobody can answer, recording WHY on the channel.
//
// The record stays rather than being deleted, for the reason a settled question's does: what
// was asked, and why it was never decided, is the trail — and a docket entry that vanishes is
// one nobody can account for.
func (e *Engine) orphanMotion(ref entity.Ref, why string) {
	e.tracef("orphaning motion %s: %s", ref.ID, why)
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		if entity.Str(cur, "outcome") != "" {
			return cur // it committed while we were deciding it had not; terminal-once
		}
		p := clonePayload(cur)
		p["status"] = motionOrphaned
		p["orphanedAt"] = e.now().UTC().Format(time.RFC3339)
		p["why"] = why
		return p.Payload()
	})
}

// knowsTrigger reports whether the charter still carries a rule by this name — installed, or
// HELD for a stratum that has not arrived. A held rule installs the moment its stratum does,
// so treating it as gone would orphan motions that are about to have an awaiter again.
func (e *Engine) knowsTrigger(name string) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, t := range e.triggers {
		if t.Name == name {
			return true
		}
	}
	for _, t := range e.pending {
		if t.Name == name {
			return true
		}
	}
	return false
}
