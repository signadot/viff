package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
)

// A governed decision is ONE primitive with two faces. A whether-gate is a decision over
// the options {proceed, deny}; a who-select is a decision over {doerA, doerB, …}. In both,
// each option is "satisfied" by a tally over the shared vote log (or by a fact, or — for a
// default — by a deadline), the FIRST option to reach its threshold wins (earlier options
// win ties), and the winner is committed terminal-once with a revoke guard. This file holds
// that unified representation and the two compilers (Formula → decision, Provider →
// decision); the engine resolves it via openChannel / awaitDecision / commitDecision.

// decision is the engine-internal, reified governed question, built from a charter surface
// (a gate Formula or a launch.Provider) and resolved over the store.
type decision struct {
	channel  entity.Ref  // decision:channel:<id>
	subject  string      // the entity ref the decision is about
	axis     string      // "whether" | "select" — channel rendering + winner mapping
	options  []decOption // ORDERED: first to reach threshold wins; earlier wins ties
	deadline *decDeadline
	// provenance for the channel entity (verse decisions):
	trigger, gate, capability string
}

// decOption is one choosable outcome. It is satisfied when its formula holds (a vote quorum
// on a `choice`, or a fact presence); a nil formula marks a DEFAULT option, satisfied only
// when the deadline fires. `need`/`choice` are carried for the channel display.
type decOption struct {
	id      string
	need    int
	choice  string
	formula *Formula // nil = default (only the deadline satisfies it)
}

// decDeadline arms a clock: when it fires with no option yet satisfied, defaultOption wins.
// defaultOption "" means "no pick" (a select that reaches neither threshold nor a designated
// default — the caller does not spawn).
type decDeadline struct {
	within        string
	defaultOption string
}

// formulaOf returns the winning option's satisfy-formula — the recheck the commit is
// predicated on (the revoke guard). nil for a default/auto winner (no witness to guard).
func (d *decision) formulaOf(winner string) *Formula {
	for _, o := range d.options {
		if o.id == winner {
			return o.formula
		}
	}
	return nil
}

// decisionChannelID hashes a seed into the stable, re-fire-idempotent channel id shared by
// both axes (a re-fire reuses the same channel, terminal-once).
func decisionChannelID(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return fmt.Sprintf("%08x", h.Sum32())
}

// providerSpecSeed derives the select-axis id seed from the subject + eligibility spec
// (inline set, capability, match), so a re-fire reuses the channel. json.Marshal sorts map
// keys, so the hash is stable.
func providerSpecSeed(subject entity.Ref, p *launch.Provider) string {
	spec, _ := json.Marshal(struct {
		E []string       `json:"e"`
		C string         `json:"c"`
		M map[string]any `json:"m"`
	}{p.Eligible, p.Capability, p.Match})
	return "select/" + subject.String() + "/" + string(spec)
}

// compileFormula builds a whether-axis decision from a gate's Formula. The formula is bound
// to THIS channel's vote log (a quorum atom tallies vote:<did>:*). A bare atom is a single
// `proceed` option (waits until it holds). An `until[left, right]` is `proceed` (left) plus
// `deny` (right) — where right is either a store condition (deny when it holds) or a deadline
// (deny defaults when the clock strikes). Left/proceed wins a tie.
func (e *Engine) compileFormula(f *Formula, channel entity.Ref, t Trigger, gname string, ev entity.Event) *decision {
	f = f.bind(channel.ID)
	d := &decision{
		channel: channel, subject: ev.Ref.String(), axis: "whether",
		trigger: t.Name, gate: gname,
	}
	if f != nil && f.Op == OpUntil && len(f.Args) == 2 {
		d.options = append(d.options, optionFromFormula("proceed", f.Args[0]))
		if dur, isDeadline := deadlineDuration(f.Args[1]); isDeadline {
			d.deadline = &decDeadline{within: dur.String(), defaultOption: "deny"}
			d.options = append(d.options, decOption{id: "deny"}) // default: only the clock satisfies it
		} else {
			d.options = append(d.options, optionFromFormula("deny", f.Args[1]))
		}
		return d
	}
	// A bare atom: proceed when it holds, no deny side (waits like the old Monitor.Await).
	d.options = append(d.options, optionFromFormula("proceed", f))
	return d
}

// compileProvider builds a select-axis decision from a launch.Provider over its resolved
// eligible options: each doer is an option satisfied by a vote quorum on its own id. `ask`
// is a vote with threshold 1; `within` arms a no-pick deadline. (`auto` short-circuits in
// the caller — it never awaits.)
func (e *Engine) compileProvider(p *launch.Provider, subject, channel entity.Ref, options []string) *decision {
	need := p.Need
	if p.Select == "ask" {
		need = 1
	}
	if need < 1 {
		need = 1
	}
	d := &decision{
		channel: channel, subject: subject.String(), axis: "select",
		capability: p.Capability,
	}
	for _, opt := range options {
		d.options = append(d.options, decOption{
			id: opt, need: need, choice: opt,
			formula: quorumFormula(channel.ID, opt, need),
		})
	}
	if p.Within != "" {
		d.deadline = &decDeadline{within: p.Within, defaultOption: ""} // "" = no pick
	}
	return d
}

// optionFromFormula wraps a satisfy-formula (a bound quorum or fact atom) as a decision
// option, extracting its threshold/choice for the channel display.
func optionFromFormula(id string, f *Formula) decOption {
	o := decOption{id: id, formula: f, need: f.quorumNeed()}
	if o.need == 0 {
		o.need = 1 // a fact/presence atom is a quorum of one
	}
	if f != nil && f.Op == OpAtom && f.Req != nil {
		o.choice, _ = f.Req.Facts.Fields["choice"].(string)
	}
	return o
}

// quorumFormula builds a bound quorum atom that tallies vote:<did>:* where choice==opt,
// satisfied at ≥ need — the same shape Formula.bind produces for a whether quorum.
func quorumFormula(did, opt string, need int) *Formula {
	return &Formula{Op: OpAtom, Req: &Requirement{
		Kind: ReqQuorum, N: need,
		Facts: entity.Pattern{System: "vote", Kind: did, Fields: map[string]any{"choice": opt}},
	}}
}

// openChannel writes the discoverable decision:channel entity (unified across both axes) so
// a voter can find it (`verse decisions`) and observe the tally. Idempotent: it never
// reopens a channel that has already closed (an `outcome` is present).
func (e *Engine) openChannel(d *decision) {
	e.store.Modify(d.channel, func(cur entity.Payload) entity.Payload {
		if _, done := cur["outcome"]; done {
			return cur // already closed — leave it
		}
		p := clonePayload(cur)
		p["axis"] = d.axis
		p["status"] = "open"
		p["subject"] = d.subject
		opts := make([]any, 0, len(d.options))
		for _, o := range d.options {
			om := map[string]any{"id": o.id}
			if o.need > 0 {
				om["need"] = o.need
			}
			if o.choice != "" {
				om["choice"] = o.choice
			}
			opts = append(opts, om)
		}
		p["options"] = opts
		if d.trigger != "" {
			p["trigger"] = d.trigger
		}
		if d.gate != "" {
			p["gate"] = d.gate
		}
		if d.capability != "" {
			p["capability"] = d.capability
		}
		return p
	})
}

// awaitDecision resolves a decision: it watches the union of the options' vote/fact subtrees
// and returns the FIRST option (in slice order — earlier wins ties) to reach its threshold.
// An optional deadline defaults to the designated option when the clock strikes with nothing
// yet satisfied ("" = no pick). ok is false only if ctx ends first. Generalizes the old
// awaitUntil (2 options) and awaitPlurality (N options) into one.
func (e *Engine) awaitDecision(ctx context.Context, d *decision) (winner string, witness []entity.Ref, ok bool) {
	var deadlineC <-chan time.Time
	if d.deadline != nil {
		if dur, err := time.ParseDuration(d.deadline.within); err == nil {
			at := e.deadlineAt(d.channel, dur) // stamp/read (restart-safe)
			timer := time.NewTimer(at.Sub(e.now()))
			defer timer.Stop()
			deadlineC = timer.C
		}
	}
	check := func() (string, []entity.Ref, bool) {
		for _, o := range d.options {
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
	sw, ok2 := e.store.(entity.SubtreeWatcher)
	if !ok2 {
		return "", nil, false
	}
	sub, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan entity.Event, 64)
	seen := map[[2]string]bool{} // one watch per distinct subtree (options often share the vote log)
	for _, o := range d.options {
		if o.formula == nil {
			continue
		}
		for _, st := range o.formula.Subtrees() {
			if seen[st] {
				continue
			}
			seen[st] = true
			ch, unsub, err := sw.WatchSubtree(st[0], st[1])
			if err != nil {
				continue
			}
			defer unsub()
			go drain(sub, ch, events)
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
			return d.deadline.defaultOption, nil, true
		case <-events:
			if id, w, decided := check(); decided {
				return id, w, true
			}
		}
	}
}
