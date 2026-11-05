package versed

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/pkg/codec"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/schedule"
)

// A schedule is declared in a charter's head document beside the strata — `{name, every,
// from}` — and installed as a record at verse.schedule.<id>, the id carrying the package's
// address as a rule's does. Install is create-or-update on `every` and `from`; `next` is
// stamped once, and stamped again only when the grid it stood on changed, so a re-install or a
// restart arms against the standing instant. The installer takes the record's place, so the
// ticks written as the schedule (entity.Via) carry the installer's grant.

// schedule installs one declaration, remembering what it replaced.
func (tx *charterTx) schedule(def *ir.Node) (int, error) {
	if tx.scope != "" {
		return http.StatusBadRequest, errors.New("a schedule ticks for the verse, not for a scope: " +
			"declare it in the baseline charter or a package's")
	}
	var decl schedule.Schedule
	if err := codec.FromNode(def, &decl); err != nil {
		return http.StatusBadRequest, err
	}
	if err := decl.Validate(); err != nil {
		return http.StatusBadRequest, err
	}
	at, store := tx.at, tx.store // captured: the transaction spans packages (see imports)
	id := schedule.RecordID(at, decl.Name)
	tx.keep("schedules", id)
	old, had, err := tx.s.Store.Get(schedule.Ref(id))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	before, err := entity.ActsAs(store, schedule.Ref(id))
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	if err := tx.s.installSchedule(store, at, decl, tx.as()); err != nil {
		return http.StatusBadRequest, err
	}
	tx.push(func() error {
		if !had {
			// As the system: what this install made is taken back out whoever it turned out to
			// be — a schedule that landed as nobody's is not its installer's to remove.
			return tx.s.removeSchedule(entity.As(store, entity.SystemCharter), id)
		}
		return tx.s.putScheduleRecord(store, id, old.Payload)
	})
	// Its ticks are written as the schedule, which acts as whoever its record says.
	if err := actsAsInstaller(store, schedule.Ref(id), tx.as(), before); err != nil {
		return charterCode(err), err
	}
	return http.StatusCreated, nil
}

// installSchedule writes a declaration's record and arms it.
func (s *Server) installSchedule(store entity.Store, at string, decl schedule.Schedule, installer string) error {
	id := schedule.RecordID(at, decl.Name)
	ref := schedule.Ref(id)
	old, had, err := s.Store.Get(ref)
	if err != nil {
		return err
	}
	rec := entity.Obj{
		schedule.EveryField: decl.Every,
		schedule.FromField:  entity.Removed(),
	}
	if decl.From != "" {
		rec[schedule.FromField] = decl.From
	}
	if at != "" {
		rec[schedule.AtField] = at
	}
	// The grid moved, or there was none: stamp `next` afresh. Otherwise the standing instant is
	// the one to arm against, as a deadline's stamp is (mirror.StampDeadline).
	sameGrid := had && entity.Str(old.Payload, schedule.EveryField) == decl.Every &&
		entity.Str(old.Payload, schedule.FromField) == decl.From
	if !sameGrid || entity.Str(old.Payload, schedule.NextField) == "" {
		every, err := decl.Period()
		if err != nil {
			return err
		}
		from, err := decl.Phase(s.zone())
		if err != nil {
			return err
		}
		rec[schedule.NextField] = schedule.Key(schedule.Next(from, every, s.now()))
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: rec.Payload()}); err != nil {
		return fmt.Errorf("schedule %q: writing %s: %w", decl.Name, ref, err)
	}
	// THE SYSTEM'S TOO, unlike a package's address: the seed holds `owner` beneath
	// verse.schedule out to everyone so that anybody may declare one, and that grant reaches
	// every record below it until an owner record is crossed. A standard schedule with no
	// record would be everybody's to backfill.
	if installer != "" {
		if err := entity.Claim(store, ref, installer); err != nil {
			fmt.Fprintf(os.Stderr, "verse: schedule %s: taking %s for %s: %v\n", id, ref, installer, err)
		}
	}
	s.armSchedule(id)
	return nil
}

// putScheduleRecord puts a record back as it stood, for an undo: a replace, so a field the
// install added goes away with it.
func (s *Server) putScheduleRecord(store entity.Store, id string, rec entity.Payload) error {
	ref := schedule.Ref(id)
	cur, _, err := s.Store.Get(ref)
	if err != nil {
		return err
	}
	if err := entity.ReplaceAt(store, ref.At(), cur.Payload, rec); err != nil {
		return err
	}
	s.armSchedule(id)
	return nil
}

// removeSchedule removes a schedule's record and then drops its timer, in that order: the delete
// is the requester's and is checked, the timer is this process's and is not, so dropped first a
// refused removal would leave the schedule standing and stopped — `minutely`, and with it the
// calendar, by anybody who asked. A timer that fires in between writes nothing: its tick is
// conditional on the `next` it read.
func (s *Server) removeSchedule(store entity.Store, id string) error {
	if _, _, err := store.Write(entity.Write{At: schedule.Ref(id).At()}); err != nil {
		return fmt.Errorf("removing schedule %s: %w", id, err)
	}
	s.dropClock(id)
	return nil
}

// packageSchedules is the record of every schedule a package declared.
func (s *Server) packageSchedules(at string) ([]entity.Ref, error) {
	ents, err := entity.BelowErr(s.Store, schedule.IsRecord, 1, entity.VerseRoot, schedule.System)
	if err != nil {
		return nil, err
	}
	var out []entity.Ref
	for _, ent := range ents {
		if entity.Str(ent.Payload, schedule.AtField) == at {
			out = append(out, ent.Ref)
		}
	}
	return out, nil
}

// removePackageSchedules removes every schedule a package declared, at its uninstall.
func (s *Server) removePackageSchedules(store entity.Store, at string) error {
	refs, err := s.packageSchedules(at)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := s.removeSchedule(store, ref.Name()); err != nil {
			return err
		}
	}
	return nil
}

// removeScheduleNamed removes one schedule by its declared name in a charter, for the charter's
// own remove-by-name. False when no such schedule stands there.
func (s *Server) removeScheduleNamed(store entity.Store, at, name string) (bool, error) {
	id := schedule.RecordID(at, name)
	if _, ok, err := s.Store.Get(schedule.Ref(id)); err != nil || !ok {
		return false, err
	}
	return true, s.removeSchedule(store, id)
}
