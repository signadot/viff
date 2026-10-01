package trigger

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
)

// Who does the work, when a spawn leaves that to the verse.
//
// A provider selection poses a QUESTION on the docket every gate shares: the eligible doers
// are its options, each at the threshold the spawn asks for, and `within` is its deadline
// with no default — the clock passing with no pick settles it on nothing, and nothing spawns.
// What settles it is whoever answers, exactly as for a gate; what the engine owes is to offer
// only doers that can be seated and to launch the one the answer names.

// selectProvider resolves a spawn's provider selection to the fragment the chosen doer
// contributes, or (zero, false) when nothing is to be spawned: no eligible doer, no pick by
// the deadline, or the selection ending before it was answered.
func (e *Engine) selectProvider(spawn launch.Spec, subject entity.Ref) (launch.Fragment, bool) {
	p := spawn.Provider
	options, frags := e.eligibleProviders(spawn)
	if len(options) == 0 {
		// A question needs options, so there is no question to pose and nothing reaches the
		// docket. That is indistinguishable from "the trigger never matched" unless we say
		// so HERE: the firing matched, its gates passed, and then it produced no task,
		// no question, and no error. Every other exit from this function traces; this one is
		// the likeliest to be hit (a capability whose registry is empty, or a match that
		// excludes everyone) and was the only one silent.
		why := e.whyNoEligible(spawn)
		e.logger.Warn("provider selection has no eligible doer; no question posed and nothing spawned",
			"subject", subject.String(), "capability", p.Capability,
			"select", p.Select, "why", why)
		// Kept on the rule: no task starts and no question is posed, so nothing else the
		// rule's author reads would say the spawn did not happen.
		e.failed(spawn.Chain, subject, fmt.Errorf("no doer is eligible for capability %q, so "+
			"nothing was spawned: %s", p.Capability, why))
		return launch.Fragment{}, false
	}
	// auto: a rule picks the first eligible doer immediately — and poses NOTHING. The docket
	// is the answerable, attributable surface; an auto pick has no answerer and casts no
	// ballot, so a record for it would report a decision as the room's that nobody made. A
	// gate follows the same rule: a question is posed for `ask`, never for `mode: auto`.
	//
	// The pick stays observable on the spawned task (its Driver and Doer). Not latching also
	// retires a failure mode: a committed pick outlived the registry and went on naming a
	// retired doer. Re-deriving per firing is deterministic given the same registry —
	// eligibleProviders sorts — and follows the registry when it changes.
	if p.Select == "" || p.Select == "auto" {
		return frags[options[0]], true
	}
	need := p.Need
	if p.Select == "ask" || need < 1 {
		need = 1
	}
	opts := make([]task.Option, 0, len(options))
	for _, id := range options {
		f := frags[id]
		opts = append(opts, task.Option{ID: id, Need: need, Grant: f.Grant, Tools: f.Tools})
	}
	var deadline *task.Deadline
	if p.Within != "" {
		deadline = &task.Deadline{Within: p.Within} // no default: no pick by then is no spawn
	}
	origin := mirror.TriggerOf(spawn.Chain)
	d := task.GateQuestion{
		// Derived from the spec and the action, so a re-fire — and a restart — re-poses the
		// same question and joins the ballots already cast on it.
		ID: selectID(e.idOwner(), spawn.Chain, subject, p), Origin: origin, Mode: task.Ask, Gate: "provider",
		Reason:   fmt.Sprintf("select a doer for %s on %s", spawn.Chain, subject),
		Subject:  subject.String(),
		Spawns:   spawnID(e.idOwner(), chainID(spawn.Chain), subject),
		Resolver: spawn.Resolver, Scope: e.scope, At: e.At(),
		Options: opts, Deadline: deadline, Chain: spawn.Chain,
	}
	ctx := e.bgctx
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := e.resolver.Resolve(ctx, d, task.ToolUse{Name: "verse:trigger:" + origin})
	if err != nil {
		e.tracef("select %s: ended before the question was answered: %v", d.ID, err)
		return launch.Fragment{}, false
	}
	choice := res.Chosen()
	f, seatable := frags[choice]
	if !seatable {
		// The clock passed with no pick (task.NoOption), or an answer named something that is
		// not on the ballot — which the answer path refuses, so it reaches here only from a
		// resolver answering the engine directly. Either way nobody chose a doer.
		e.tracef("select %s: settled on %q, which seats nobody; not spawning", d.ID, choice)
		return launch.Fragment{}, false
	}
	e.tracef("select %s: %s by %s", d.ID, choice, res.By)
	return f, true
}

// selectID is the question a selection poses, derived so a re-fire joins it: the rule, the
// action, the subject and the eligibility spec, hashed together.
func selectID(owner, chain string, subject entity.Ref, p *launch.Provider) string {
	return fmt.Sprintf("%s%s.trigger.%s.provider%s%s", owner, entity.VerseRoot,
		entity.SafeSeg(mirror.TriggerOf(chain)), task.GateSep,
		seedHash(chain+"|"+providerSpecSeed(subject, p)))
}

// seedHash hashes a seed into the stable, re-fire-idempotent id a question is keyed by.
func seedHash(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return fmt.Sprintf("%08x", h.Sum32())
}

// providerSpecSeed derives the select-axis id seed from the subject + eligibility spec
// (inline set, capability, match), so a re-fire reuses the same channel.
//
// The seed is the spec rendered in tony wire form, with its key order fixed HERE by
// FromKeyVals rather than left to a map's iteration. That is what makes it stable.
//
// ir.Node.Hash() would read as the obvious choice and is NOT usable: it builds a
// maphash.Hash with no seed, and maphash's zero value takes a fresh random seed per
// instance — so the same node hashes differently on two successive calls in one process.
// Filed as tony-format#f69agjyeh12ksa25cnn0 item 16. TestProviderSpecSeedIsStable is the
// guard; it fails immediately if any of this stops being deterministic.
//
// NOTE: this changed the channel ids. They are derived, not stored, so nothing needs
// migrating — but a motion already open when a verse is upgraded gets a NEW channel on its
// next re-fire, leaving the old vote log orphaned. Acceptable for a prototype; it belongs
// in release notes, not in someone's afternoon.
func providerSpecSeed(subject entity.Ref, p *launch.Provider) string {
	match, err := gomap.ToTonyIR(p.Match)
	if err != nil || match == nil {
		match = ir.Null()
	}
	spec := ir.FromKeyVals([]ir.KeyVal{
		{Key: ir.FromString("e"), Val: stringsNode(p.Eligible)},
		{Key: ir.FromString("c"), Val: ir.FromString(p.Capability)},
		{Key: ir.FromString("m"), Val: match},
	})
	rendered, err := codec.NodeBytesWire(spec, codec.Tony)
	if err != nil {
		rendered = []byte(p.Capability) // degraded but stable; a bad spec will not spawn anyway
	}
	return "select/" + subject.String() + "/" + string(rendered)
}

func stringsNode(ss []string) *ir.Node {
	vals := make([]*ir.Node, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, ir.FromString(s))
	}
	return ir.FromSlice(vals)
}

// ResumeSelections gives every open selection its awaiter back after a restart, and settles
// the ones no charter can answer any more.
//
// A gate's question needs nothing here: its awaiter is the firing, and the rule's replay
// re-poses it. A selection opens from inside an action, after that action has written its
// latch, so nothing re-fires it — the chain it recorded is the only way back to the work the
// ballot is holding up. The reconcile that ran before this orphaned every question whose
// waiter died; re-posing the same id brings it back pending, over the ballots already cast
// and against the deadline already stamped.
//
// The count is what it resumed. The error is the docket not answering (entity.IsUnread): a
// listing that did not happen resumed nothing and settled nothing, and is asked for again.
func (e *Engine) ResumeSelections() (int, error) {
	resumed := 0
	ents := entity.Below(e.store, mirror.IsQuestion,
		1, entity.VerseRoot, mirror.QuestionSystem, mirror.QuestionKind)
	if err := entity.ReadFailed(e.store); err != nil {
		return 0, fmt.Errorf("the docket could not be read, so no selection was resumed: %w", err)
	}
	for _, ent := range ents {
		p := ent.Payload
		chain := entity.Str(p, "chain")
		if chain == "" {
			continue // a gate's question; the rule's replay re-poses it
		}
		if entity.Str(p, "status") == mirror.QuestionAnswered || entity.Str(p, "verdict") != "" {
			continue // settled: terminal-once, and nothing is waiting on a decided question
		}
		mine, said := e.ownsQuestion(p)
		if !mine {
			continue // another engine's charter asked it, and that engine resumes it
		}
		name := mirror.TriggerOf(chain)
		t, ok := e.triggerNamed(name)
		if !ok {
			// A rule HELD for a stratum that has not arrived is not a rule that is gone: it
			// installs the moment the stratum does. Nothing can be navigated through it in the
			// meantime, so the question is left standing rather than resumed or settled.
			if e.knowsTrigger(name) {
				continue
			}
			if !said && e.knownElsewhere(name) {
				// It does not say which charter it came from and the rule is in one of the
				// others, so it is not gone — it was never ours to settle (ownsQuestion).
				e.tracef("selection %s is another charter's rule %q: left standing", ent.Ref.Name(), name)
				continue
			}
			e.orphanQuestion(ent.Ref, "the rule that asked it is no longer in the charter")
			continue
		}
		act, ok := navigate(t, chain)
		if !ok || act.Spawn == nil || act.Spawn.Provider == nil {
			// The charter was edited under a live selection: the action moved, or no longer
			// selects a doer. There is nothing left to run, and saying so beats a ballot into
			// a void.
			e.orphanQuestion(ent.Ref, "the action that asked it is no longer in the charter")
			continue
		}
		subj, err := entity.ParseAddr(entity.Str(p, "subject"))
		if err != nil {
			e.orphanQuestion(ent.Ref, "it does not name a subject this verse can read")
			continue
		}
		// The subject itself, because the ballot is about work on it: an entity REMOVED while
		// the question stood leaves a question about something that is not there, and
		// answering it would start a task about a ghost. A store that cannot answer is left
		// alone rather than guessed about.
		if _, found, err := e.store.Get(subj); err == nil && !found {
			e.orphanQuestion(ent.Ref, "the subject it is about is gone")
			continue
		}
		e.tracef("resuming selection %s: %s on %s", ent.Ref.Name(), chain, subj)
		go e.resumeSelection(*act, chain, subj)
		resumed++
	}
	// At Info, not Debug: work is waiting on somebody, and "there are two selections on the
	// docket that this verse just picked back up" is the kind of thing an operator reading a
	// startup log should not have to turn tracing on to learn.
	if resumed > 0 {
		e.logger.Info("resumed open selections from the docket", "count", resumed)
	}
	return resumed, nil
}

// ownsQuestion answers whether a question is this engine's to resume or settle, by the
// charter it records having asked it (`at`), and whether the record says at all.
//
// A record with no `at` is baseline's to look at, being the engine that always exists. What
// it may not do is settle one on its own reading: "not in MY charter" is not "not in ANY
// charter", and for an unstamped record there is no way left to tell which it was. That is
// what knownElsewhere answers.
func (e *Engine) ownsQuestion(p entity.Payload) (mine, said bool) {
	if entity.Get(p, "at") == nil {
		return e.At() == "", false
	}
	return entity.Str(p, "at") == e.At(), true
}

// orphanQuestion settles a question nobody can answer, recording WHY. The record stays rather
// than being deleted: what was asked, and why it was never decided, is the trail.
func (e *Engine) orphanQuestion(ref entity.Ref, why string) {
	e.tracef("orphaning %s: %s", ref.Name(), why)
	e.modify(ref, func(cur entity.Payload) entity.Payload {
		if entity.Str(cur, "verdict") != "" {
			return cur // it settled while we were deciding it had not; terminal-once
		}
		p := clonePayload(cur)
		p["status"] = mirror.QuestionOrphaned
		p["orphanedAt"] = time.Now().UTC().Format(time.RFC3339)
		p["why"] = why
		return p.Payload()
	})
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
	// HEAD, not Rev, and this is the one call that made a resume expensive. Rev SYNCS — it
	// opens a watch on the root and is seeded with a read — and this runs once per motion, so
	// a verse coming back with a docket paid that per question: staging spent 9.954s here,
	// which is the session budget, so a timeout rather than work.
	//
	// Head is the session's known commit (libctl's mark, raised by every answer that carries
	// one), so it costs nothing and is the store's head rather than the last CHANGED commit —
	// which is what "as of now" means here. It can lag an idle store by a heartbeat; a resume
	// is not that, and this function already errs toward keeping a motion when it cannot tell.
	now, err := e.reader(t.Name).AsOf(e.store.Head(), paths(t.reads())...)
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

// selectsNow is stillSelects for a firing about to happen: whether the rule's `when` holds and its
// subject picks this entity out of the verse as it stands. It answers FALSE when it cannot tell,
// where stillSelects answers true: here the cost of a wrong yes is an action applied, and the
// next commit that touches what the rule reads asks again.
func (e *Engine) selectsNow(t Trigger, subj entity.Ref) bool {
	now, err := e.reader(t.Name).AsOf(e.store.Head(), paths(t.reads())...)
	if err != nil {
		return false
	}
	if t.Condition.When != nil {
		if ok, _ := t.Condition.When.Eval(now); !ok {
			return false
		}
	}
	m, err := e.conditionMatch(t.Condition.Subject, now)
	if err != nil {
		return false
	}
	return slices.Contains(m.Select(now), subj)
}

// Knows reports whether this engine's charter carries a rule by this name. It is knowsTrigger
// under a name another package can say, for the one caller that has to ask an engine about a
// rule it does not hold: versed, wiring KnowsElsewhere across the engines it runs.
func (e *Engine) Knows(name string) bool { return e.knowsTrigger(name) }

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
