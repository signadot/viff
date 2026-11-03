package trigger

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	tonyeval "github.com/signadot/tony-format/go-tony/eval"
	"github.com/signadot/tony-format/go-tony/gomap"
	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/tony-format/go-tony/ir/kpath"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger/charter"
)

// AddTrigger installs a rule into the charter, refusing a name in use: one rule at a time,
// where a name already taken is a collision somebody has not noticed. InstallTrigger replaces.
func (e *Engine) AddTrigger(t Trigger) error {
	e.trigMu.Lock()
	err := e.nameTaken(t.Name)
	e.trigMu.Unlock()
	if err != nil {
		return err
	}
	return e.InstallTriggerAs(context.Background(), "", t)
}

// InstallTrigger is AddTrigger for a CHARTER FILE: a rule of the same name is REPLACED rather
// than refused, because the file is what the operator means and re-applying one is the ordinary
// way to change a rule. Installed or held, both: a rule waiting for its stratum is still a rule
// of this name.
func (e *Engine) InstallTrigger(t Trigger) error {
	return e.InstallTriggerAs(context.Background(), "", t)
}

// InstallTriggerAs is InstallTrigger on behalf of a principal: the rule's record is written as
// theirs, so the charter acts as its installer from then on. The empty principal is the
// store's own — the system — and is what InstallTrigger passes. It returns once the engine
// holds the rule (followed), bounded by ctx.
//
// THE RECORD IS THE RULE (follow.go): the install is the write, and what the engine runs
// follows from it. Every check a rule faces is made first, with the old one still standing: a
// file re-applied with a mistake in it is refused and leaves what stood standing.
func (e *Engine) InstallTriggerAs(ctx context.Context, as string, t Trigger) error {
	// REPLACING A RULE IS ITS OWNER'S ACT, asked before anything is touched: the record is
	// written through the engine's own store, which is the system's, so a check made only by
	// the write would pass for anybody.
	if err := e.MayRemoveTrigger(as, t.Name); err != nil {
		return err
	}
	hold, err := e.vet(t, as, RunnersChecked)
	if err != nil {
		return err
	}
	_, had := e.Rule(t.Name)
	// WRITTEN OVER, NOT DELETED AND WRITTEN. A rule's spec is stored raw, so a write replaces
	// it whole, and deleting the record first would take its owner record with it — the grants
	// on it, and whose the rule is: the replacement would be recorded as whoever replaced it
	// and fire as them. A rule stays its installer's through an edit by a co-owner; only the
	// system, installing at a name it decides, takes it (takeForTheSystem).
	rev, err := e.persist(t, as, true)
	if err != nil {
		if rev > 0 && !had {
			// Written and not taken: a record standing under a name nobody placed is worse
			// than none, where the name was free.
			if _, back := e.forget(t.Name); back != nil {
				e.logger.Error("the record of a refused install could not be taken back", "trigger", t.Name, "err", back)
			}
		}
		return err
	}
	if err := e.followed(ctx, rev, func() { e.admit(t, hold) }); err != nil {
		return err
	}
	e.reworded(t)
	// The owner record lands after the rule's, so the follow may have vetted the rule as whose
	// it was, or held it for the record (asOwner): a pass vets it again as whose it is.
	e.kick()
	e.passed(ctx, t.Name)
	return nil
}

// passed waits for the pass a placed rule waits on to answer for it: armed, or held for what a
// pass does not clear. A pass is the vet and the asks for the rule's watches, answered at once
// — the store opens them behind the arm — so an install's answer says what the rule waits for,
// and not that it waits for the pass.
func (e *Engine) passed(ctx context.Context, name string) {
	e.trigMu.Lock()
	var settled chan struct{}
	if i := e.pendingIndex(name); i >= 0 && e.pending[i].cause.passing() {
		settled = e.pending[i].settled
	}
	e.trigMu.Unlock()
	if settled == nil {
		return
	}
	select {
	case <-settled:
	case <-ctx.Done():
	case <-time.After(followWait):
	}
}

// reworded swaps a rule's words in where the follow could not: a watch reports what differs
// in value, and a rule written over with the same value and other words — the comments its
// author left above it moved — is a commit it says nothing of. The record holds the words, a
// restart reads them, and the engine's copy follows here.
func (e *Engine) reworded(t Trigger) {
	if cur, ok := e.Rule(t.Name); ok && SameValue(cur, t) && !SameText(cur, t) {
		e.reword(t)
	}
}

// addTrigger puts a rule into the engine with nothing written, vetted with runners as the
// caller says and refused as that vet refuses: for a test. The record is the rule everywhere
// else.
func (e *Engine) addTrigger(t Trigger, as string, runners Runners) error {
	hold, err := e.vet(t, as, runners)
	if err != nil {
		return err
	}
	e.admit(t, hold)
	return nil
}

// RestoreTrigger puts a rule back as it stood — the undo of a replacement, for a transaction
// that is backing out. It is the engine's own act and is asked of nobody: it writes the old
// content over what stands and CLAIMS NOTHING, so whose the rule is does not move — restored as
// an install would be, a rule put back by the engine would be taken for the system.
func (e *Engine) RestoreTrigger(ctx context.Context, old Trigger) error {
	rev, err := e.persist(old, "", false)
	if err != nil {
		return err
	}
	if err := e.followed(ctx, rev, func() { e.place(old) }); err != nil {
		return err
	}
	e.reworded(old)
	return nil
}

// MayRemoveTrigger reports whether principal may remove the rule of this name, installed or
// held: they hold `owner` on its record, since removing a node is its owner's act and what goes
// with this one is whatever fires through it. nil where no such rule stands, and for the empty
// principal and the system, which are the engine's own callers (a restore, an undo).
//
// The engine asks because the engine is what removes: its store acts as the system, so a caller
// that did not ask first has had the system delete somebody's rule on a stranger's say-so.
func (e *Engine) MayRemoveTrigger(principal, name string) error {
	if principal == "" || entity.IsSystem(principal) {
		return nil
	}
	if _, ok := e.Rule(name); !ok {
		return nil
	}
	return entity.Check(e.store, principal, entity.OwnerLevel, charter.RuleRef(e.confine.stamp(), name), "removing it")
}

// RemoveTrigger removes a rule by name — armed or pending, since either occupies the name — and
// reports whether it stood. The removal is the delete of its record, which is what the next
// start reads, and the engine follows: the rule's watches stop with it, or a removed rule keeps
// firing off a watch nothing lists any more.
func (e *Engine) RemoveTrigger(name string) bool {
	stood, err := e.RemoveTriggerAs(context.Background(), "", name)
	if err != nil {
		e.logger.Warn("a rule's removal did not land", "trigger", name, "err", err)
	}
	return stood
}

// RemoveTriggerAs is RemoveTrigger on behalf of a principal, refused where they may not, and
// answered once the engine no longer holds the rule (followed), bounded by ctx.
func (e *Engine) RemoveTriggerAs(ctx context.Context, principal, name string) (bool, error) {
	if err := e.MayRemoveTrigger(principal, name); err != nil {
		return false, err
	}
	_, stood := e.Rule(name)
	rev, err := e.forget(name)
	if err != nil {
		return false, err
	}
	return stood, e.followed(ctx, rev, func() { e.unplace(name) })
}

// vet is every check an install makes of a rule, and changes nothing. A rule that fails one
// is REFUSED, with the error; one that passes them all is admitted, with no hold; and one whose
// checks cannot all be made yet is HELD, with the cause that can clear (reconcile.go) — its
// stratum not installed, a read that did not happen, and, where runners says the runners are
// still to come, no runner offering its spawn. A refusal outranks a hold: a rule wrong in some
// way that waiting will not cure is told about THAT.
func (e *Engine) vet(t Trigger, as string, runners Runners) (*cause, error) {
	var hold *cause
	holdFor := func(c cause) {
		if hold == nil {
			hold = &c
		}
	}
	if err := validTriggerName(t.Name); err != nil {
		return nil, err
	}
	// A RULE THAT COULD NEVER MATCH IS REFUSED, not installed silent. The perms mirror is the
	// system's, with no read held out on it, so a rule reading it matches nothing unless it is
	// the system's own — and the one that should read it is exactly that one: a system charter
	// watching policy change. Anybody else's is told so here rather than left to wonder.
	if entity.PolicyOf(e.store) != nil {
		for _, st := range t.reads() {
			if p := st.Path(); len(p) >= 2 && p[0] == entity.VerseRoot && p[1] == entity.PermsSystem {
				ok, err := entity.Holds(e.store, as, entity.Read, entity.VerseRef(entity.PermsSystem))
				if err != nil {
					holdFor(cause{causeRead, err.Error()})
				} else if !ok {
					return nil, fmt.Errorf("trigger %q reads %s, which is the perms mirror: it is the "+
						"system's and held out to nobody, so a rule of anyone else's reading it "+
						"would never match. Policy is watched by the system's own charter", t.Name,
						entity.JoinSegs(p))
				}
			}
		}
	}
	if err := e.checkAnswerers(t); err != nil {
		if !entity.IsUnread(err) {
			return nil, err
		}
		holdFor(cause{causeRead, err.Error()})
	}
	if err := t.Condition.validate(); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	if err := t.Action.validate(); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// AGAINST WHAT THE RUNNERS OFFER, where the author is present: a spawn a runner offers and
	// none takes — a model none takes, repos on a forge none has, an environment over every
	// ceiling — is refused here, with the offers named, rather than failing at its first firing.
	// A spawn NO RUNNER OFFERS (ErrNoOffer) is refused the same, unless the caller says the
	// runner may be still to come (RunnersToCome), as the engine's placing of a record does: then
	// it is held for one (git-issue xf2pp5j0h12krh1ywxn0).
	if e.offerCheck != nil {
		// Every spawn is asked: a refusal of a later one outranks a hold for an earlier one.
		var refused, unoffered error
		t.Action.eachSpawn(t.Name, func(chain string, spec *launch.Spec) {
			if refused != nil {
				return
			}
			l, known := installLaunch(*spec)
			if !known {
				return // the doer is picked or bound at the firing, and routed then
			}
			// Held to the runner's ceilings on its environment AS WRITTEN: a `$` attribute is
			// within where some binding of it could be (runnerd.CheckOfferAsWritten), which the
			// stand-ins installLaunch writes for it, being no binding, never are. The launch is
			// held to them again, bound.
			l.Environment = spec.Environment
			switch err := e.offerCheck(l); {
			case err == nil:
			case errors.Is(err, ErrNoOffer) && runners == RunnersToCome:
				if unoffered == nil {
					unoffered = fmt.Errorf("trigger %q: spawn: %w", chain, err)
				}
			default:
				refused = fmt.Errorf("trigger %q: spawn: %w", chain, err)
			}
		})
		if refused != nil {
			return nil, refused
		}
		if unoffered != nil {
			holdFor(cause{causeRunner, unoffered.Error()})
		}
	}
	// A RULE THAT DOES NOTHING IS REFUSED. It installed, reported "installed 1 trigger(s)",
	// rendered back with `action: {}`, and then watched its subtree for the life of the verse
	// without ever writing anything (git-issue b2g9hsnfh12ks5vqg9n0).
	//
	// It is the failure every refusal around it exists to prevent — a rule that looks installed
	// and is not — and a worse instance than the ones already caught, because there is no
	// half-written field to notice: the whole action is missing. A gate does not rescue it
	// either. A gate with no action poses a question whose answer nothing reads.
	if !t.Action.does() {
		return nil, fmt.Errorf("trigger %q: the action does nothing — it has no patch, derive, spawn "+
			"or seq, so the rule watches its subtree and writes nothing whatever it sees. Give "+
			"it an action (a `patch` is one line), or remove the rule", t.Name)
	}
	if err := t.Action.noNestedSeq(false); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// A CREDENTIAL SAYS WHICH GATE LET THE WORK THROUGH, so it is asked for where there is one to
	// name: the rule's own spawn, of a rule that writes a gate. A gate written `auto` counts, and
	// a rule with none does not: work that reaches another system unattended does so because its
	// charter says so, where a reviewer reads it. Refused here, where the author is present, and
	// not at the first firing, which would start a task only to fail it.
	if where := t.Action.credentialBeneath(""); where != "" {
		return nil, fmt.Errorf("trigger %q: %s asks for a credential, and a credential goes to the "+
			"spawn a gate let through, which is the rule's own: a hook or a sequence step starts "+
			"after the answer was given. Make the promotion a rule of its own, with its own gate", t.Name, where)
	}
	if t.Action.Spawn != nil && t.Action.Spawn.Credential != nil && len(t.AllGates()) == 0 {
		return nil, fmt.Errorf("trigger %q: the spawn asks for a credential, and the rule has no "+
			"gate: a credential says which gate let the work through, so the rule writes one. "+
			"`gates: [{name: …, mode: ask}]` has somebody answer; `mode: auto` asks nobody, and the "+
			"credential says that too", t.Name)
	}
	// One spelling or the other, never both. `gates:` at the top level means
	// `gating: {gates: …}`; a rule that writes both has said which gates it has twice, and
	// picking one silently is how a charter comes to gate on something its author removed.
	if t.Gating != nil && len(t.Gating.Gates) > 0 && len(t.Gates) > 0 {
		return nil, fmt.Errorf("trigger %q: gates are written twice — at the top level and under "+
			"`gating`. They are one field read two ways; keep one of them", t.Name)
	}
	for _, g := range t.AllGates() {
		if err := g.validate(); err != nil {
			return nil, fmt.Errorf("trigger %q: gate %q: %w", t.Name, g.Name, err)
		}
		if _, err := g.mode(); err != nil {
			return nil, err
		}
		// An onDenied is an ordinary action and faces every check one faces — including
		// that ITS own completion hooks need a spawn. What it does not need is a spawn of
		// its own: it is not a completion hook, it is what a refusal means.
		if g.OnDenied != nil {
			if err := g.OnDenied.validate(); err != nil {
				return nil, fmt.Errorf("trigger %q: gate %q onDenied: %w", t.Name, g.Name, err)
			}
		}
	}
	// LEVEL DISCIPLINE: a subject inside its stratum's pattern, a derive inside some strictly
	// higher one's. Syntactic like the rest, and neither reads the store.
	//
	// Before the latch check for the reason that check gives for being last — a charter wrong in
	// some other way should be told about THAT — and a rule acting outside the level it declared
	// is another way, and a more specific one.
	//
	// VERSE'S OWN HALF FIRST, which no rule but the system's writes (checkReserved): it is the
	// more specific answer, and a rule told only that its derive needs a level would be given
	// one and told this next.
	if err := e.checkReserved(t, as); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// The verdict is held rather than acted on, because a rule whose stratum is not installed yet
	// is not refused (see below) and must still face every check that does not depend on the
	// ordering.
	ok, levelErr := e.checkLevels(t)
	if levelErr != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, levelErr)
	}
	if !ok {
		holdFor(cause{causeStratum, t.Stratum})
	}
	// The confinement, before the ordering below: a rule that may not write where it acts is
	// refused whatever its levels say, and "outside this charter's address" is the more
	// specific answer of the two.
	if err := e.checkConfined(t); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// And what it READS, against what the package declares. After the write check because the
	// two answer different questions and the write one is the sharper failure: a rule reaching
	// past its address to WRITE is a mistake, and one reaching past it to read is a dependency
	// somebody has not written down. See reads.go.
	if err := e.checkReads(t); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	// LAST, and fatal. A charter that cannot stop spawning is refused rather than logged: it
	// is not a style question but a rule with no end state, and the operator installing it is
	// the one person positioned to fix it.
	//
	// Last so that a charter which is wrong in some other way is told about THAT. A nested
	// sequence also fails this check, and "your action re-spawns" would be a true answer to
	// the wrong question.
	if err := checkLatch(t.Condition, t.Action); err != nil {
		return nil, fmt.Errorf("trigger %q: %w", t.Name, err)
	}
	return hold, nil
}

// admit puts a vetted rule into the engine, in the stead of whatever stood under its name:
// armed where nothing holds it and nothing is to be opened for it (baseline mode), pending
// otherwise — for the pass it kicks, or with the cause vet held it for (reconcile.go).
//
// ONE STEP: what stood goes out and the rule comes in under one hold of the lock, so nothing
// that reads the charter between finds the name empty — a wait on the rule (Ready) follows a
// replacement through.
func (e *Engine) admit(t Trigger, hold *cause) {
	if hold == nil {
		// Advisory checks: a charter can mean what it says and still not mean what its author
		// intended. These name it and install it anyway. And the ones that need the REST of the
		// charter to see — a derive of one rule over a field another rule owns is not visible
		// in either rule alone. A held rule is advised as it arms.
		for _, w := range append(advise(t), e.adviseDeriveClobber(t)...) {
			e.logger.Warn("charter", "trigger", t.Name, "note", w)
		}
	}
	e.trigMu.Lock()
	e.unplaceLocked(t.Name)
	switch {
	case hold != nil:
		e.hold(t, *hold)
	case e.subtree:
		e.hold(t, cause{causePass, ""})
	default:
		e.triggers = append(e.triggers, t)
	}
	e.trigMu.Unlock()
	// The shapes of its sequences are a constant of the charter, recorded for every rule placed
	// — a held one arms later, by the reconciler, and fires with them — and forgotten with it.
	e.recordShapes(t)
	switch {
	case hold == nil:
		if e.subtree {
			e.kick()
		}
	case hold.kind == causeRefused:
		e.logger.Warn("rule refused: it is written down, and fires on nothing until it passes as the world stands",
			"trigger", t.Name, "err", hold.why)
	case hold.passing(): // the pass its install kicks arms it: no news
	default:
		e.logger.Warn("rule held: it is written down, and fires on nothing until what it waits for is here",
			"trigger", t.Name, "waits", hold.String())
	}
}

// unplace takes a rule out of the ENGINE — armed or pending — and leaves what is written down.
func (e *Engine) unplace(name string) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	return e.unplaceLocked(name)
}

// unplaceLocked is unplace with trigMu HELD. It STOPS THE RULE'S OBSERVATION, which in subtree
// mode is a set of live goroutines: each rule holds its own watches, so a removal that only
// edited the charter would leave them running and the removed rule firing — invisibly, since
// nothing lists it any more. A PENDING rule goes by the same name: it would arm the moment its
// cause cleared.
func (e *Engine) unplaceLocked(name string) bool {
	named := func(t Trigger) bool { return t.Name == name }
	armed := slices.ContainsFunc(e.triggers, named)
	e.triggers = slices.DeleteFunc(slices.Clone(e.triggers), named)
	if armed {
		e.stopWatchers(name)
		e.forgetTrouble(name)
	}
	pending := false
	for i := range e.pending {
		if named(e.pending[i].Trigger) {
			pending = true
			e.settle(i)
		}
	}
	e.pending = slices.DeleteFunc(slices.Clone(e.pending), func(h held) bool { return named(h.Trigger) })
	if armed || pending {
		e.forgetShapes(name)
	}
	return armed || pending
}

// Trigger returns one installed trigger by name.
func (e *Engine) Trigger(name string) (Trigger, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, t := range e.triggers {
		if t.Name == name {
			return t, true
		}
	}
	return Trigger{}, false
}

// Rule returns one rule of the charter by name, ARMED OR PENDING.
//
// Trigger answers only the first, which is the question a firing asks. This is the question an
// INSTALLER asks — what stands under this name, so that an install which has to be undone can put
// it back — and there "it is not running" and "it is not there" are different answers: a pending
// rule is written down, occupies its name, and arms the moment its cause clears.
func (e *Engine) Rule(name string) (Trigger, bool) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, set := range [][]Trigger{e.triggers, e.pendingTriggers()} {
		for _, t := range set {
			if t.Name == name {
				return t, true
			}
		}
	}
	return Trigger{}, false
}

// Notes is what an install has to say to whoever made it, beside whether it happened.
//
// NEITHER IS A REFUSAL, and both change what the author does next. A pending rule is installed
// in every sense but the one that matters to somebody watching for it to fire; an advisory is a
// rule that is legal and reads as something other than what it does. The engine's log has both,
// and the log is read by whoever runs the daemon — the author is whoever sent the file, and
// what they are told is the install's answer.
type Notes struct {
	// Waits is what the rule waits for: what a pending rule says (Pending.Cause), or for an
	// armed rule whose watches are not all open yet, what the store is doing for them, with
	// Arming set; "" for an armed rule watching.
	Waits  string
	Arming bool
	// Advice is what the rule says that its author may not have meant (advise).
	Advice []string
}

// NotesOn is what the install of t, just made, has to say.
func (e *Engine) NotesOn(t Trigger) Notes {
	var n Notes
	e.trigMu.Lock()
	if i := e.pendingIndex(t.Name); i >= 0 {
		n.Waits = e.pending[i].cause.String()
	} else if ok, doing := e.watchingLocked(t.Name); !ok {
		n.Waits, n.Arming = doing, true
	}
	e.trigMu.Unlock()
	if n.Waits != "" && !n.Arming {
		// A held rule is advised when it arms: what it crosses is not known until then.
		return n
	}
	n.Advice = append(advise(t), e.adviseDeriveClobber(t)...)
	return n
}

// Triggers returns the installed triggers (the charter).
func (e *Engine) Triggers() []Trigger {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := make([]Trigger, len(e.triggers))
	copy(out, e.triggers)
	return out
}

// ChainSep bounds a task id's CHAIN from its SUBJECT: `review.onDone@demo.pr.1` is
// "the onDone hook of trigger review, about demo.pr.1". It is '@' because the chain is a
// kpath and so is the address, so the boundary needs a character neither grammar spends —
// and because '@' survives a URL path segment and a shell word intact.
//
// A chain cannot contain '@' (validTriggerName rejects it, and every descent appends a
// Go field name), so the FIRST '@' is always the boundary, whatever the address carries.
const ChainSep = "@"

// AtSep and ScopeSep bound WHICH ENGINE minted an id from the chain it was minted for, in every
// id a firing mints — its task, its gate question. A rule is (scope,
// charter address, name), and names are unique only within an engine, while every engine of a
// verse shares one runtime and one question registry: without the two, baseline's `review` and
// a package's `review` about one subject would spawn under one task id and wait on one question.
//
//	review.onDone@demo.pr.1                 baseline's rule
//	verse-dev.drift:claim@git.verse.head    a package's, at its address
//	me::review@demo.pr.1                    scope me's
//
// Each is absent when empty, so baseline's ids read as they always have. Neither character can
// stand in a chain (validTriggerName refuses ':' and a descent appends a Go field name), and the
// scope and the address are rendered as tokens with none of ':', '@' or '~' (ownerToken), so the
// first "::" bounds the scope, the next ':' the address, and the first '@' the chain: the
// composition is injective, and the spellings `verse flow --charter` reads share it.
const (
	AtSep    = ":"
	ScopeSep = "::"
)

// ownerOf is the prefix naming the engine an id is minted in: the scope's token and ScopeSep,
// then the charter address's token and AtSep, each only when there is one.
func ownerOf(scope string, at []string) string {
	var b strings.Builder
	if scope != "" {
		b.WriteString(ownerToken([]string{scope}))
		b.WriteString(ScopeSep)
	}
	if len(at) > 0 {
		b.WriteString(ownerToken(at))
		b.WriteString(AtSep)
	}
	return b.String()
}

// idOwner is ownerOf for this engine.
func (e *Engine) idOwner() string {
	var at []string
	if e.confine.confined() {
		at = e.confine.Path()
	}
	return ownerOf(e.scope, at)
}

// ownerToken is entity.SegToken with ':' and '@' rewritten too, since both bound an id's parts
// here; the hash over the original segments keeps a rewrite from merging two names.
func ownerToken(segs []string) string {
	safe := make([]string, len(segs))
	lossy := false
	for i, sg := range segs {
		safe[i] = strings.Map(func(r rune) rune {
			if r == ':' || r == '@' {
				return '_'
			}
			return r
		}, entity.SafeSeg(sg))
		if safe[i] != sg || strings.Contains(sg, ".") {
			lossy = true
		}
	}
	out := strings.Join(safe, ".")
	if lossy {
		out += entity.HashSep + entity.SegHash(segs)
	}
	return out
}

// spawnID makes a legible, collision-resistant, URL- and shell-safe task id from a
// CHAIN (the kpath of the spawning action inside its trigger — "review", "review.onDone",
// "review.onDone.onFailed") and the whole address the firing is about. It routes through
// /tasks/{id} (one path segment), so it must not contain path-breaking characters, and it gets
// pasted back onto a command line, so it must not contain shell globs either.
//
// It keys off the whole address (not just the last segment, so demo.pr.1 and other.pr.1 differ),
// and it leads with the engine that minted it (ownerOf).
func spawnID(owner, chain string, ref entity.Ref) string {
	return owner + chain + ChainSep + entity.SegToken(ref.Segs())
}

// namedSegs is where an author-NAMED derive writes: the key alone, with none of the
// (rule, subject) composition eachSegs puts in front of it, so the address is one a charter can
// spell.
//
// VERBATIM, with no sanitising and no hash. Those were an ID's, and this is a SEGMENT: a
// segment holds whatever it holds — a slash, a `#`, a dot — because nothing splits it, and the
// quoting is the rendering's problem rather than identity's. That is the whole of what a derived
// address being a path buys, said once.
func namedSegs(key string) []string { return []string{key} }

// CheckFields reports a field name a rule does not have, before the rule is decoded.
//
// It exists because an unknown field DECODES SILENTLY: gomap ignores what it does not
// recognise and has no strict-unmap option, so `startum: sdlc` installs cleanly into the
// implicit stratum and reports success — the one field that says which guarantees apply is
// exactly what a misspelling discards. Every field below has that character to some degree
// (`derve:` is a derive that never happens, `gatse:` an approval nobody is asked for), and the
// failure is always the same shape: the charter does less than it says, quietly.
//
// It is a check at the DECODE BOUNDARY rather than in the codec, because the codec is
// generated. Depth stops where the shape stops being this package's: a spawn is a launch.Spec,
// read strictly as one, and a patch or a derive is an arbitrary payload whose keys are the
// author's.
func CheckFields(node *ir.Node) error {
	return checkFields(node, "trigger", map[string]bool{
		"name": true, "stratum": true, "condition": true, "gates": true, "gating": true,
		"action": true,
	}, func(field string, val *ir.Node) error {
		switch field {
		case "condition":
			return checkFields(val, "condition", map[string]bool{
				"subject": true, "on": true, "when": true,
			}, nil)
		case "gates":
			return checkGates(val)
		case "gating":
			if ir.Get(val, "resolver") != nil {
				return fmt.Errorf("gating.resolver: %s", retiredResolver)
			}
			return checkFields(val, "gating", map[string]bool{
				"gates": true,
			}, func(field string, val *ir.Node) error {
				if field == "gates" {
					return checkGates(val)
				}
				return nil
			})
		case "action":
			return checkAction(val)
		}
		return nil
	})
}

// checkGates is CheckFields for a rule's gates. `formula` is called out by name: it was a kind
// of gate, and a charter that still writes one is told what replaced it rather than that
// the field is unknown.
func checkGates(node *ir.Node) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ArrayType {
		return nil
	}
	for i, el := range node.Values {
		el = ir.Uncomment(el)
		if el != nil && el.Type == ir.ObjectType && ir.Get(el, "formula") != nil {
			return fmt.Errorf("gates[%d] carries a `formula`, which is no longer a kind of gate. A "+
				"gate is a question with options: write `need: N` for a threshold on proceed, "+
				"`options: [{id, need}]` for outcomes of its own, and `deadline: {within: \"24h\", "+
				"default: deny}` for a clock. Waiting for a fact is `when:`, not a gate", i)
		}
		if err := checkFields(el, "gate", map[string]bool{
			"name": true, "mode": true, "need": true, "options": true, "deadline": true, "onDenied": true,
			"answerers": true, "answerersFrom": true, "pose": true, "labels": true,
		}, func(field string, val *ir.Node) error {
			if field == "onDenied" {
				return checkAction(val)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("gates[%d]: %w", i, err)
		}
	}
	return nil
}

// retiredResolver is what a rule naming its gates' resolver is told (9ce3bva0, stage 4): nothing
// names who answers. A gate names its answerers, and whoever is asked grants the resolver that
// answers for them.
const retiredResolver = "retired: nothing names a resolver to answer a rule's or a run's gates. A gate " +
	"names its `answerers`, and whoever is asked grants the resolver that answers for them " +
	"(`verse resolver grant`)"

// checkAction is CheckFields for an action, recursing where an action nests one: a sequence's
// elements and the four completion hooks, each of which is a full action.
func checkAction(node *ir.Node) error {
	return checkFields(node, "action", map[string]bool{
		"patch": true, "derive": true, "spawn": true, "seq": true,
		"onDone": true, "onFailed": true, "onCancelled": true, "onOrphaned": true,
	}, func(field string, val *ir.Node) error {
		switch field {
		case "spawn":
			if val != nil && val.Type == ir.ObjectType && ir.Get(val, "resolver") != nil {
				return fmt.Errorf("spawn.resolver: %s", retiredResolver)
			}
			// A spawn's own keys are launch.Spec's, read strictly: one it does not have (`doer`,
			// `autonomy`) would install a task that runs without what it says.
			var spec launch.Spec
			if err := gomap.FromTonyIR(ir.Uncomment(val), &spec, gomap.Strict()); err != nil {
				return fmt.Errorf("spawn: %w", err)
			}
		case "seq":
			if val.Type != ir.ArrayType {
				return nil // shape is validate's to report, in its own words
			}
			for i, el := range val.Values {
				if err := checkAction(ir.Uncomment(el)); err != nil {
					return fmt.Errorf("seq[%d]: %w", i, err)
				}
			}
		case "onDone", "onFailed", "onCancelled", "onOrphaned":
			if err := checkAction(val); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
		return nil
	})
}

// checkFields names any key outside the known set, and descends where descend says to.
func checkFields(node *ir.Node, what string, known map[string]bool, descend func(string, *ir.Node) error) error {
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	for i, field := range node.Fields {
		name := field.String
		if !known[name] {
			want := make([]string, 0, len(known))
			for k := range known {
				want = append(want, k)
			}
			sort.Strings(want)
			return fmt.Errorf("%s has no field %q (want any of %s) — an unknown field decodes "+
				"silently, so this would install as a rule that does less than it says",
				what, name, strings.Join(want, ", "))
		}
		if descend != nil {
			if err := descend(name, ir.Uncomment(node.Values[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// deriveSegs is where the entity a firing derives STANDS, under the target the rule names: the
// chain as ONE segment, then the address it is about. A function of (rule, subject) and nothing
// else, which is what makes a re-fire idempotent and what retires "two triggers, one field" for
// everything derived — rule names are unique across the charter and the segments are injective
// in the subject, so no two rules and no two subjects can address the same derived entity.
//
// AN ADDRESS RATHER THAN AN ID, which is the difference from spawnID beside it. A task id must
// be one URL path segment, so it is the same vector FLATTENED — sanitised per segment, joined,
// hashed where that is lossy. A derived entity is an ENTITY: it can simply be deeper. So it
// keeps the subject's segments exactly as they are, which means the address of a derivation
// carries what it is about faithfully, where the flattened id could only carry a lossy trace of
// it (the reason the engine also writes `verse.meta.derivedFrom`).
//
// THE CHAIN IS EXACTLY ONE SEGMENT, and that is what keeps the composition injective: the
// boundary between the rule and the subject is a POSITION rather than a character, so nothing
// has to be spent as punctuation. validTriggerName already guarantees a name is a single bare
// field; a hook chain (`review.onDone`) holds a dot and is one segment that gets quoted when it
// is rendered, which is the ordinary treatment of a name holding a delimiter.
func deriveSegs(chain string, ref entity.Ref) []string {
	return append([]string{chain}, ref.Segs()...)
}

// chainOf splits a spawn id back into its chain and the (safeSeg'd) subject it is about.
// Not the inverse of spawnID — safeSeg is lossy and the hash is not a ref — but the chain
// half IS exact, which is what reads a task's position in its trigger's action tree.
func chainOf(id string) (chain string, rest string, ok bool) {
	chain, rest, ok = strings.Cut(id, ChainSep)
	if i := strings.LastIndex(chain, AtSep); i >= 0 { // the engine that minted it, not the chain
		chain = chain[i+len(AtSep):]
	}
	return chain, rest, ok
}

// validTriggerName rejects a name that cannot be the root segment of a chain kpath. A
// chain is a real kpath — `review.onDone` addresses the Action at `.onDone` inside trigger
// `review` — so the root must be one bare field: no '.', no bracket of any kind, nothing
// kpath would have to QUOTE (a quote is not legal unescaped in a URL path), and nothing a
// shell reads as a glob.
//
// This also closes a latent collision in the old ':'-joined form, where a trigger named
// `review:done` produced the same spawn id as trigger `review`'s done hook.
func validTriggerName(name string) error {
	if name == "" {
		return fmt.Errorf("trigger name is empty")
	}
	kp, err := kpath.Parse(name)
	if err != nil {
		return fmt.Errorf("trigger name %q is not a kpath field: %w", name, err)
	}
	// One segment, a plain field, and it must render back to itself — which is what
	// rejects a name kpath would quote, without this having to know the quoting rules.
	if kp == nil || kp.Next != nil || kp.Field == nil || kp.Wild() || kp.String() != name {
		return fmt.Errorf("trigger name %q must be a single bare kpath field: a chain "+
			"addresses the action inside a trigger (review.onDone), so the root cannot "+
			"contain '.', brackets, or anything needing quotes", name)
	}
	if i := strings.IndexAny(name, ChainSep+AtSep+entity.HashSep+"*?[]{}()\\ \t"); i >= 0 {
		return fmt.Errorf("trigger name %q contains %q, which a task id built on it "+
			"would spend as punctuation — %q bounds the subject, %q the charter's address, %q "+
			"marks a hash, and the rest are shell globs that make an id unpasteable",
			name, name[i], ChainSep, AtSep, entity.HashSep)
	}
	return nil
}

// gateID makes the id of a trigger gate's question, deterministically from the trigger, the
// gate, and WHAT THE FIRING IS ABOUT — never from a counter.
//
// A counter made every firing a new question, which was invisible while a firing needed a
// fresh edge on the trigger's own subtree to happen at all. It stops being invisible the
// moment a trigger is re-evaluated rather than re-edged: the same decision, about the same
// subjects, lands on the docket again and again, and an approver answers a queue of identical
// questions. Derived from the subjects instead, a re-firing while the question stands open poses
// the id already standing there and JOINS it (task.Registry.Resolve) — one entry, one decision,
// delivered to every firing waiting on it. Once it is answered it is settled, and a firing after
// that asks a new question under an id derived from this one (askingID).
//
// It HASHES the subject list rather than spelling it: the id crosses a URL path and a command
// line (see spawnID and safeSeg), and a broadcast's subject list is unbounded. Subjects arrive
// sorted (Match.Select), so the same firing always hashes the same.
//
// Over SEGMENTS rather than renderings, for segToken's reason: a rendering is a spelling, and an
// id that tracks the spelling changes when the spelling does. The subjects are flattened into
// one vector with a NUL between each segment AND between each subject, so nothing about where
// one address ends is lost to the hash.
func gateID(owner, trigger, gate string, subjects []entity.Ref) string {
	// No capacity hint: how many segments a subject has is the subject's, not a number this
	// package knows, and a hint computed from a wrong arity is worse than letting append size it.
	var segs []string
	for _, s := range subjects {
		segs = append(segs, s.Segs()...)
		segs = append(segs, "") // an empty segment bounds one subject from the next: none can be one
	}
	// Dotted, like every other address-shaped name: it is not parsed back as one, but a
	// question id that reads as an address in a spelling nothing else uses is a second grammar
	// growing where nobody meant to plant one.
	return fmt.Sprintf("%s%s.trigger.%s.%s%s%s", owner, entity.VerseRoot,
		entity.SafeSeg(trigger), entity.SafeSeg(gate), task.GateSep, entity.SegHash(segs))
}

// bindingFor is what an action's spawn expressions are evaluated against (launch.Binding): each
// field of the payload its references read (see refSource), by its name, and `subject`, the
// RESOLVED subject it is about, as {ref, name, value}: its address, the address's last segment,
// and its payload.
//
// value is its own argument because it is not always payload: for a completion hook payload is
// the finished task's record, whose fields are `status`, `result`, `data` and `err`, and value is
// the subject at the commit its rule fired at (Engine.spawnSubjectValue). nil leaves
// `subject.value` out, so an expression that reads it is refused as naming something absent.
//
// `subject` is set LAST: it is the one reserved name, and a payload field happening to be called
// `subject` must not shadow it.
//
// entity.Fields and not ir.ToMap, because a payload put from a file with a head comment is stored
// with it: its root is a Comment node wrapping the object. IsObj sees through the wrapper, so the
// rule's condition matches; Fields unwraps, as the match does. A payload that is not an object (42,
// true) has no named parts, so only `subject` is bound.
//
// CONVERTED ONCE where value is the payload, a rule's own firing and a gate's pose: its fields
// are the entries of the whole, which an expression only reads.
func bindingFor(payload entity.Payload, subject entity.Ref, value entity.Payload) launch.Binding {
	b := launch.Binding{}
	whole := anyOf(value)
	if m, ok := whole.(map[string]any); ok && value == payload {
		maps.Copy(b, m)
	} else {
		for k, v := range entity.Fields(payload) {
			b[k] = anyOf(v)
		}
	}
	subj := map[string]any{subjPartRef: subject.String(), subjPartName: subject.Name()}
	if value != nil {
		subj[subjPartValue] = whole
	}
	b[refSubject] = subj
	return b
}

// taskFacts gives a completion hook's spawn every fact of the task that ended, whether or not
// the task had it: `result` and `err` are "" where it said nothing, and `data` is null where its
// result is no document. So `$[err]` in a prompt an onDone and an onFailed share reads the same
// in both, as `$[status]` does.
func taskFacts(b launch.Binding) {
	for _, k := range []string{"result", "err"} {
		if _, ok := b[k]; !ok {
			b[k] = ""
		}
	}
	if _, ok := b["data"]; !ok {
		b["data"] = nil
	}
}

// anyOf is n as an expression reads it (eval.ToAny), nil for no node.
func anyOf(n entity.Payload) any {
	if n == nil {
		return nil
	}
	return tonyeval.ToAny(n)
}

// payloadPaths is every field under a payload, keyed by its dotted path (`a.b`), at any depth of
// objects: what the clobber advice compares between a derive and a patch (clobberNotes). A list
// is one field, and its elements are not paths of their own: a patch writing `reviewers` writes
// the list.
//
// A SEGMENT HOLDING A DOT IS SKIPPED, itself and everything under it: `a.b` as a field name and
// `a` containing `b` would produce the same key, and one would silently win.
func payloadPaths(payload entity.Payload) map[string]entity.Payload {
	out := map[string]entity.Payload{}
	var walk func(n entity.Payload, prefix string)
	walk = func(n entity.Payload, prefix string) {
		for k, v := range entity.Fields(ir.Uncomment(n)) {
			if strings.Contains(k, ".") {
				continue
			}
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			out[path] = v
			walk(v, path)
		}
	}
	walk(payload, "")
	return out
}

// ParseMode maps a gate mode name to a Mode.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "auto", "":
		return ModeAuto, nil
	case "ask":
		return ModeAsk, nil
	default:
		return ModeAuto, fmt.Errorf("unknown gate mode %q (want auto|ask)", s)
	}
}

// checkAnswerers refuses a gate whose question nobody could settle, at install rather than at
// the first firing: a threshold above the number of answerers named, when every one of them
// is a single principal and so casts at most one ballot; and an answerer the grammar does not
// read (entity.Grammar), or a group or a place acting that does not stand. A person is taken
// at their word before they have ever presented themselves, since a gate is routed to somebody
// before they act, not after.
//
// The spawn's own gates are checked the same way, since a task's questions are posed under
// them; a hook's spawn beneath the action is not walked here.
func (e *Engine) checkAnswerers(t Trigger) error {
	var policy task.Autonomy
	if t.Action.Spawn != nil {
		policy = t.Action.Spawn.Autonomy
	}
	for i, g := range t.AllGates() {
		if err := e.checkGateAnswerers(g.Answerers, g.Need, nil); err != nil {
			return fmt.Errorf("trigger %q gates[%d]: %w", t.Name, i, err)
		}
	}
	if err := e.CheckAutonomyAnswerers(policy); err != nil {
		return fmt.Errorf("trigger %q spawn.%w", t.Name, err)
	}
	return nil
}

// CheckAutonomyAnswerers checks each ask row's answerers as a gate's are checked (checkAnswerers):
// for an autonomy a charter's spawn carries, a run started by hand, or a replacement.
func (e *Engine) CheckAutonomyAnswerers(p task.Autonomy) error {
	for i, r := range p {
		if r.Then.Ask == nil {
			continue
		}
		if err := e.checkGateAnswerers(r.Then.Ask.Answerers, r.Then.Ask.Need, nil); err != nil {
			return fmt.Errorf("autonomy[%d]: %w", i, err)
		}
	}
	return nil
}

func (e *Engine) checkGateAnswerers(answerers []string, need int, options []task.Option) error {
	if len(answerers) == 0 {
		return nil
	}
	bounded := true
	for _, a := range answerers {
		if a == task.PrincipalAnswerer {
			continue // one person, whom the task acts for: refused where it acts for nobody (validate)
		}
		g, err := entity.ParseGrantee(a)
		if err != nil {
			return fmt.Errorf("answerer %w", err)
		}
		var stands entity.Ref
		switch {
		case g.Kind == entity.Everyone, g.ID == entity.Everyone:
			bounded = false
		case g.Kind == entity.GroupKind:
			stands, bounded = entity.GroupRef(g.ID), false // a group may stand for any number
		case g.Kind == entity.ViaKind && g.Path[len(g.Path)-1] == entity.Everyone:
			bounded = false
		case g.Kind == entity.ViaKind:
			stands = entity.RefAt(g.Path...)
		}
		// A person, an agent or a runner is taken at their word; a group or a place is a record
		// somebody installed, and one that is not there is a question nobody is held out to.
		if len(stands.Segs()) > 0 {
			if _, ok, gerr := e.store.Get(stands); gerr != nil || !ok {
				return fmt.Errorf("answerer %q: nothing stands at %s", a, stands)
			}
		}
	}
	if !bounded {
		return nil
	}
	most := need
	for _, o := range options {
		if o.Need > most {
			most = o.Need
		}
	}
	if most > len(answerers) {
		return fmt.Errorf("a threshold of %d over %d answerer(s) is a question nobody can settle: "+
			"each answerer casts one ballot", most, len(answerers))
	}
	return nil
}
