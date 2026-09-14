// Package trigger is the ECA core: it observes the entity-verse, matches triggers
// against each change, gates the ones that match, and fires their actions. This is
// the loop that makes SDLC flows *emergent* — no one authors the path; it falls
// out of triggers reacting to state that other actions write.
//
// Two grammars carry the whole model, and keeping them apart is the point:
//
//	Match   — POINT-IN-TIME: which entities satisfy this right now. The atom, and the
//	          leaf of every Formula. A Subject and a flow address are bare Matches.
//	Formula — a Match asked of the verse's evolution: at two revisions for a cause (`on`),
//	          at one for a condition (`when`). Nothing composes it over time.
//
// Name entities at an instant → a Match; ask one of the store as it moves → a Formula. See
// docs/trigger-model.md.
package trigger

import (
	"fmt"
	"strings"
	"sync"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
)

// Trigger is one ECA rule — fully declarative (JSON-serializable, no Go closure), so a
// charter can be installed over the wire, persisted, or reflected as an entity, and the
// engine interprets it directly (there is no separate compiled form). When the Condition
// holds and every Gate resolves to proceed, the Action fires. Gates are conjunctive
// (all-of); an empty Gates fires with no approval.
//
//tony:schemagen=verse-trigger,notag,comment=Doc,lineComment=Note
type Trigger struct {
	// Doc and Note are the comments the rule was WRITTEN with — the lines above it, and the one
	// beside it — carried through the decode and re-encode a charter makes on its way into the
	// store. Nothing reads them and no rule acts on them; they are here so the record is the
	// rule as its author left it, reasons included.
	//
	// A rule is persisted from this struct (`persist` writes `t.ToTonyIR()`), so without them an
	// installed charter came back stripped of every comment except those inside a field carried
	// as a raw node — `action.patch` kept its notes and the lines above `name:` did not, which
	// is the arbitrary half of a document surviving that is worse than none of it.
	//
	// `tony:"-"` because they ride the NODE rather than being fields of the document: a head
	// comment wraps the value it precedes, a line comment sits on the value it follows. Codegen
	// leaves them out of the field set for the same reason (isCommentCarrier), so neither path
	// spells them as keys.
	//
	// Needs go-tony v0.0.142, which built the encode half of these annotations; before it they
	// captured on the way in and were dropped on the way out.
	Doc  []string `tony:"-"`
	Note []string `tony:"-"`

	// The per-FIELD comment carriers: <Field>Doc is the lines written above that field,
	// <Field>Note the one written beside its value.
	//
	// ConditionDoc is the reason the field form exists at all. A struct-level `comment=`
	// captures the comment on a struct's OWN node, and Condition has no generated codec to
	// capture it with (its codec is hand-written). The field-level form (go-tony v0.0.145) names
	// the field's VALUE instead, which exists whatever the value turns out to be.
	//
	// The Notes are the same argument for the other position. A struct-level `lineComment=`
	// carries the note beside the RULE (`- {name: r} # …` in a list); the note beside a FIELD's
	// value is a different place in the document, and a scalar field has no type of its own to
	// carry it — which is why `name: comment # this is a comment`, about as ordinary a line as a
	// charter contains, was the one comment that reached the store and came back missing.
	//
	// GatesDoc takes the comment above `gates:` AND the one above its first entry, which are one
	// comment: a block list begins at its first element, so both head the list. Every later
	// entry keeps its own, carried by Gate itself.
	//
	// `gating:` and `action:` are absent here on purpose: Gating and Action have generated
	// codecs of their own, so their struct-level comment=/lineComment= already carry both
	// positions. A second carrier on the field would capture the same lines twice.
	NameDoc       []string `tony:"-"`
	NameNote      []string `tony:"-"`
	StratumDoc    []string `tony:"-"`
	StratumNote   []string `tony:"-"`
	ConditionDoc  []string `tony:"-"`
	ConditionNote []string `tony:"-"`
	GatesDoc      []string `tony:"-"`
	GatesNote     []string `tony:"-"`

	Name string `tony:"field=name,comment=NameDoc,lineComment=NameNote"`
	// Stratum names the level this rule lives in — a trigger:strata:<name> entity carrying
	// the pattern its subject must fall inside, and the level a derive must climb above.
	// Empty is the IMPLICIT stratum: unordered, checked against nothing, which is exactly
	// what every rule written before strata existed means. See stratum.go.
	Stratum   string    `tony:"field=stratum,omitzero,comment=StratumDoc,lineComment=StratumNote"`
	Condition Condition `tony:"field=condition,comment=ConditionDoc,lineComment=ConditionNote"`
	// conjunctive (all must proceed)
	Gates []Gate `tony:"field=gates,omitzero,comment=GatesDoc,lineComment=GatesNote"`
	// Gating groups the two facts about a rule's OWN gates: which they are, and who answers
	// them. `gates:` at the top level still works and means `gating: {gates: …}` — the two
	// are one field read two ways, not two fields that can disagree, and writing both is
	// refused rather than merged.
	//
	// It exists because a resolver had nowhere to be named for a FIRING's gates. A spawn
	// says `resolver:` for the run it starts; a rule had no counterpart, so the named door
	// was open on one side of the design and shut on the other.
	Gating *Gating `tony:"field=gating,omitzero"`
	Action Action  `tony:"field=action"`
}

// Gating is a rule's own gates and who answers them.
//
//tony:schemagen=verse-gating,notag,comment=Doc,lineComment=Note
type Gating struct {
	// Doc and Note are the comments this was WRITTEN with — the lines above it, and the one
	// beside it. See Trigger.Doc: they ride the node rather than being fields of the document,
	// and they exist so a charter read back is the one its author wrote.
	Doc  []string `tony:"-"`
	Note []string `tony:"-"`
	// The per-field carriers, as on Trigger: a scalar field has no type of its own to hold
	// what was written about it.
	ResolverDoc  []string `tony:"-"`
	ResolverNote []string `tony:"-"`
	GatesDoc     []string `tony:"-"`
	GatesNote    []string `tony:"-"`
	// Resolver names the binding that answers these gates — `resolvers:<kind>:<name>`. Same
	// spelling as a spawn's `resolver:` on purpose: it is the same act, naming an answerer,
	// and one word for it is worth more than either word being marginally better in its own
	// place. Empty leaves the question to the standing layer, then to the human docket.
	Resolver string `tony:"field=resolver,omitzero,comment=ResolverDoc,lineComment=ResolverNote"`
	Gates    []Gate `tony:"field=gates,omitzero,comment=GatesDoc,lineComment=GatesNote"`
}

// AllGates is the rule's gates however they were written.
func (t Trigger) AllGates() []Gate {
	if t.Gating != nil && len(t.Gating.Gates) > 0 {
		return t.Gating.Gates
	}
	return t.Gates
}

// GateResolver is the binding named to answer this rule's own gates, empty for none.
func (t Trigger) GateResolver() string {
	if t.Gating == nil {
		return ""
	}
	return t.Gating.Resolver
}

// Condition is the ECA "cause": what the firing is about, and what fires it. Subject and
// When are grouped rather than nested because the Subject is resolved at the instant When
// fires — there is no subject before the match — yet neither field owns the other. Two
// halves of "about Y, when X", in two DIFFERENT grammars: Subject is point-in-time, When
// is temporal.
//
// The REQUIRED half is the Subject. An implicit subject — "whatever entity the when
// matched" — reads fine on a when that pins one system:kind, and has no coherent general
// meaning at all: `{system: demo}` already spans kinds, and a disjunctive when (`!or [a:b:c,
// {system: d, kind: e}]`) makes it plain, since "the matched entity" answers *which entity
// fired* when the question is *what is this rule about*. A trigger that acts on whichever
// disjunct happened to fire is not a rule anyone wrote down.
//
//tony:schemagen=verse-condition,notag,codec=custom
type Condition struct {
	// SubjectDoc/OnDoc/WhenDoc are the comments written above those three fields, and the
	// Notes the ones written beside their values.
	//
	// They are captured and re-emitted by Condition's OWN codec (tonycodec.go), which is what
	// this case needs and the annotations cannot supply: `comment=` is emitted by codegen, and
	// codegen writes no codec for a `codec=custom` type. Every other comment in a charter is
	// carried either by the value's type or by the field holding it in a GENERATED parent; a
	// field of a hand-written codec has neither, so the codec does it by hand.
	SubjectDoc  []string `tony:"-"`
	SubjectNote []string `tony:"-"`
	OnDoc       []string `tony:"-"`
	OnNote      []string `tony:"-"`
	WhenDoc     []string `tony:"-"`
	WhenNote    []string `tony:"-"`

	// Subject is what the firing is ABOUT, selected at the fire instant — never
	// accumulated, so it carries no temporal operators. An address names one entity; a
	// system+kind selector is a pattern — the changed entity when there is no When,
	// otherwise the firing's own subject (a reflected task's `flow`) when it carries
	// one, else the SET of matching entities in the store, one action per member.
	Subject Match `tony:"field=subject"`
	// On is the CAUSE, as a rising edge of a store predicate: the trigger fires at the commit
	// where `on` first holds, having not held at the commit before. Optional.
	//
	// It is the same grammar as When and evaluated the same way — a predicate over the store,
	// satisfied when something matching it exists — differing only in WHEN it is asked. `on`
	// is asked twice, at C-1 and at C, and fires on ¬P → P. `when` is asked once, at C, and
	// gates. Cause and condition, in one language, told apart by edge and level.
	//
	// A trigger carrying one dispatches by that rule and by nothing else: the subject is a
	// selection over the store at C, one firing per member, and a change to a subject entity
	// is not a cause. A trigger without one keeps the older dispatch (below), which is what
	// every charter written so far relies on.
	//
	// Two consequences worth knowing before writing one. An EDGE DOES NOT RE-FIRE while it
	// stays true: `on: {system: demo, kind: pr, value: {stage: reviewed}}` rises when the
	// first PR reaches `reviewed` and says nothing about the second, because the predicate was
	// already true. And an `on` trigger does NOT act on the backlog — installed over a verse
	// where its predicate already holds, there is no rising edge to observe, where a
	// subject-dispatched trigger acts on everything already standing (see the level-triggered
	// seed in docs/change-events.md).
	On *Formula `tony:"field=on,omitzero"`
	// When fires the trigger. Its atom form is a single match, dispatched on the edge of
	// one change event; temporal operators relate the verse's evolution instead.
	//
	// Optional, and an omitted When IS the Subject: the ordinary rule reacts to the thing it
	// is about ("when a pr goes open, review that pr"), so saying the same match twice would
	// be the common case paying for the general one. A When is what a trigger adds when the
	// cause and the subject come apart — a completion trigger firing on `task:instance`
	// but acting on the PR, or one change advancing a whole selected set.
	When *Formula `tony:"field=when,omitzero"`
}

// validate reports whether the condition is installable. It is deliberately strict: the
// subject is what every other part of a firing is stated relative to, and the "when" is where
// complexity hides, so a charter that cannot mean what it says fails at install rather than
// silently matching nothing.
func (c Condition) validate() error {
	if err := c.validateOn(); err != nil {
		return err
	}
	if len(c.Subject) == 0 {
		return fmt.Errorf(`condition.subject required: a trigger has to say what it acts on, ` +
			`because nothing else in it does — the action has no target of its own, and a when ` +
			`that spans kinds has no one entity to inherit. Name it ("demo.pr.1"), or select a ` +
			`kind ({system: demo, kind: pr}). A subject alone is a complete condition: it fires ` +
			`on changes to what it names`)
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("condition.subject: %w", err)
	}
	// A read inside the subject's predicate: its shape, and the one thing it may not address.
	// Checked here so a rule that cannot mean what it says fails at install, where its author
	// is, rather than at a firing they are not watching.
	if err := checkConditionReads(c.Subject); err != nil {
		return fmt.Errorf("condition.subject: %w", err)
	}
	if c.On != nil {
		// The cause is `on`, and validateOn has already checked what the edge rule reads. A
		// `when` beside it is the level guard, checked below like any other.
		if c.When == nil {
			return nil
		}
	} else if c.When == nil {
		// The subject is the cause too: this fires on changes to what it is about, one firing
		// per change, so the subject is never asked to select a set out of the store and needs
		// no definite kind. `{system: demo}` is a complete condition — every demo entity that
		// changes, about the one that changed.
		return nil
	}
	if c.On == nil {
		_, names := c.Subject.Names()
		if !names && !c.Subject.Selects() {
			return fmt.Errorf(`condition.subject must be an address ("demo.pr.1", "demo.pr.*") or name ` +
				`both a system and a kind when the condition has its own when: the change that fires ` +
				`is not the subject then, so the subject has to select out of the store, and it has to ` +
				`name a definite kind to select and spawn for`)
		}
	}
	if !c.When.IsMatchAtom() {
		return fmt.Errorf("condition.when carries temporal operators, which are not evaluated " +
			"yet: a temporal when has no change to dispatch on, so it needs a long-lived monitor " +
			"over the subtrees it reads (see docs/trigger-model.md). Use a point-in-time " +
			"match here; temporal composition works today in a gate's `formula`")
	}
	if err := c.When.Req.Facts.Validate(); err != nil {
		return fmt.Errorf("condition.when: %w", err)
	}
	return nil
}

// validateOn checks the cause half of the condition. An `on` trigger dispatches by the edge
// rule, so what it needs is exactly what that rule reads: a predicate the store can answer at
// two revisions, a subject that selects, and a system to watch.
func (c Condition) validateOn() error {
	if c.On == nil {
		return nil
	}
	if !c.On.IsMatchAtom() {
		return fmt.Errorf("condition.on carries temporal operators, which are not evaluated " +
			"yet: `on` is a store predicate asked at two revisions, so it has to be answerable " +
			"at one. Use a point-in-time match; temporal composition works in a gate's " +
			"`formula`")
	}
	if err := c.On.Req.Facts.Validate(); err != nil {
		return fmt.Errorf("condition.on: %w", err)
	}
	if c.On.Req.Facts.System() == "" {
		return fmt.Errorf("condition.on needs a system: it names the subtree whose changes are " +
			"the cause, and a watch path cannot be a wildcard (kind is optional)")
	}
	_, names := c.Subject.Names()
	if !names && !c.Subject.Selects() {
		return fmt.Errorf(`condition.subject must be an address ("demo.pr.1", "demo.pr.*") or ` +
			`name both a system and a kind when the condition has an ` + "`on`" + `: the cause is ` +
			`not the subject then, so the subject selects out of the store, and it has to name a ` +
			`definite kind to select and spawn for`)
	}
	return nil
}

// Gate is one decision point on a trigger firing. A trigger's gates are conjunctive — every
// one must resolve positively for the action to fire (e.g. a deploy needing both a security
// and a release-manager approval). Each poses a QUESTION with two options unless it says
// otherwise: `need` is how many ballots `proceed` takes, `options` names outcomes of the
// gate's own, and `deadline` says how long the question stands and what it settles on when
// the clock passes. A gate that says none of these is answered by one person, yes or no.
//
//tony:schemagen=verse-trigger-gate,notag,comment=Doc,lineComment=Note
type Gate struct {
	// Doc and Note are the comments this was WRITTEN with — the lines above it, and the one
	// beside it. See Trigger.Doc: they ride the node rather than being fields of the document,
	// and they exist so a charter read back is the one its author wrote.
	Doc  []string `tony:"-"`
	Note []string `tony:"-"`
	// The per-field carriers, as on Trigger.
	NameDoc  []string `tony:"-"`
	NameNote []string `tony:"-"`
	ModeDoc  []string `tony:"-"`
	ModeNote []string `tony:"-"`
	Name     string   `tony:"field=name,omitzero,comment=NameDoc,lineComment=NameNote"`
	// auto | ask | park. Empty is auto, unless the gate carries a threshold, options or a
	// deadline, which only make sense for a question somebody is asked; then it is ask.
	Mode string `tony:"field=mode,omitzero,comment=ModeDoc,lineComment=ModeNote"`
	// Need is how many ballots settle `proceed`; one `deny` still refuses. It is the short
	// spelling of `options: [{id: proceed, need: N}, {id: deny, need: 1}]`, and the two
	// are not written together.
	Need int `tony:"field=need,omitzero"`
	// Options are the gate's own outcomes, each with its threshold, for a question that is
	// not yes or no — or one where a refusal takes more than one voice. Ordered: the first
	// to reach its threshold settles the question.
	Options []task.Option `tony:"field=options,omitzero"`
	// Deadline is how long the question stands, and the option it settles on when the clock
	// passes with none at its threshold. An empty default settles on nothing, which the
	// firing reads as a refusal nobody made.
	Deadline *task.Deadline `tony:"field=deadline,omitzero"`
	// Answerers is who the question is held out to, when the gate says: grantees, the way a
	// grant names them — a person, a kind (`user`, `agent`), `*`, a group's or a binding's
	// address. Written as the question's `read-write` grant when it is posed, in place of the
	// docket's default, so a gate naming davido is davido's to answer and the charter's owner's,
	// who co-owns what their rule asked. `need` is how many of them it takes.
	Answerers []string `tony:"field=answerers,omitzero"`
	// OnDenied is what a NO means for the subject — an action applied when this gate
	// refuses, about the same subjects the firing was about.
	//
	// It exists because a denied firing otherwise writes nothing at all (see Engine.fire:
	// "no budget consumed"), so the subject is left in the state that asked the question.
	// Any later commit that touches it re-selects it, the rule fires again, and the gate is
	// re-posed under its deterministic id — over an answer somebody already gave. No rule
	// shape can author its way out of that, because the latch a well-formed rule carries
	// lives in the action, and on a deny the action never runs.
	//
	// It hangs off the GATE rather than the action because a no is the gate's outcome: two
	// gates on one rule refuse for different reasons, and each says what its own refusal
	// means. That also keeps it clear of onDone/onFailed/onCancelled/onOrphaned, which are
	// completion hooks for a spawn — a denied gate has no task to complete, which is why
	// Action.validate requires a spawn for those and must not for this one.
	OnDenied *Action `tony:"field=onDenied,omitzero"`
}

// validate refuses a gate whose question cannot be posed as written. Every refusal names
// the spelling that would work, because each of these is a plausible thing to write.
func (g Gate) validate() error {
	if g.Need < 0 {
		return fmt.Errorf("need %d: a threshold is at least one", g.Need)
	}
	if g.Need > 0 && len(g.Options) > 0 {
		return fmt.Errorf("`need` and `options` are written together; a threshold per " +
			"option is spelled on the option")
	}
	seen := map[string]bool{}
	for _, o := range g.Options {
		if o.ID == "" {
			return fmt.Errorf("an option has no id")
		}
		if seen[o.ID] {
			return fmt.Errorf("option %q is written twice", o.ID)
		}
		seen[o.ID] = true
		if o.Need < 0 {
			return fmt.Errorf("option %q: need %d: a threshold is at least one", o.ID, o.Need)
		}
	}
	if g.Deadline != nil {
		d, err := g.Deadline.After()
		if err != nil {
			return fmt.Errorf("deadline: %w (quote the duration: within: \"24h\")", err)
		}
		if d <= 0 {
			return fmt.Errorf("deadline: within %q: a deadline is in the future", g.Deadline.Within)
		}
		if dflt := g.Deadline.Default; dflt != "" {
			opts := g.options()
			if opts == nil {
				opts = task.DefaultOptions()
			}
			if _, ok := task.Need(opts, dflt); !ok {
				return fmt.Errorf("deadline default %q is not one of this gate's options (%s)",
					dflt, strings.Join(task.OptionIDs(opts), ", "))
			}
		}
	}
	if g.asks() && strings.EqualFold(g.Mode, "auto") {
		return fmt.Errorf("mode auto asks nobody, so a threshold, options or a deadline have " +
			"nothing to settle; drop them or drop the mode")
	}
	return nil
}

// mode resolves the gate's autonomy mode. Empty is auto, unless the gate carries a
// threshold, options or a deadline — a question shaped for somebody to answer — and then it
// is ask.
func (g Gate) mode() (task.Mode, error) {
	m := g.Mode
	if m == "" && g.asks() {
		m = "ask"
	}
	return ParseMode(m)
}

// asks reports whether the gate says anything about how its question settles, which only a
// posed question can honour.
func (g Gate) asks() bool {
	return g.Need > 0 || len(g.Options) > 0 || g.Deadline != nil
}

// options is what the gate's question offers: its own options when it names them, the two
// default ones with `proceed` at the threshold `need` asks for, or nothing — the default
// two, one ballot each — when it says neither.
func (g Gate) options() []task.Option {
	if len(g.Options) > 0 {
		return g.Options
	}
	if g.Need > 1 {
		return []task.Option{{ID: task.Proceed, Need: g.Need}, {ID: task.Deny, Need: 1}}
	}
	return nil
}

// occasion is what an action needs to know about the firing it is running for. It exists
// because two revisions live here that are easy to conflate, and conflating them is a real bug
// rather than a tidiness question:
//
//	parent — the causal PARENT, for the throttle: the commit this firing's writes descend from.
//	         It is deliberately 0 when the firing begins a fresh chain — a gate paced it, or the
//	         waking commit did not change this subject (see causeFor).
//	commit — the commit that CAUSED this firing. Never zeroed for pacing: a gated firing still
//	         happened because of one particular commit, and that commit is the state the rule
//	         was evaluated against, which is what $subject.value must be read at and what
//	         $subject.commit names.
//
// Before this they were one field and `parent` won, so a firing held at a gate reported no
// causing commit at all.
type occasion struct {
	// ev is the CHANGE, carried for a completion hook, whose references read the task's
	// payload rather than the subject's (see refSource). Zero for a subject-dispatched firing,
	// where the change is a doorbell and nothing reads it.
	ev entity.Event
	// commit is the causing commit, with its per-entity changes — so a subject that was itself
	// changed by it yields its payload at C for free, and only a fan-out member that did NOT
	// change costs a read at a revision.
	commit entity.Commit
	parent int64
	// snap is the firing's snapshot of the verse at that commit, taken at most once and shared
	// by everything in the action that needs one. A pointer, because an occasion is copied
	// down the action tree and the whole point is that the copies agree.
	//
	// One per FIRING rather than one per reference: Store.AsOf answers by replaying the log to
	// that commit, so an action with three path reads would otherwise pay three replays to learn
	// three things about one world — and, worse, could learn them about three different ones if
	// the replays were not pinned to the same revision. They are, so this is about cost; but
	// the cost is the kind that is discovered in production rather than in a test.
	snap *snapCache
	// reader is the store this firing reads through: the engine's, as the rule that is
	// firing, so what a reference resolves to is what the rule's owner may read. nil reads as
	// the engine itself.
	reader entity.Store
}

// reads is the store the occasion's reads go through.
func (oc occasion) reads(e *Engine) entity.Store {
	if oc.reader != nil {
		return oc.reader
	}
	return e.store
}

// snapCache is a snapshot taken lazily and at most once. A charter that never reads at a
// revision never takes one.
type snapCache struct {
	once sync.Once
	snap entity.Snapshot
	err  error
}

// snapshot returns the firing's view of the verse at the commit that caused it.
func (oc occasion) snapshot(e *Engine) (entity.Snapshot, error) {
	if oc.commit.Rev == 0 {
		return nil, fmt.Errorf("this firing has no causing commit to read at, so there is no " +
			"revision to be as of. A completion hook takes the one that caused the firing it " +
			"belongs to, off its task's record; a task nothing in the charter started has none")
	}
	if oc.snap == nil {
		return oc.reads(e).AsOf(oc.commit.Rev) // no cache wired: correct, just unshared
	}
	oc.snap.once.Do(func() { oc.snap.snap, oc.snap.err = oc.reads(e).AsOf(oc.commit.Rev) })
	return oc.snap.snap, oc.snap.err
}

// Action is what a trigger does when it fires

// Action is what a trigger does when it fires and its gates proceed — declarative data,
// not a Go closure: optionally set fields on an entity (a state transition) and/or spawn a
// task (a launch.Spec). The engine interprets it via apply.
//
// It has no target of its own: what an action acts on is the Condition's Subject. (This
// replaced a `setRef` field and a charter-set `spawn.subject`, which expressed the same
// thing twice, by side channel, and only existed because $subject was wired to the matched
// entity rather than to the subject.)
//
// Bindings available to templates: each field of the changed payload by name ($stage,
// $path, …), and $subject — the RESOLVED subject, set last so a payload field cannot
// shadow it. A Patch resolves the same names as a kpath deref, at any depth and keeping the
// value's type ($result, $verse.review.step, $result.findings[0].id) — see deref.go, which
// is what lets a task's output become a fact the next `when` can match rather than only
// text in the next prompt. A Spawn's strings keep the older flat substitution (launch.Bind).
//
//tony:schemagen=verse-action,notag,comment=Doc,lineComment=Note
type Action struct {
	// Doc and Note are the comments this was WRITTEN with — the lines above it, and the one
	// beside it. See Trigger.Doc: they ride the node rather than being fields of the document,
	// and they exist so a charter read back is the one its author wrote.
	Doc  []string `tony:"-"`
	Note []string `tony:"-"`
	// Patch is applied to the subject's payload with tony.Patch — the WRITE half of the
	// vocabulary whose read half is a Match. It merges rather than assigns, so a nested
	// write touches one subtree instead of replacing its parent, and it can carry the
	// mergeop operators (`!delete`, `!replace`, …) that a field overlay had no way to say.
	//
	// It replaced `set`, a shallow top-level overlay. The name went with the semantics:
	// this deep-merges, deletes and replaces, and "set" describes one of those. (Like the
	// read side's `value`, it is unrestricted in shape: `patch: 42` is a legitimate
	// whole-value replace, because a payload is any tony value.) See docs/trigger-model.md.
	// Spelled entity.Payload, which is what it IS. That was impossible until go-tony
	// v0.0.95: the resolver could not follow an alias ("Payload is not a named type"), so
	// every schemagen'd field had to say *ir.Node and the domain vocabulary was absent from
	// exactly the declarations a reader of the schema reads. Fixed, so the name is back.
	Patch entity.Payload `tony:"field=patch,omitzero"`

	// Derive writes an entity that is NOT the subject — the one thing a patch cannot do, and
	// the reason the level ordering exists. It is addressed <system>.<kind>, exactly two
	// levels, and everything below is payload; the derived entity's id is the engine's (a
	// function of rule and subject), as a spawn's is, so a re-fire is idempotent and no two
	// rules can ever address the same derived entity.
	//
	// Its content may reference the firing's SUBJECT and nothing else — every part of it,
	// including $subject.value, and refused at install for anything further (validateDerive,
	// via deriveMayReference). The line is not "no references": that was where this started
	// and it was too strict, since including something of the subject in another entity is
	// what a derive is FOR.
	//
	// What the ordering actually needs is content the levels above cannot influence. Nothing
	// above can write the subject — a patch touches only its own subject and a derive only
	// climbs — so a read from above that wakes the rule without changing the subject resolves
	// $subject.value to the same bytes, writes them to the same address, and dies as a no-op
	// the store ends by itself. That is what lets a rule read a HIGHER level without the read
	// becoming a cycle. $subject.commit is the one part refused, because it is a function of
	// WHEN the firing happened rather than of the subject, so a read from above does change
	// it. The worked cycle is on deriveMayReference. See stratum.go and docs/strata.md.
	Derive entity.Payload `tony:"field=derive,omitzero"`

	Spawn *launch.Spec `tony:"field=spawn,omitzero"`

	// Seq runs its elements IN ORDER, each starting when the previous one completes. It is
	// the only construct the model needed for sequencing, because parallelism already has
	// one: independent triggers matching the same condition. The two axes stay orthogonal in
	// the id space — breadth is distinct roots sharing a subject, depth is extension of one
	// chain — which is why there is no `par`. See docs/trigger-model.md.
	//
	// The sequence owns the DONE slot of each element: an element may carry onFailed /
	// onCancelled / onOrphaned as off-ramps (firing one ends the sequence), but not onDone,
	// because the list already says what comes next. This action's own onDone fires after the
	// WHOLE sequence, so a sequence is a unit with its own completion and nesting composes.
	// SeqDoc/SeqNote: the list is not a struct, so the comments about `seq:` need the field.
	// SeqDoc takes the line above `seq:` AND the one above its first element, which are one
	// comment (see Trigger.GatesDoc); each later element keeps its own on the Action it is.
	SeqDoc  []string `tony:"-"`
	SeqNote []string `tony:"-"`
	Seq     []Action `tony:"field=seq,omitzero,comment=SeqDoc,lineComment=SeqNote"`

	// Completion hooks: when this action's Spawn reaches a terminal status, the matching
	// hook fires as a full Action (set + spawn + its own onX) about the same subject — so a
	// chain (review → merge → approve) reads top-to-bottom in one trigger. The engine applies
	// it scoped to exactly this spawn's task (see Engine.applyHook / hookFor). onOrphaned is
	// inert until resume rehydrates hooks (see docs/trigger-model.md).
	OnDone      *Action `tony:"field=onDone,omitzero"`      // status done (success)
	OnFailed    *Action `tony:"field=onFailed,omitzero"`    // status failed
	OnCancelled *Action `tony:"field=onCancelled,omitzero"` // status cancelled
	OnOrphaned  *Action `tony:"field=onOrphaned,omitzero"`  // status orphaned
}

// hookFor returns the completion hook for a terminal status (nil if none). The four terminal
// statuses map to the four onX fields; non-terminal statuses have no hook.
func (a *Action) hookFor(s task.Status) *Action {
	switch s {
	case task.Done:
		return a.OnDone
	case task.Failed:
		return a.OnFailed
	case task.Cancelled:
		return a.OnCancelled
	case task.Orphaned:
		return a.OnOrphaned
	}
	return nil
}

// hookField is hookFor's other half: the NAME of the field a terminal status fires, which
// is the segment a chain kpath descends by. `review` + status done → `review.onDone`, which
// is the address of that hook inside the trigger — so a chain reads back as a lookup into
// the charter rather than as a private naming scheme. Empty for a non-terminal status.
func hookField(s task.Status) string {
	switch s {
	case task.Done:
		return "onDone"
	case task.Failed:
		return "onFailed"
	case task.Cancelled:
		return "onCancelled"
	case task.Orphaned:
		return "onOrphaned"
	}
	return ""
}

// hasHooks reports whether any completion hook is set — used to require a spawn for them.
func (a *Action) hasHooks() bool {
	return a.OnDone != nil || a.OnFailed != nil || a.OnCancelled != nil || a.OnOrphaned != nil
}

// does reports whether this action would do anything at all: write the subject, write another
// entity, or start work. Hooks are not among them — a hook is what happens WHEN something
// finishes, and there is nothing here to finish (validate refuses that separately).
func (a *Action) does() bool {
	return a.Patch != nil || a.Derive != nil || a.Spawn != nil || len(a.Seq) > 0
}

// validate reports whether the action is installable (called from AddTrigger). A completion
// hook needs a task to complete, so any onX requires a spawn; each hook is itself an action
// and is validated recursively.
func (a Action) validate() error {
	// The derive's own shape and its content rule. Checked here so it runs over every action in
	// the tree — validate recurses into a sequence's elements and a hook's action — while WHERE
	// a derive may land, and where a path read inside one may reach, are checked once per rule
	// against the strata (stratum.go).
	if err := a.validateDerive(); err != nil {
		return err
	}
	// A path read anywhere in this action, patch or derive: its shape, and the two segments the
	// level check reads. `!get-path` additionally refuses anything that could name several
	// things, which is what tells it apart from `!list-paths`.
	for _, p := range []entity.Payload{a.Patch, a.Derive} {
		if _, err := readTargets(p); err != nil {
			return err
		}
	}
	// A hook needs something that can COMPLETE. That used to mean a spawn; a sequence
	// completes too, when its last element does.
	if a.hasHooks() && a.Spawn == nil && len(a.Seq) == 0 {
		return fmt.Errorf("action.onX requires a spawn or a seq — a completion hook needs " +
			"something that finishes")
	}
	if len(a.Seq) > 0 && a.Spawn != nil {
		return fmt.Errorf("action has both `spawn` and `seq`: which one the sequence waits " +
			"for would be a coin flip. Put the spawn in the sequence as its first element")
	}
	// A governed selection needs a ceiling to select within. A provider entity is an ordinary
	// entity — anyone who can write one can create a doer — so a doer's `grant` is only safe
	// as a NARROWING of something the charter declared. An omitted grant is not a small
	// ceiling but an unbounded one (task.Launch.Grant nil = unbounded), which would make
	// "a provider may only narrow" a check against everything. Refuse at install, where the
	// charter's author is present to answer, rather than at fire time: `grant: all` is
	// spellable precisely so a charter can SAY it means unbounded instead of omitting it.
	if a.Spawn != nil && a.Spawn.Provider != nil && a.Spawn.Grant == "" {
		return fmt.Errorf("spawn declares `provider:` but no `grant:`: a selected doer may " +
			"carry its own grant, and that is only checkable against a ceiling the charter " +
			"states. Add one (`grant: read-only`, `no-delegate`, `none`) — or `grant: all` to " +
			"say the selection is deliberately unbounded")
	}
	for i, el := range a.Seq {
		// The sequence owns each element's done slot — the list already says what comes
		// next, so a second answer in the same place is a contradiction, not a composition.
		if el.OnDone != nil {
			return fmt.Errorf("seq[%d] carries onDone: a sequence already says what follows "+
				"an element. Use the next element, or this action's own onDone to run after "+
				"the WHOLE sequence", i)
		}
		if err := el.validate(); err != nil {
			return fmt.Errorf("seq[%d]: %w", i, err)
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h == nil {
			continue
		}
		if err := h.validate(); err != nil {
			return err
		}
	}
	return nil
}

// noNestedSeq rejects a second `seq` anywhere below one. Stated over CHAINS rather than over
// elements — "no chain may contain two seq segments" — because an element's onFailed holding
// a sequence nests just as much as an element holding one directly, only further away.
//
// It is what keeps re-fire idempotence a PREDICATE rather than a search: with at most one seq
// per chain, "the sequence this live task belongs to" is a unique ancestor rather than a
// stack of them (docs/trigger-model.md).
func (a Action) noNestedSeq(inSeq bool) error {
	if len(a.Seq) > 0 {
		if inSeq {
			return fmt.Errorf("a sequence inside a sequence: no chain may carry two `seq` " +
				"segments, because then which sequence a running task belongs to has more " +
				"than one answer")
		}
		inSeq = true
	}
	for _, el := range a.Seq {
		if err := el.noNestedSeq(inSeq); err != nil {
			return err
		}
	}
	for _, h := range []*Action{a.OnDone, a.OnFailed, a.OnCancelled, a.OnOrphaned} {
		if h == nil {
			continue
		}
		if err := h.noNestedSeq(inSeq); err != nil {
			return err
		}
	}
	return nil
}

// apply runs the action once per resolved subject: apply the Set (a state transition on
// that subject) then spawn (if any). chain is this action's kpath inside its trigger (the
// trigger name at the top, `review.onDone` for a hook), used to derive the spawned task
// id. Writing entities advances state — which wakes the next trigger, closing the loop.
// It returns the first subject's failure, if any. A fan-out still attempts every subject —
// one member's bad reference must not stop the others — but the error is reported rather
// than only traced, so a caller that has somewhere to record it (a completion hook, which
// knows the task it belongs to) can persist it instead of dropping it into the log.
func (a Action) apply(chain string, oc occasion, subjects []entity.Ref, e *Engine, src refSource) error {
	var first error
	for _, subj := range subjects {
		h, err := a.applyOne(chain, oc, subj, e, src)
		if err != nil {
			if first == nil {
				first = err
			}
			continue // already traced; this subject does not advance
		}
		// A sequence starts once the carrier's own write has landed. It cannot also have
		// spawned (validate rejects spawn+seq), so there is nothing in flight to race.
		if len(a.Seq) > 0 && h == nil {
			e.startSeq(chain, a, oc, subj, src)
		}
	}
	return first
}

// applyOne is apply for exactly one subject: the write, then the spawn. It returns the
// spawned task (nil when the action starts none) and the write's error, which is what lets
// a sequence tell "this element is running" from "this element is already finished" — the
// distinction advanceSeq turns on.
func (a Action) applyOne(chain string, oc occasion, subj entity.Ref, e *Engine, src refSource) (task.Handle, error) {
	// What $references read: the cause's payload for a hook, the subject's own otherwise.
	// Resolved once, so a patch and a spawn in one action cannot disagree about what $x means.
	root := e.refPayload(oc.ev, subj, src)
	if a.Patch != nil {
		// Resolve $references against the payload that fired this action, so a patch can
		// write a value it read (a completed task's $result, a nested $verse.review.done)
		// rather than only a constant. An unresolved reference fails the firing for the same
		// reason a failed write does — and specifically must not be dropped: the store
		// distinguishes an absent key from a null one, so omitting it would launder "the
		// task never said" into "the task said nothing", which a match reads as a
		// different fact.
		sn, err := e.subjectFor(oc, subj, wantsOf(a.Patch))
		if err != nil {
			e.tracef("trigger %s: %v; not spawning", chain, err)
			return nil, err
		}
		patch, err := expandPatch(a.Patch, refRoot(root, sn), e.readerFor(oc))
		if err != nil {
			e.tracef("trigger %s: patch on %s could not be resolved (%v); not spawning", chain, subj, err)
			return nil, err
		}
		// ev.Rev is the commit that woke this firing, and carrying it into the write is what
		// makes the resulting commit a HOP rather than a fresh cause. It is 0 for a firing with
		// no engine-caused cause — a completion hook, whose event comes off a task finishing
		// (taskEvent leaves Rev unset) — and 0 is exactly right there: the world came back, so
		// the chain starts again. See causal.go.
		if err := e.patchEntity(chain, oc.parent, subj, patch); err != nil {
			// Fail the firing rather than continuing into the spawn: a task started about
			// a state the trigger FAILED to establish is worse than no task at all, and it
			// would be the harder failure to read afterwards.
			e.tracef("trigger %s: patch on %s failed (%v); not spawning", chain, subj, err)
			return nil, err
		}
	}
	// DERIVATIONS, after the subject's own write and before the spawn — a derive is state, and
	// a task started about a state the trigger failed to establish is the same failure the
	// patch above already refuses to create.
	//
	// The only reference a derive may carry is $subject, checked at install (validateDerive) and
	// resolved against a root holding nothing else. What the engine adds beyond that is the
	// linkage — which subject this derivation is about — on the precedent the spawn below sets
	// for its own subject.
	for _, d := range a.derives() {
		// A FAN-OUT: one derived entity per element of `over`, addressed by `key`.
		//
		// `over` resolves against the payload the action reads — the finished task's in a hook,
		// which is where an answer with twelve findings arrives — while each element's `value`
		// resolves against the subject and `$item` alone. Two roots, because they are two
		// different questions: what to walk, and what one element writes.
		if d.Each != nil {
			// The subject parts `over` and `key` reach for: `over: $subject.value.findings` is
			// the trigger-side spelling and it costs a read at the causing commit, which is
			// exactly what wantsOf exists to ask for rather than always paying.
			sn, err := e.subjectFor(oc, subj, wantsOf(d.Each.Over).or(wantsOf(d.Each.Key)))
			if err != nil {
				e.tracef("trigger %s: %v; not spawning", chain, err)
				return nil, err
			}
			elems, err := e.eachElements(d.Each, refRoot(root, sn))
			if err != nil {
				e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, d.addr(), err)
				return nil, err
			}
			for i, item := range elems {
				key, err := eachKey(d.Each, eachRootFor(sn, item), i)
				if err != nil {
					e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, d.addr(), err)
					return nil, err
				}
				segs := eachSegs(chainID(chain), subj, key)
				if d.Each.ID != nil {
					segs = namedSegs(key) // author-named: the place plus the value, see deriveTarget.ID
				}
				ref := d.refFor(segs...)
				payload, err := e.eachContent(d.Each, chain, oc, subj, item)
				if err != nil {
					e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, ref, err)
					return nil, err
				}
				if err := e.patchEntity(chain, oc.parent, ref, payload); err != nil {
					e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, ref, err)
					return nil, err
				}
			}
			continue
		}
		segs := deriveSegs(chainID(chain), subj)
		if d.Key != nil {
			// A KEYED derive: one entity per value, addressed the way a fan-out's elements are.
			// The key is read from the same root the content is, so `key: $subject.value.sha`
			// says "one of these per tip".
			sn, err := e.subjectFor(oc, subj, wantsOf(d.Key))
			if err != nil {
				e.tracef("trigger %s: %v; not spawning", chain, err)
				return nil, err
			}
			key, err := keyText(d.Key, refRoot(root, sn))
			if err != nil {
				e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, d.addr(), err)
				return nil, err
			}
			segs = eachSegs(chainID(chain), subj, key)
		}
		if d.ID != nil {
			// AN AUTHOR-NAMED derive: the same key, addressed by itself. See deriveTarget.ID
			// for what the composition bought and who pays for it now.
			sn, err := e.subjectFor(oc, subj, wantsOf(d.ID))
			if err != nil {
				e.tracef("trigger %s: %v; not spawning", chain, err)
				return nil, err
			}
			key, err := keyText(d.ID, refRoot(root, sn))
			if err != nil {
				e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, d.addr(), err)
				return nil, err
			}
			segs = namedSegs(key)
		}
		ref := d.refFor(segs...)
		payload, err := e.deriveContent(d.Payload, chain, oc, subj)
		if err != nil {
			e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, ref, err)
			return nil, err
		}
		// ev.Rev as the parent, exactly as the subject patch above: the derived entity is what
		// wakes the higher stratum's rule, so this is where a cascade's causal chain crosses a
		// level. Losing it would not loosen the throttle's bound — a chain never returns to a
		// stratum it has left, so a per-segment count still trips — but it would truncate the
		// report, which names the rules that looped by walking back parents (causal.go).
		if err := e.patchEntity(chain, oc.parent, ref, payload); err != nil {
			e.tracef("trigger %s: derive into %s failed (%v); not spawning", chain, ref, err)
			return nil, err
		}
	}
	if a.Spawn == nil {
		return nil, nil
	}
	return e.launchSpawn(a, chain, subj, root, oc.commit.Rev)
}

// launchSpawn is the spawn half of an action: bind the spec, resolve the who-axis, and start
// the task under its deterministic id.
//
// It is its own function rather than the tail of applyOne because a who-motion outlives the
// firing that opened it: ResumeSelections re-enters HERE after a restart, and a second copy of
// these lines is how a resumed selection would come to launch something subtly different from
// the run it is finishing.
//
// root is what $references read (the cause's payload for a hook, the subject's own
// otherwise); occurrence is the store revision this run is about, 0 when nothing caused it.
func (e *Engine) launchSpawn(a Action, chain string, subj entity.Ref, root entity.Payload,
	occurrence int64) (task.Handle, error) {
	spec := a.Spawn.Bind(bindingFor(root, subj))
	// The subject is the engine's to set, not the charter's: a task is about exactly
	// one subject, and which one is the Condition's answer.
	spec.Subject = subj.String()
	// The chain rides the spec so the SPAWNER can mirror it: mirror.Reflect owns the task
	// entity's payload and re-Puts it on every event, so a field the engine wrote afterwards
	// would be clobbered. Engine-owned, like Subject above.
	//
	// Set BEFORE the selection below, not after: a who-motion is opened by that selection and
	// outlives the firing, so the chain is what it records to be resumable by — the same
	// lookup key a resumed task's hooks are rehydrated from (AdoptChain).
	spec.Chain = chain
	// And WHICH RUN this is. The id names the work — chain plus subject, so a re-fire is
	// idempotent — and nothing named the occurrence, so a second run overwrote the first
	// record: staging held one that read `ended` fifty minutes before its `started`. The
	// causing commit is in hand here and is what an operator is holding when they ask which
	// run they are looking at. Engine-owned, and it rides to the record only — a DERIVE that
	// referenced its cause is the cycle deriveMayReference exists to refuse.
	spec.Occurrence = occurrence
	// who-axis: if the spawn declares a governed provider selection, resolve it and overlay
	// what the chosen doer contributes — driver, frame, tools, grant — before spawn. No live
	// swap (reassign = cancel+respawn). Orthogonal to the runner that then transports Driver.
	//
	// The spawn is the base and the winner is the overlay, so the charter's spawn block is
	// what a doer varies FROM rather than a set of fields one doer must satisfy: that is what
	// lets one trigger route to a fixer and a memo-writer that differ in everything but the
	// driver. Narrowing only — see launch.Spec.Overlay.
	if spec.Provider != nil {
		frag, ok := e.selectProvider(spec, subj)
		if !ok {
			return nil, nil // no eligible doer / selection did not resolve — do not spawn
		}
		overlaid, err := spec.Overlay(frag)
		if err != nil {
			// The doer and the spawn agree on no tool. Failing the firing is the only safe
			// answer, because the alternative spelling of "no tools" is an empty list, which
			// means ALL of them — see launch.Spec.Overlay.
			e.tracef("trigger %s: selected doer cannot be launched under this spawn (%v); not spawning", chain, err)
			return nil, err
		}
		spec = overlaid
	}
	// Keyed by the SUBJECT, not the matched entity, so a re-fire about the same subject is
	// idempotent (and, under fan-out, each member gets its own deterministic id). The action
	// carries its own completion hooks (onX): they ride the spawned task's flowTask and
	// fire when it reaches a terminal status. chain is this action's kpath, so a hook's own
	// spawn — or a sequence's next element — gets a distinct, deterministic id.
	hooks := a
	return e.spawnHooked(spawnID(e.idOwner(), chainID(chain), subj), spec, &hooks, chain), nil
}
