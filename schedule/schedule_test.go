package schedule

import (
	"testing"
	"time"

	"github.com/signadot/verse/entity"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s not available: %v", name, err)
	}
	return loc
}

// The grid is from + n*every, and Next is the first point strictly after the instant asked.
func TestNextIsTheFirstGridPointStrictlyAfter(t *testing.T) {
	from := Epoch(time.UTC)
	at := time.Date(2026, 9, 18, 2, 30, 0, 0, time.UTC)
	if got := Next(from, time.Hour, at); !got.Equal(time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("hourly next after 02:30 = %s", got)
	}
	// On the grid exactly: the NEXT point, not this one.
	onGrid := time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC)
	if got := Next(from, time.Hour, onGrid); !got.Equal(onGrid.Add(time.Hour)) {
		t.Fatalf("hourly next at 03:00 = %s, want 04:00", got)
	}
	// Before the phase: the phase itself.
	if got := Next(onGrid, time.Hour, at); !got.Equal(onGrid) {
		t.Fatalf("next before the phase = %s, want the phase %s", got, onGrid)
	}
}

// A period of whole days steps by calendar days, so a daily schedule keeps its wall-clock time
// across a DST change instead of drifting by the hour the clocks moved.
func TestADailyScheduleKeepsItsWallClockTimeAcrossDST(t *testing.T) {
	loc := mustZone(t, "Europe/Berlin")
	from := Epoch(loc)
	// Clocks go forward on 2026-03-29 in Berlin.
	before := time.Date(2026, 3, 28, 12, 0, 0, 0, loc)
	next := Next(from, 24*time.Hour, before)
	if next.Hour() != 0 || next.Day() != 29 {
		t.Fatalf("next after the 28th = %s, want midnight on the 29th", next)
	}
	after := Next(from, 24*time.Hour, next)
	if after.Hour() != 0 || after.Day() != 30 {
		t.Fatalf("next after the change = %s, want midnight on the 30th, not 01:00", after)
	}
	if d := after.Sub(next); d != 23*time.Hour {
		t.Fatalf("the day of the change is %s long, want 23h", d)
	}
	// Weekly lands on a Monday.
	weekly := Next(from, 7*24*time.Hour, before)
	if weekly.Weekday() != time.Monday || weekly.Hour() != 0 {
		t.Fatalf("weekly next = %s, want a Monday at midnight", weekly)
	}
}

// A timer armed against 09:00 that fires at 10:30 writes the 10:00 tick, and says 09:00 went
// unticked.
func TestDueIsTheLastGridPointAndMissedCountsTheSkipped(t *testing.T) {
	from := Epoch(time.UTC)
	armed := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	if due := Due(from, time.Hour, armed.Add(time.Second)); !due.Equal(armed) {
		t.Fatalf("due just after 09:00 = %s, want 09:00", due)
	}
	late := armed.Add(90 * time.Minute)
	due := Due(from, time.Hour, late)
	if !due.Equal(armed.Add(time.Hour)) {
		t.Fatalf("due at 10:30 = %s, want 10:00", due)
	}
	if got := Missed(armed, due, time.Hour); got != 1 {
		t.Fatalf("missed = %d, want 1 (09:00)", got)
	}
	if got := Missed(armed, armed, time.Hour); got != 0 {
		t.Fatalf("on time: missed = %d", got)
	}
	// Across a DST change a daily count is still a count of days.
	loc := mustZone(t, "Europe/Berlin")
	a := time.Date(2026, 3, 28, 0, 0, 0, 0, loc)
	if got := Missed(a, a.AddDate(0, 0, 3), 24*time.Hour); got != 3 {
		t.Fatalf("three days across the change: missed = %d", got)
	}
}

func TestATickCarriesItsCalendarInTheZone(t *testing.T) {
	loc := mustZone(t, "Europe/Berlin")
	due := time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC) // 03:00 in Berlin
	p := Tick(due, 1, loc)
	want := map[string]string{
		"due": "2026-09-18T01:00:00Z", "date": "2026-09-18", "time": "03:00",
		"weekday": "Fri", "timezone": "Europe/Berlin",
	}
	for k, v := range want {
		if got := entity.Str(p, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := entity.Get(p, "year"); got == nil || got.Int64 == nil || *got.Int64 != 2026 {
		t.Errorf("year = %v", got)
	}
	if got := entity.Get(p, "missed"); got == nil || got.Int64 == nil || *got.Int64 != 1 {
		t.Errorf("missed = %v", got)
	}
	if IsCalendarName("hourly") || !IsCalendarName("weekday") {
		t.Error("IsCalendarName is wrong about a name")
	}
}

func TestValidateRefusesWhatCannotTick(t *testing.T) {
	for _, s := range []Schedule{
		{Name: "", Every: "1h"},
		{Name: "date", Every: "1h"},
		{Name: "a:b", Every: "1h"},
		{Name: "x", Every: "0s"},
		{Name: "x", Every: "soon"},
		{Name: "x", Every: "1h", From: "yesterday"},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v validated", s)
		}
	}
	if err := (Schedule{Name: "hourly", Every: "1h"}).Validate(); err != nil {
		t.Errorf("a plain schedule was refused: %v", err)
	}
}

func TestIsTickSubject(t *testing.T) {
	if !IsTickSubject([]string{"verse", "schedule", "hourly", "tick", "*"}) {
		t.Error("the tick glob is not a tick subject")
	}
	if IsTickSubject([]string{"verse", "schedule", "hourly"}) || IsTickSubject([]string{"demo", "pr", "*"}) {
		t.Error("a record or a world address read as a tick subject")
	}
	if NameOf("verse-dev.drift:hourly") != "hourly" || NameOf("hourly") != "hourly" {
		t.Error("NameOf")
	}
}

// A tick's zone is a name a client can read. time.Local prints as "Local", which is nobody's
// name for anything, so the name comes from where the process got its zone; a zone with no
// name at all is spelled as its offset at the instant.
func TestZoneNameIsOneAClientCanRead(t *testing.T) {
	at := time.Date(2026, 9, 18, 1, 0, 0, 0, time.UTC)
	if got := ZoneName(mustZone(t, "Europe/Berlin"), at); got != "Europe/Berlin" {
		t.Fatalf("a named zone = %q", got)
	}
	if got := ZoneName(time.UTC, at); got != "UTC" {
		t.Fatalf("UTC = %q", got)
	}
	if got := ZoneName(time.FixedZone("", 2*3600), at); got != "+02:00" {
		t.Fatalf("a nameless zone = %q, want its offset", got)
	}
	t.Setenv("TZ", "America/New_York")
	if got := ZoneName(time.Local, at); got != "America/New_York" {
		t.Fatalf("time.Local with TZ set = %q", got)
	}
	t.Setenv("TZ", "")
	if got := ZoneName(time.Local, at); got == "Local" || got == "" {
		t.Fatalf("time.Local with no TZ = %q: the word Local reaches a client", got)
	}
}
