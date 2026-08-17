package trigger

import (
	"fmt"
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
//   - A WHETHER-motion blocks the firing itself, and the firing comes back by being EVALUATED
//     again: its action never ran (that is what the gate is holding), so a rule that still
//     selects the subject re-monitors the same channel over the same ballots. What this owes
//     that case is nothing — and what it owes the other one is an answer, because a rule that
//     no longer selects the subject will never look at that motion again (reviewWhether).
//   - A WHO-motion blocks a spawn that the action already latched its way past. Nothing will
//     re-fire it, so this re-enters the selection itself, through the same launchSpawn a
//     firing uses. Both paths are idempotent by construction: the channel id is derived from
//     (subject, spec), the commit is terminal-once, and the spawn id is deterministic — so a
//     rule that DOES re-fire alongside this resolves one motion and starts one task.
func (e *Engine) ResumeMotions() int {
	resumed := 0
	for _, ent := range e.store.List(func(r entity.Ref) bool {
		return r.Reserved() && r.System() == motionSystem && r.Kind() == motionChannelKind
	}) {
		p := ent.Payload
		if entity.Str(p, "outcome") != "" {
			continue // closed: terminal-once, and nothing is waiting on a decided motion
		}
		if entity.Str(p, "axis") != "select" {
			e.reviewWhether(ent.Ref, p)
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
		subj, err := entity.ParseAddr(entity.Str(p, "subject"))
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
		e.tracef("resuming select %s: %s on %s", ent.Ref.ID(), chain, subj)
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

// reviewWhether decides what a whether-motion left open by the previous verse is: something the
// rule will monitor again, or something nothing will ever read.
//
// Its awaiter is the firing, and the firing comes back only if the rule still SELECTS the
// subject — the replay is a re-evaluation, not a resumption. Two ways that fails, and only the
// first was obvious: the rule left the charter, and the SUBJECT MOVED OUT of what the rule
// picks. The second is the one seen in the wild: a merge proposal was marked `state: stale` by
// a sweep while its gate stood open, which is exactly what its rule's `!not.has-path state`
// excludes. The motion stayed `open` on the docket, somebody voted approve, and the tally never
// moved, because nothing was left to move it.
//
// Only the whether axis is asked this. A who-motion's subject is EXPECTED to have moved on —
// the action wrote its latch before opening the selection — so the same question there would
// orphan every one of them.
func (e *Engine) reviewWhether(ref entity.Ref, p entity.Payload) {
	name := entity.Str(p, "trigger")
	if name == "" {
		return // it does not say whose it is; leave it standing rather than guess
	}
	t, ok := e.triggerNamed(name)
	if !ok {
		// A rule HELD for a stratum that has not arrived installs the moment the stratum does,
		// so it is not a rule that is gone.
		if !e.knowsTrigger(name) {
			e.orphanMotion(ref, "the rule that opened it is no longer in the charter")
		}
		return
	}
	subj, err := entity.ParseAddr(entity.Str(p, "subject"))
	if err != nil {
		return
	}
	if !e.stillSelects(t, subj) {
		e.orphanMotion(ref, fmt.Sprintf("%s no longer selects %s, so nothing will read the answer",
			name, subj))
	}
}

// stillSelects reports whether a trigger's subject predicate still picks this entity out of the
// verse as it stands — the same question the dispatcher asks per commit, asked once at startup
// against the current revision.
//
// It answers TRUE when it cannot tell (an unreadable store, a predicate whose read fails).
// Leaving a motion standing costs a stale docket entry; settling one wrongly takes a live
// question away from the people it is in front of, and this is not the code that should be
// deciding that on a guess.
//
// It asks about the SUBJECT alone, not the cause: a rule fired by an `on` edge may still select
// its subject and never see that edge again. So a motion this keeps is not a promise that the
// rule will re-fire — only that nothing about the subject says it cannot.
func (e *Engine) stillSelects(t Trigger, subj entity.Ref) bool {
	now, err := e.store.AsOf(e.store.Rev(), paths(t.reads())...)
	if err != nil {
		return true
	}
	m, err := e.conditionMatch(t.Condition.Subject, now)
	if err != nil {
		return true
	}
	for _, r := range m.Select(now) {
		if r == subj {
			return true
		}
	}
	return false
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
	e.tracef("orphaning motion %s: %s", ref.ID(), why)
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
