package versed

import (
	"errors"
	"net/http"
	"sort"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/schedule"
)

// The schedules on the wire: a listing of every record with its `next` and its standing tick,
// and a backfill that writes the tick for a past instant as the schedule.

// listSchedules answers GET /schedules.
func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	ents, err := entity.BelowErr(store, schedule.IsRecord, 1, entity.VerseRoot, schedule.System)
	if unreadable(w, r, err) {
		return
	}
	rows := make([]api.ScheduleRow, 0, len(ents))
	for _, ent := range ents {
		row := api.ScheduleRow{
			ID:    ent.Ref.Name(),
			Every: entity.Str(ent.Payload, schedule.EveryField),
			From:  entity.Str(ent.Payload, schedule.FromField),
			Next:  entity.Str(ent.Payload, schedule.NextField),
			At:    entity.Str(ent.Payload, schedule.AtField),
			By:    entity.Str(ent.Payload, schedule.ByField),
		}
		// The standing tick is the latest; a backfill beside it is older by construction.
		keys := schedule.Standing(ent.Payload)
		sort.Strings(keys)
		if len(keys) > 0 {
			tick := entity.Get(entity.Get(ent.Payload, schedule.TickField), keys[len(keys)-1])
			row.Tick = entity.Str(tick, "due")
			if m := entity.Get(tick, "missed"); m != nil && m.Int64 != nil {
				row.Missed = *m.Int64
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	writeOut(w, r, http.StatusOK, rows)
}

// tickSchedule answers POST /schedules/{id}/tick: a backfill. The caller must hold write on the
// record — a schedule is its installer's — and the tick is then written as the schedule, the
// same entity the timer would have written.
func (s *Server) tickSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body api.ScheduleTick
	if err := codec.Read(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	due, err := schedule.ParseKey(body.Due)
	if err != nil {
		http.Error(w, "due: an RFC3339 instant: "+err.Error(), http.StatusBadRequest)
		return
	}
	ref := schedule.Ref(id)
	if _, ok, err := s.Store.Get(ref); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	} else if !ok {
		http.Error(w, "no schedule "+id, http.StatusNotFound)
		return
	}
	if err := entity.Check(s.Store, entity.PrincipalOf(s.scopedStore(r)), entity.ReadWrite, ref,
		"backfilling a tick of it"); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := s.backfill(id, due); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, entity.ErrForbidden) {
			code = http.StatusForbidden
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeOut(w, r, http.StatusCreated, map[string]any{"schedule": id, "tick": schedule.Key(due)})
}
