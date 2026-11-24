package trigger

// A PARKED LAUNCH IS RECORDED, so that it outlives the viff that parked it as the suspension
// holding it does. The record stands in scratch (entity.Scratch), at mirror.task.parked.<id> for
// the task the launch would start: nothing watches it, it has no history worth keeping, and one
// engine writes it. It is written when the launch parks and removed when the lift starts it. An
// engine reads its own before it settles the claims the last viff left, so a claim waiting on a
// parked launch is found in flight.
//
// Scratch is the store's to keep, and is flushed behind the write: a store that goes down before
// the flush may not have the record when it is back. A claim that waited on a launch whose record
// is not there is settled as a task that was lost.

import (
	"context"
	"errors"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/launch"
	"go.viff.dev/viff/pkg/mirror"
)

// parkedKind is where the records stand: mirror.task.parked.<id>, in scratch.
const parkedKind = "parked"

func parkedRef(id string) entity.Ref { return entity.ViffRef(mirror.System, parkedKind, id) }

// id is the task the launch would start.
func (l parkedLaunch) id(e *Engine) string { return spawnID(e.idOwner(), chainID(l.chain), l.subj) }

// recordParked writes a parked launch's record. A store with no scratch, or one that refuses the
// write, leaves the launch parked in this viff alone.
func (e *Engine) recordParked(l parkedLaunch) {
	sc, ok := entity.ScratchOf(e.store)
	if !ok {
		return
	}
	passed := make([]*ir.Node, 0, len(l.passed))
	for i := range l.passed {
		n, err := l.passed[i].ToTonyIR()
		if err != nil {
			e.logger.Warn("a parked launch was not recorded, and will not outlive this viff", "chain", l.chain, "subject", l.subj.String(), "err", err)
			return
		}
		passed = append(passed, n)
	}
	rec := entity.Obj{
		"flow":       l.subj.String(),
		"chain":      l.chain,
		"occurrence": l.occurrence,
		"binding":    entity.Obj(l.binding).Payload(),
		"passed":     ir.FromSlice(passed),
	}
	if err := sc.ScratchWrite(parkedRef(l.id(e)), rec.Payload()); err != nil && !errors.Is(err, entity.ErrNoScratch) {
		e.logger.Warn("a parked launch was not recorded, and will not outlive this viff", "chain", l.chain, "subject", l.subj.String(), "err", err)
	}
}

// forgetParked removes the record of a launch that is parked no longer.
func (e *Engine) forgetParked(id string) {
	sc, ok := entity.ScratchOf(e.store)
	if !ok {
		return
	}
	if err := sc.ScratchWrite(parkedRef(id), nil); err != nil && !errors.Is(err, entity.ErrNoScratch) {
		e.logger.Warn("the record of a launch that is parked no longer was not removed", "task", id, "err", err)
	}
}

// restoreParked parks again the launches the last viff left parked under this engine's owner
// prefix, and reports whether it did. The records are read until the store answers, for as long
// as the engine runs. A record is removed, and nothing parked, where what it names is not there
// to launch: its action no longer in the charter, or its task already started, which the task's
// record of that occurrence says.
func (e *Engine) restoreParked(ctx context.Context) bool {
	sc, ok := entity.ScratchOf(e.store)
	if !ok {
		return true
	}
	var recs *ir.Node
	for wait := 250 * time.Millisecond; ; wait = min(2*wait, 8*time.Second) {
		got, err := sc.ScratchRead(entity.ViffRef(mirror.System, parkedKind))
		if err == nil {
			recs = got
			break
		}
		if errors.Is(err, entity.ErrNoScratch) {
			return true // a view over a store with none: nothing was recorded, so nothing is left
		}
		e.logger.Warn("the launches the last viff left parked could not be read; the engine stays shut and asks again", "err", err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
	for id, rec := range entity.Fields(recs) {
		if OwnerOf(id) != e.idOwner() {
			continue
		}
		l, ok := e.parkedOf(id, rec)
		if !ok {
			e.forgetParked(id)
			continue
		}
		e.parkMu.Lock()
		e.parked = append(e.parked, l)
		e.parkMu.Unlock()
	}
	return true
}

// parkedOf reads a record back into the launch it parks, false where there is nothing to launch.
func (e *Engine) parkedOf(id string, rec entity.Payload) (parkedLaunch, bool) {
	chain := entity.Str(rec, "chain")
	subj, err := entity.ParseAddr(entity.Str(rec, "flow"))
	if err != nil || chain == "" || spawnID(e.idOwner(), chainID(chain), subj) != id {
		e.tracef("parked %s: the record does not name its launch; removed", id)
		return parkedLaunch{}, false
	}
	l := parkedLaunch{chain: chain, subj: subj, occurrence: mirror.OccurrenceOf(rec)}
	t, ok := e.Rule(mirror.TriggerOf(chain))
	if !ok {
		e.tracef("parked %s: its rule is not in the charter; removed", id)
		return parkedLaunch{}, false
	}
	a, ok := navigate(t, chain)
	if !ok || a.Spawn == nil {
		e.tracef("parked %s: no spawn stands at its chain; removed", id)
		return parkedLaunch{}, false
	}
	l.a = *a
	if ent, ok, err := e.store.Get(mirror.Ref(id)); err == nil && ok && mirror.OccurrenceOf(ent.Payload) == l.occurrence {
		e.tracef("parked %s: its task has a record of this occurrence, so it started; removed", id)
		return parkedLaunch{}, false
	}
	if b, ok := anyOf(entity.Get(rec, "binding")).(map[string]any); ok {
		l.binding = launch.Binding(b)
	}
	if ps := ir.Uncomment(entity.Get(rec, "passed")); ps != nil && ps.Type == ir.ArrayType {
		for _, n := range ps.Values {
			var p launch.Passed
			if err := p.FromTonyIR(n); err != nil {
				e.tracef("parked %s: a gate it passed could not be read (%v); removed", id, err)
				return parkedLaunch{}, false
			}
			l.passed = append(l.passed, p)
		}
	}
	return l, true
}

// parkedAt reports whether the launch of chain about subj is parked: a hooked spawn's claim
// waits on it, and the task the lift starts releases the claim when it ends.
func (e *Engine) parkedAt(chain string, subj entity.Ref) bool {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	for _, l := range e.parked {
		if l.chain == chain && l.subj == subj {
			return true
		}
	}
	return false
}
