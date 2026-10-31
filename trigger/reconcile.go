package trigger

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

// A RULE OF THE CHARTER IS ARMED OR PENDING. Armed, it is in e.triggers with its watches open,
// and fires. Pending, it is in e.pending with the CAUSE it waits on: it stands by name, as an
// installed rule does, and fires on nothing. The causes are the ones that can clear without
// the rule's record changing — a stratum it names not installed yet, no runner offering what
// its spawn asks, a read the vetting needs that did not happen, watches the store has not
// opened or refused, and a vet that passed at install and fails now that the world has moved.
// Everything else an install refuses, where the author is present to hear it (vet).
//
// The reconciler is what clears a cause: a pass over every pending rule, in the order they
// were placed, vetting each again and arming the ones that pass. A pass runs when something
// that could clear a cause happens — a rule placed, a stratum installed, a declaration set, and
// what versed sees: a runner or an io registered — and once a minute regardless (git-issue
// wv2pp5j0). The pace of a pass is the store's: arming opens the rule's watches, each of which
// waits in the store's line (entity.watchGate), so a restore's worth of rules arms a few at a
// time, and an install made meanwhile waits its turn in the same line.

// ErrNoOffer marks an offer check's refusal of a spawn no runner offers at all, as against one
// some runner offers and none takes: the first is held for a runner that may register, the
// second is refused where the author is present.
var ErrNoOffer = errors.New("no runner offers it")

// cause is why a pending rule is not armed.
type cause struct {
	kind causeKind
	why  string // the words: the stratum's name, the runner refusal, the read's, the store's or the vet's error
}

type causeKind int

const (
	causeStratum causeKind = iota // the stratum it names is not installed
	causeRunner                   // no runner offers what its spawn asks
	causeRead                     // a read the vetting needs did not happen
	causeStore                    // the store refused its watches
	causeRefused                  // it does not pass now, as the world stands since its install
	causeWatch                    // its watches are being opened
)

// String is the cause as status and an install's answer say it.
func (c cause) String() string {
	switch c.kind {
	case causeStratum:
		return "waiting for stratum " + c.why
	case causeRunner:
		return "waiting for a runner: " + c.why
	case causeRead:
		return "waiting on a read: " + c.why
	case causeStore:
		return "waiting on the store: " + c.why
	case causeRefused:
		return "refused: " + c.why
	default:
		return "arming: " + c.why
	}
}

// held is a pending rule with its cause.
type held struct {
	Trigger
	cause cause
}

// Pending is one pending rule as the engine reports it: its name, why it is not armed, and
// whether that is only its watches — it passed every check, and the store has not answered yet.
type Pending struct {
	Rule   string
	Cause  string
	Arming bool
}

// reconcileEvery is the backstop: a pass this long after the last, kicked or not.
const reconcileEvery = time.Minute

// Kick asks for a pass over the pending rules: something that could clear a cause happened
// that the engine does not see for itself — a runner or an io registered, a session back.
func (e *Engine) Kick() { e.kick() }

// kick asks for a pass. With the reconciler running it is asked of it; without — an engine
// nobody started, as a check's or a test's — the pass is made here and now, which opens no
// watches, since only a started engine in subtree mode has any to open.
func (e *Engine) kick() {
	if !e.reconciling.Load() {
		e.pass()
		return
	}
	select {
	case e.kicks <- struct{}{}:
	default: // a pass is already asked for
	}
}

// reconcile is the reconciler's loop: a pass per kick, and one every reconcileEvery.
func (e *Engine) reconcile(ctx context.Context) {
	every := time.NewTimer(e.every)
	defer every.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kicks:
		case <-every.C:
		}
		e.pass()
		if !every.Stop() {
			select {
			case <-every.C:
			default:
			}
		}
		every.Reset(e.every)
	}
}

// pass tries every pending rule once, in the order they were placed.
func (e *Engine) pass() {
	e.trigMu.Lock()
	snap := make([]held, len(e.pending))
	copy(snap, e.pending)
	e.trigMu.Unlock()
	for _, h := range snap {
		if e.bgctx != nil && e.bgctx.Err() != nil {
			return
		}
		e.try(h)
	}
}

// try vets one pending rule again and arms it if it passes. A rule refused now — its stratum
// arrived and its subject is outside it, say, or the runner that offered its spawn left and
// another's ceiling is lower — stays pending with the refusal as its cause: it was accepted,
// its record stands, and the world that refuses it may change back. It is reported once, as
// the cause changes. A rule still held has its cause brought up to date. One that passes has
// its watches opened, and is armed once they are: a removal that lands while they open wins,
// as it wins over a late registration (registerWatchers).
func (e *Engine) try(h held) {
	hold, err := e.vet(h.Trigger, e.OwnerOf(h.Name))
	if err != nil {
		if e.setCause(h.Name, cause{causeRefused, err.Error()}) {
			e.logger.Warn("pending rule refused: it does not pass as the world stands; it fires on nothing until it does",
				"trigger", h.Name, "waited", h.cause.String(), "err", err)
		}
		return
	}
	if hold != nil {
		e.setCause(h.Name, *hold)
		return
	}
	var cancel context.CancelFunc
	if e.subtree {
		subtrees, err := e.watchedSubtrees(h.Trigger)
		if err != nil {
			e.setCause(h.Name, cause{causeStore, err.Error()})
			return
		}
		e.setCause(h.Name, cause{causeWatch, "opening its watch"})
		var ctx context.Context
		ctx, cancel = context.WithCancel(e.bgctx)
		if err := e.openWatches(ctx, h.Trigger, subtrees); err != nil {
			cancel()
			e.setCause(h.Name, cause{causeStore, err.Error()})
			return
		}
	}
	if !e.arm(h.Name, cancel) {
		if cancel != nil {
			cancel()
		}
		return
	}
	if h.cause.kind != causeWatch {
		// Advised as it arms: what a rule held for its stratum crosses is not known until then.
		for _, w := range append(advise(h.Trigger), e.adviseDeriveClobber(h.Trigger)...) {
			e.logger.Warn("charter", "trigger", h.Name, "note", w)
		}
		e.logger.Info("pending rule armed: what it waited for is here", "trigger", h.Name, "waited", h.cause.String())
	}
}

// arm moves a pending rule to the armed ones, with the cancel that stops its watches (none in
// baseline mode), answering false where it is no longer pending — removed meanwhile.
func (e *Engine) arm(name string, cancel func()) bool {
	e.trigMu.Lock()
	i := e.pendingIndex(name)
	if i < 0 {
		e.trigMu.Unlock()
		return false
	}
	h := e.pending[i]
	e.pending = slices.Delete(slices.Clone(e.pending), i, i+1)
	e.triggers = append(e.triggers, h.Trigger)
	old := e.watchers[name]
	if cancel != nil {
		e.watchers[name] = cancel
	} else {
		delete(e.watchers, name)
	}
	e.trigMu.Unlock()
	if old != nil {
		old()
	}
	return true
}

// pendingIndex is where the pending rule of this name stands, -1 for none. Called with trigMu
// HELD.
func (e *Engine) pendingIndex(name string) int {
	for i, h := range e.pending {
		if h.Name == name {
			return i
		}
	}
	return -1
}

// setCause brings a pending rule's cause up to date, answering whether that changed it; a
// rule no longer pending is left alone.
func (e *Engine) setCause(name string, c cause) bool {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	i := e.pendingIndex(name)
	if i < 0 || e.pending[i].cause == c {
		return false
	}
	e.pending[i].cause = c
	return true
}

// Pendings is every pending rule with its cause, by name.
func (e *Engine) Pendings() []Pending {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	out := make([]Pending, 0, len(e.pending))
	for _, h := range e.pending {
		out = append(out, Pending{Rule: h.Name, Cause: h.cause.String(), Arming: h.cause.kind == causeWatch})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Arming is the names of the rules whose watches are still being opened, sorted: they passed
// every check and wait on the store alone. Empty on a verse that is up.
func (e *Engine) Arming() []string {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	var out []string
	for _, h := range e.pending {
		if h.cause.kind == causeWatch {
			out = append(out, h.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Held returns the pending rules: each is written down, occupies its name and fires on nothing
// until what it waits for is here (Pendings says what).
func (e *Engine) Held() []Trigger {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	return e.pendingTriggers()
}

// pendingTriggers is the pending rules without their causes, for the walks over the charter
// whole. Called with trigMu HELD.
func (e *Engine) pendingTriggers() []Trigger {
	out := make([]Trigger, 0, len(e.pending))
	for _, h := range e.pending {
		out = append(out, h.Trigger)
	}
	return out
}
