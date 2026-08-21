package trigger

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// A governed motion is ONE primitive with two faces. A whether-gate is a motion over
// the options {proceed, deny}; a who-select is a motion over {doerA, doerB, …}. In both,
// each option is "satisfied" by a tally over the shared vote log (or by a fact, or — for a
// default — by a deadline), the FIRST option to reach its threshold wins (earlier options
// win ties), and the winner is committed terminal-once with a revoke guard. This file holds
// that unified representation and the two compilers (Formula → motion, Provider →
// motion); the engine resolves it via openChannel / awaitMotion / commitMotion.

// Where a motion stands: motion:channel:<id>. Named once because a channel is written by
// two compilers, read by the docket, and walked by the resume — and an address spelled out
// at each of those is one that drifts at one of them.
const (
	motionSystem      = "motion"
	motionChannelKind = "channel"
)

// motion is the engine-internal, reified governed question, built from a charter surface
// (a gate Formula or a launch.Provider) and resolved over the store.
type motion struct {
	channel  entity.Ref     // motion:channel:<id>
	subject  string         // the entity ref the motion is about
	axis     string         // "whether" | "select" — channel rendering + winner mapping
	options  []motionOption // ORDERED: first to reach threshold wins; earlier wins ties
	deadline *motionDeadline
	// provenance for the channel entity (`verse motion`):
	trigger, gate, capability string
	// chain is the kpath of the ACTION this motion belongs to, and it is provenance with a
	// second job: it is the lookup key back into the charter that lets a restart find the
	// continuation a who-motion is blocking — see ResumeMotions. Empty on a whether-motion,
	// whose continuation is the firing itself and comes back with the rule's own replay.
	chain string
	// moot answers whether the question this motion asks has stopped being about anything —
	// the rule no longer selects the subject. It is a closure rather than a field of data
	// because the answer is a read of the verse as it stands, and it is nil for a motion whose
	// staleness nobody can decide (no subject, or no rule to ask).
	//
	// It is what makes the withdrawal PROMPT. The same question is asked at the commit and at a
	// restart; without a wake on the subject's own subtree, a motion whose subject left the
	// selection sat open offering a decision about work that will never be taken, until
	// somebody voted or the verse came back (git-issue 1my63vm6h12ks6bng9n0).
	moot func() bool
}

// motionOption is one choosable outcome. It is satisfied when its formula holds (a vote quorum
// on a `choice`, or a fact presence); a nil formula marks a DEFAULT option, satisfied only
// when the deadline fires. `need`/`choice` are carried for the channel display.
//
// `grant`/`tools` are the ENVELOPE a select-axis option would run under, carried for the same
// display. They exist because the options on a who-motion stopped being interchangeable: once
// a doer brings its own grant and tool set, picking between two names is picking between two
// authorities, and a ballot that shows only names asks the group to authorise a capability it
// was never shown. Empty on a whether-motion, and on any doer that adds nothing to the spawn.
type motionOption struct {
	id      string
	need    int
	choice  string
	grant   string
	tools   []string
	formula *Formula // nil = default (only the deadline satisfies it)
}

// motionDeadline arms a clock: when it fires with no option yet satisfied, defaultOption wins.
// defaultOption "" means "no pick" (a select that reaches neither threshold nor a designated
// default — the caller does not spawn).
type motionDeadline struct {
	within        string
	defaultOption string
}

// guardFor returns the revoke guard for a winner about to be committed: the winning
// option's satisfy-formula, but ONLY when there is a witness to guard. A win with no
// witness is a deadline default or an auto pick — nothing was retracted, so there is
// nothing to recheck, and predicating the commit on a formula that never held would make
// every such commit read as revoked (an unbounded re-monitor loop).
func (m *motion) guardFor(winner string, witness []entity.Ref) *Formula {
	if len(witness) == 0 {
		return nil
	}
	return m.formulaOf(winner)
}

// formulaOf returns the winning option's satisfy-formula — the recheck the commit is
// predicated on (the revoke guard). nil for a default/auto winner (no witness to guard).
func (m *motion) formulaOf(winner string) *Formula {
	for _, o := range m.options {
		if o.id == winner {
			return o.formula
		}
	}
	return nil
}

// motionChannelID hashes a seed into the stable, re-fire-idempotent channel id shared by
// both axes (a re-fire reuses the same channel, terminal-once).
func motionChannelID(seed string) string {
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

// compileFormula builds a whether-axis motion from a gate's Formula. The formula is bound
// to THIS channel's vote log (a quorum atom tallies vote:<mid>:*). A bare atom is a single
// `proceed` option (waits until it holds). An `until[left, right]` is `proceed` (left) plus
// `deny` (right) — where right is either a store condition (deny when it holds) or a deadline
// (deny defaults when the clock strikes). Left/proceed wins a tie.
// The subject is the FIRING's subject, not the ref of the change that dispatched it — the
// same distinction the channel id already makes one line up in monitorGate. A motion is a
// decision about the work, and the work is the subject; keying the id on one and displaying
// the other let `verse motion` name the doorbell (and name nothing at all on a replay, where
// a level-triggered firing has no dispatching event to speak of).
func (e *Engine) compileFormula(f *Formula, channel entity.Ref, t Trigger, gname string, subject entity.Ref) *motion {
	f = f.bind(channel.ID())
	m := &motion{
		channel: channel, subject: subject.String(), axis: "whether",
		trigger: t.Name, gate: gname,
	}
	// Whether the question is still about anything. Only a whether-motion gets one: it is the
	// rule's own selection that decides, and a who-motion is a question about a spawn that has
	// already been decided to happen.
	if subject != (entity.Ref{}) {
		m.moot = func() bool { return !e.stillSelects(t, subject) }
	}
	if f != nil && f.Op == OpUntil && len(f.Args) == 2 {
		m.options = append(m.options, optionFromFormula("proceed", f.Args[0]))
		if dur, isDeadline := deadlineDuration(f.Args[1]); isDeadline {
			m.deadline = &motionDeadline{within: dur.String(), defaultOption: "deny"}
			m.options = append(m.options, motionOption{id: "deny"}) // default: only the clock satisfies it
		} else {
			m.options = append(m.options, optionFromFormula("deny", f.Args[1]))
		}
		return m
	}
	// A bare atom: proceed when it holds, no deny side (waits like the old Monitor.Await).
	m.options = append(m.options, optionFromFormula("proceed", f))
	return m
}

// compileProvider builds a select-axis motion from a launch.Provider over its resolved
// eligible options: each doer is an option satisfied by a vote quorum on its own id. `ask`
// is a vote with threshold 1; `within` arms a no-pick deadline. (`auto` short-circuits in
// the caller — it never awaits.)
func (e *Engine) compileProvider(p *launch.Provider, subject, channel entity.Ref, options []string,
	frags map[string]launch.Fragment, chain string) *motion {
	need := p.Need
	if p.Select == "ask" {
		need = 1
	}
	if need < 1 {
		need = 1
	}
	m := &motion{
		channel: channel, subject: subject.String(), axis: "select",
		capability: p.Capability, chain: chain,
	}
	for _, opt := range options {
		f := frags[opt]
		m.options = append(m.options, motionOption{
			id: opt, need: need, choice: opt,
			grant: f.Grant, tools: f.Tools,
			formula: quorumFormula(channel.ID(), opt, need),
		})
	}
	if p.Within != "" {
		m.deadline = &motionDeadline{within: p.Within, defaultOption: ""} // "" = no pick
	}
	return m
}

// optionFromFormula wraps a satisfy-formula (a bound quorum or fact atom) as a motion
// option, extracting its threshold/choice for the channel display.
func optionFromFormula(id string, f *Formula) motionOption {
	o := motionOption{id: id, formula: f, need: f.quorumNeed()}
	if o.need == 0 {
		o.need = 1 // a fact/presence atom is a quorum of one
	}
	if f != nil && f.Op == OpAtom && f.Req != nil {
		o.choice = f.Req.Facts.FieldString("choice")
	}
	return o
}

// quorumFormula builds a bound quorum atom that tallies vote:<mid>:* where choice==opt,
// satisfied at ≥ need — the same shape Formula.bind produces for a whether quorum.
func quorumFormula(mid, opt string, need int) *Formula {
	return &Formula{Op: OpAtom, Req: &Requirement{
		Kind: ReqQuorum, N: need,
		Facts: Match{mKeySystem: "vote", mKeyKind: mid, mKeyValue: map[string]any{"choice": opt}},
	}}
}

// openChannel writes the discoverable motion:channel entity (unified across both axes) so
// a voter can find it (`verse motion`) and observe the tally. Idempotent: it never
// reopens a channel that has already closed (an `outcome` is present).
func (e *Engine) openChannel(m *motion) {
	e.modify(m.channel, func(cur entity.Payload) entity.Payload {
		if entity.Get(cur, "outcome") != nil {
			return cur // already closed — leave it
		}
		p := clonePayload(cur)
		p["axis"] = m.axis
		p["status"] = "open"
		p["subject"] = m.subject
		opts := make([]any, 0, len(m.options))
		for _, o := range m.options {
			om := map[string]any{"id": o.id}
			if o.need > 0 {
				om["need"] = o.need
			}
			if o.choice != "" {
				om["choice"] = o.choice
			}
			// What this option would run as — see motionOption. Written only when the doer
			// says something, so a ballot between interchangeable doers stays as terse as it
			// was.
			if o.grant != "" {
				om["grant"] = o.grant
			}
			if len(o.tools) > 0 {
				om["tools"] = o.tools
			}
			// A DEFAULT OPTION IS NOT A THRESHOLD, and the ballot has to say so. `!until
			// [!quorum 2, !deadline "24h"]` decides on the quorum or on the clock, so no number
			// of ballots for `deny` closes it: two people denied and the motion stayed open with
			// the subject untouched (git-issue 88r53xm6h12ks6bng9n0). Rendered beside the
			// others with no `need`, it read as the approve option's symmetric twin.
			//
			// It is MARKED rather than removed or given a threshold of its own, because a
			// denial is worth recording even when it cannot close anything — dissent on the
			// record is the point of a vote log — and inventing a deny-quorum is a change to
			// what `!until` means, not to how it is displayed.
			if m.deadline != nil && o.formula == nil && o.id == m.deadline.defaultOption {
				om["reachedBy"] = "deadline"
			}
			opts = append(opts, om)
		}
		p["options"] = opts
		if m.trigger != "" {
			p["trigger"] = m.trigger
		}
		// WHICH CHARTER's rule that is. `trigger` is a name, and a name is unique inside a
		// charter rather than across them, so a docket entry saying `train-run` names a rule
		// only if you already know where to look. It is also what tells one engine's motions
		// from another's when a restart comes to settle them (ResumeMotions).
		if at := e.At(); at != "" {
			p["at"] = at
		}
		if m.gate != "" {
			p["gate"] = m.gate
		}
		if m.capability != "" {
			p["capability"] = m.capability
		}
		// The action this motion blocks, written down because nothing else on the channel
		// says it: a who-motion names a subject and a capability, and neither answers "which
		// rule is waiting on this ballot". It is what ResumeMotions navigates back through,
		// and what a docket reader follows to the words that asked.
		if m.chain != "" {
			p["chain"] = m.chain
		}
		return p.Payload()
	})
}

// awaitMotion resolves a motion: it watches the union of the options' vote/fact subtrees
// and returns the FIRST option (in slice order — earlier wins ties) to reach its threshold.
// An optional deadline defaults to the designated option when the clock strikes with nothing
// yet satisfied ("" = no pick). ok is false only if ctx ends first. Generalizes the old
// awaitUntil (2 options) and awaitPlurality (N options) into one.
func (e *Engine) awaitMotion(ctx context.Context, m *motion) (winner string, witness []entity.Ref, ok bool) {
	var deadlineC <-chan time.Time
	if m.deadline != nil {
		if dur, err := time.ParseDuration(m.deadline.within); err == nil {
			at := e.deadlineAt(m.channel, dur) // stamp/read (restart-safe)
			timer := time.NewTimer(at.Sub(e.now()))
			defer timer.Stop()
			deadlineC = timer.C
		}
	}
	check := func() (string, []entity.Ref, bool) {
		for _, o := range m.options {
			if o.formula == nil {
				continue // a default option is satisfied only by the deadline
			}
			if held, w := o.formula.Eval(e.store); held {
				return o.id, w, true
			}
		}
		return "", nil, false
	}
	if id, w, decided := check(); decided {
		return id, w, true
	}
	sub, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan entity.Commit, 64)
	seen := map[Subtree]bool{} // one watch per distinct subtree (options often share the vote log)
	watch := func(st Subtree) {
		if seen[st] {
			return
		}
		seen[st] = true
		ch, unsub, err := e.store.WatchSubtree(st.Depth(), st.Path()...)
		if err != nil {
			return
		}
		context.AfterFunc(sub, unsub)
		go drain(sub, ch, events)
	}
	// THE SUBJECT'S OWN SUBTREE, beside the vote log. The options say when the question is
	// ANSWERED; this says when it stops being a question — see motion.moot. Nothing here
	// decides on it, which is deliberate: the moot check runs on the event, and the withdrawal
	// itself is the caller's, where the reason can be written in the rule's own terms.
	if m.moot != nil && m.subject != "" {
		if ref, err := entity.ParseAddr(m.subject); err == nil {
			watch(SubtreeAt(ref.Segs()...))
		}
	}
	for _, o := range m.options {
		if o.formula == nil {
			continue
		}
		for _, st := range o.formula.Subtrees() {
			watch(st)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return "", nil, false
		case <-deadlineC: // the clock struck — an option that just made it wins; else the default
			if id, w, decided := check(); decided {
				return id, w, true
			}
			return m.deadline.defaultOption, nil, true
		case <-events:
			if id, w, decided := check(); decided {
				return id, w, true
			}
			// Nothing is decided. Ask whether there is still anything to decide: a subject that
			// left the rule's selection makes the question moot, and the caller withdraws it.
			if m.moot != nil && m.moot() {
				return "", nil, false
			}
		}
	}
}

// drain forwards one subtree's commits onto the shared event channel, so a motion whose
// options read several subtrees waits on ONE channel rather than a select over N. It moved
// here from monitor.go, whose only other caller was a goal's doneWhen — a fan-in helper
// belongs with whoever still fans in.
func drain(ctx context.Context, in <-chan entity.Commit, out chan<- entity.Commit) {
	for {
		select {
		case c, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
