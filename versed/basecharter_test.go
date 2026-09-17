package versed

import (
	"testing"
	"time"

	"github.com/signadot/tony-format/go-tony/ir/kpath"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/entity/entitytest"
	"github.com/signadot/verse/schedule"
)

// Every verse stands on the base charter: the standard schedules are records the moment it
// starts, and the calendar is derived from the hourly tick, one entity per fact, so a fact
// commits only when it changes.

func calendarFact(t *testing.T, srv *Server, name, field string) string {
	t.Helper()
	for range 400 {
		ent, ok, err := srv.Store.Get(entity.VerseRef(schedule.System, name))
		if err == nil && ok {
			if v := entity.Str(ent.Payload, field); v != "" {
				return v
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("verse.schedule.%s never carried %s", name, field)
	return ""
}

func TestTheBaseCharterStandsEveryVerseUp(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())

	for _, id := range []string{"hourly", "daily", "weekly"} {
		rec, ok := scheduleRecord(t, srv, id)
		if !ok {
			t.Fatalf("no standard schedule %s", id)
		}
		if entity.Str(rec, schedule.ByField) != entity.SystemCharter {
			t.Fatalf("%s is by %q, want the system's charter", id, entity.Str(rec, schedule.ByField))
		}
	}
	daily, _ := scheduleRecord(t, srv, "daily")
	if next, err := schedule.ParseKey(entity.Str(daily, schedule.NextField)); err != nil || next.Hour() != 0 || next.Minute() != 0 {
		t.Fatalf("daily next = %q, want a midnight", entity.Str(daily, schedule.NextField))
	}
	weekly, _ := scheduleRecord(t, srv, "weekly")
	if next, err := schedule.ParseKey(entity.Str(weekly, schedule.NextField)); err != nil || next.Weekday() != time.Monday {
		t.Fatalf("weekly next = %q, want a Monday", entity.Str(weekly, schedule.NextField))
	}
}

func TestTheCalendarIsDerivedFromTheHourlyTick(t *testing.T) {
	srv := NewWithStore(ScriptedBuilder, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	scheduleRecord(t, srv, "hourly")

	// Two ticks an hour apart, on one day: the daemon's own write, called by hand so the test
	// does not wait for the top of the hour.
	first := time.Date(2026, 9, 18, 3, 30, 0, 0, time.UTC)
	if _, _, err := srv.Store.Write(entity.Write{
		At: schedule.Ref("hourly").At(schedule.NextField), Value: entity.Val(schedule.Key(first.Truncate(time.Hour))),
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.tickAt("hourly", first); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := calendarFact(t, srv, "time", "time"); got != "03:00" {
		t.Fatalf("time = %q, want 03:00", got)
	}
	if got := calendarFact(t, srv, "date", "date"); got != "2026-09-18" {
		t.Fatalf("date = %q", got)
	}
	if got := calendarFact(t, srv, "weekday", "weekday"); got != "Fri" {
		t.Fatalf("weekday = %q", got)
	}
	if got := calendarFact(t, srv, "timezone", "timezone"); got != "UTC" {
		t.Fatalf("timezone = %q", got)
	}
	dateKP, err := kpath.Parse("verse.schedule.date")
	if err != nil {
		t.Fatal(err)
	}
	before, err := srv.Store.Since(0, dateKP)
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.tickAt("hourly", first.Add(time.Hour)); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	for range 400 {
		if got := calendarFact(t, srv, "time", "time"); got == "04:00" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := calendarFact(t, srv, "time", "time"); got != "04:00" {
		t.Fatalf("time after the second tick = %q, want 04:00", got)
	}
	// The date did not change, so the derive of the same bytes was a no-op: no commit landed
	// at verse.schedule.date for the second tick.
	after, err := srv.Store.Since(0, dateKP)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("verse.schedule.date took %d commits across two ticks of one day, want %d", len(after), len(before))
	}
}
