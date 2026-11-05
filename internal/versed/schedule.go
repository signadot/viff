package versed

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/schedule"
)

// A schedule's clock is durable and the timer that fires it is not: the record at
// verse.schedule.<id> holds `next`, the daemon arms a timer at it, and every firing re-reads the
// record. Mirrors a question's deadline (armDeadline, armDeadlines, settleByDeadline): stamped
// once, re-armed at start, and the record decides whether the clock has run out.
//
// A tick is one commit, written as the schedule: `next` advances to the first grid point after
// now, every standing tick is removed and the one now due is written, under a precondition that
// `next` is still what the timer was armed against. Two timers — a restart's re-arm beside one
// that fired just before it, a second replica — then write one tick, and the loser re-reads and
// re-arms. Nothing else ever writes under a schedule's `tick`, so a rule reading there fires
// once per tick, whatever else it does.

// clocks is the timers standing for this process, by schedule id: what a re-arm stops and an
// uninstall drops.
type clocks struct {
	mu     sync.Mutex
	timers map[string]Timer
}

// Zone is the zone a tick's calendar is spelled in, and the zone a period of whole days steps
// in. time.Local, which a deployment sets with TZ; a test points it elsewhere.
func (s *Server) zone() *time.Location {
	if s.Zone != nil {
		return s.Zone
	}
	return time.Local
}

// armSchedules re-arms, at startup, every schedule standing in the store. After the charter
// and the packages, which declare them, and the system charter, which declares the standard
// ones; an install through those paths arms as it writes, so this is for the records a restart
// finds.
//
// A listing that did not answer armed nothing, and says so (entity.IsUnread) so the phase asks
// again: a clock that is never set is a schedule that never ticks.
func (s *Server) armSchedules() error {
	if err := s.awaiting(phaseCharter, phasePackages); err != nil {
		return err
	}
	ents, err := entity.BelowErr(s.Store, schedule.IsRecord, 1, entity.VerseRoot, schedule.System)
	if err != nil {
		return entity.Unread(fmt.Errorf("the schedules could not be read, so none was armed: %w", err))
	}
	for _, ent := range ents {
		s.armSchedule(ent.Ref.Name())
	}
	return nil
}

// armSchedule reads a schedule's record and arms a timer at its `next`. A timer already standing
// for the id is replaced, so arming is idempotent. A record that is gone, or one that has no
// `next` to arm against, arms nothing.
func (s *Server) armSchedule(id string) {
	ent, ok, err := s.Store.Get(schedule.Ref(id))
	if err != nil || !ok {
		s.dropClock(id)
		return
	}
	at, err := schedule.ParseKey(entity.Str(ent.Payload, schedule.NextField))
	if err != nil {
		s.dropClock(id)
		return
	}
	s.clocks.mu.Lock()
	defer s.clocks.mu.Unlock()
	if s.clocks.timers == nil {
		s.clocks.timers = map[string]Timer{}
	}
	if old := s.clocks.timers[id]; old != nil {
		old.Stop()
	}
	s.clocks.timers[id] = s.at(at, func() { s.tick(id) })
}

// dropClock stops and forgets a schedule's timer: an uninstall, or a record that is gone.
func (s *Server) dropClock(id string) {
	s.clocks.mu.Lock()
	defer s.clocks.mu.Unlock()
	if old := s.clocks.timers[id]; old != nil {
		old.Stop()
		delete(s.clocks.timers, id)
	}
}

// tick is what a schedule's timer does: write the tick now due, if the clock has run out, and
// arm the next. The record is read again here — the timer that fired may be one an earlier
// arming set, and the record is what says whether the instant has passed.
func (s *Server) tick(id string) {
	if err := s.tickAt(id, s.now()); err != nil && !errors.Is(err, errTickRefused) {
		fmt.Fprintf(os.Stderr, "verse: schedule %s: %v\n", id, err)
	}
	s.armSchedule(id)
}

// errTickRefused is a precondition that did not hold: another timer wrote this tick.
var errTickRefused = errors.New("another timer ticked first")

// tickAt writes the tick due at now, as the schedule, under the precondition that `next` is
// still what the record said when it was read here. An instant still ahead writes nothing.
func (s *Server) tickAt(id string, now time.Time) error {
	ent, ok, err := s.Store.Get(schedule.Ref(id))
	if err != nil {
		return err
	}
	if !ok {
		return nil // uninstalled between the arming and the firing
	}
	return s.tickFrom(id, ent.Payload, now)
}

// tickFrom is tickAt over a record already read: the precondition is against what THIS reading
// said `next` was, which is what makes two timers that read the same record write one tick.
func (s *Server) tickFrom(id string, rec entity.Payload, now time.Time) error {
	ref := schedule.Ref(id)
	armed, err := schedule.ParseKey(entity.Str(rec, schedule.NextField))
	if err != nil {
		return fmt.Errorf("the record's next %q is not an instant: %w", entity.Str(rec, schedule.NextField), err)
	}
	if now.Before(armed) {
		return nil
	}
	decl := schedule.Schedule{Name: schedule.NameOf(id), Every: entity.Str(rec, schedule.EveryField),
		From: entity.Str(rec, schedule.FromField)}
	every, err := decl.Period()
	if err != nil {
		return err
	}
	from, err := decl.Phase(s.zone())
	if err != nil {
		return err
	}
	due := schedule.Due(from, every, now)
	if due.Before(armed) {
		due = armed // armed on the grid by construction; a phase edit is the one way off it
	}
	next := schedule.Next(from, every, now)
	ticks := entity.Obj{}
	for _, k := range schedule.Standing(rec) {
		ticks[k] = entity.Removed()
	}
	ticks[schedule.Key(due)] = schedule.Tick(due, schedule.Missed(armed, due, every), s.zone())
	store := entity.As(s.Store, entity.Via(ref))
	committed, _, err := store.Write(entity.Write{
		At:    ref.At(),
		Value: entity.Obj{schedule.NextField: schedule.Key(next), schedule.TickField: ticks}.Payload(),
		When:  []entity.Require{{At: ref.At(schedule.NextField), Pattern: entity.Unchanged(entity.Val(schedule.Key(armed)))}},
	})
	if err != nil {
		return err
	}
	if !committed {
		return errTickRefused
	}
	return nil
}

// backfill writes the tick for one past instant beside the standing one, create-only, as the
// schedule: the same entity the schedule would have written, so writing it twice is one write.
// An instant off the schedule's grid, or ahead of now, is refused.
func (s *Server) backfill(id string, due time.Time) error {
	ref := schedule.Ref(id)
	ent, ok, err := s.Store.Get(ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no schedule %q", id)
	}
	decl := schedule.Schedule{Name: schedule.NameOf(id), Every: entity.Str(ent.Payload, schedule.EveryField),
		From: entity.Str(ent.Payload, schedule.FromField)}
	every, err := decl.Period()
	if err != nil {
		return err
	}
	from, err := decl.Phase(s.zone())
	if err != nil {
		return err
	}
	if due.After(s.now()) {
		return fmt.Errorf("%s is ahead: a backfill is for an instant that has passed", schedule.Key(due))
	}
	if on := schedule.Due(from, every, due); !on.Equal(due) {
		return fmt.Errorf("%s is not on the schedule's grid; the nearest before it is %s", schedule.Key(due), schedule.Key(on))
	}
	key := schedule.Key(due)
	store := entity.As(s.Store, entity.Via(ref))
	_, _, err = store.Write(entity.Write{
		At:    ref.At(schedule.TickField, key),
		Value: schedule.Tick(due, 0, s.zone()),
		When:  []entity.Require{{Absent: true}},
	})
	return err
}
