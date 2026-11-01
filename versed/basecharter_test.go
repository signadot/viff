package versed

import (
	"testing"
	"time"

	"github.com/signadot/verse/runner/runnerd/runnerdtest"

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
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())

	for _, id := range []string{"minutely", "hourly", "daily", "weekly"} {
		_, ok := scheduleRecord(t, srv, id)
		if !ok {
			t.Fatalf("no standard schedule %s", id)
		}
		if who, err := entity.ActsAs(srv.Store, schedule.Ref(id)); err != nil || who != entity.SystemCharter {
			t.Fatalf("%s is %q's (%v), want the system's charter", id, who, err)
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
	srv := serverWith(t, runnerdtest.Scripted, entitytest.Store(t))
	srv.Zone = time.UTC
	srv.Start(t.Context())
	scheduleRecord(t, srv, "hourly")

	// The daemon's own writes, called by hand so the test does not wait for the clock. A
	// schedule is armed against an instant before the tick, since a tick ahead of `next`
	// writes nothing.
	arm := func(id string, next time.Time) {
		t.Helper()
		if _, _, err := srv.Store.Write(entity.Write{
			At: schedule.Ref(id).At(schedule.NextField), Value: entity.Val(schedule.Key(next)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Date(2026, 9, 18, 3, 30, 0, 0, time.UTC)

	// The clock, to the minute, from the minutely tick.
	arm("minutely", first)
	if err := srv.tickAt("minutely", first); err != nil {
		t.Fatalf("first minute: %v", err)
	}
	if got := calendarFact(t, srv, "time", "time"); got != "03:30" {
		t.Fatalf("time = %q, want 03:30", got)
	}
	if err := srv.tickAt("minutely", first.Add(time.Minute)); err != nil {
		t.Fatalf("second minute: %v", err)
	}
	for range 400 {
		if got := calendarFact(t, srv, "time", "time"); got == "03:31" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := calendarFact(t, srv, "time", "time"); got != "03:31" {
		t.Fatalf("time after the second minute = %q, want 03:31", got)
	}

	// The calendar, from the hourly tick.
	arm("hourly", first.Truncate(time.Hour))
	if err := srv.tickAt("hourly", first); err != nil {
		t.Fatalf("first hour: %v", err)
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
	// A second hour of the same day, then the first hour of the next: the rule fires for both
	// in order, so once the date has moved the same-day tick has been evaluated — and it left
	// no commit at verse.schedule.date, the same bytes being a no-op.
	if err := srv.tickAt("hourly", first.Add(time.Hour)); err != nil {
		t.Fatalf("second hour: %v", err)
	}
	if err := srv.tickAt("hourly", first.Add(21*time.Hour)); err != nil {
		t.Fatalf("next day: %v", err)
	}
	for range 400 {
		if got := calendarFact(t, srv, "date", "date"); got == "2026-09-19" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := calendarFact(t, srv, "date", "date"); got != "2026-09-19" {
		t.Fatalf("date after the next day's tick = %q", got)
	}
	after, err := srv.Store.Since(0, dateKP)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("verse.schedule.date took %d commits across three ticks on two days, want %d",
			len(after), len(before)+1)
	}
}
