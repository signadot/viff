// Package schedule is what a schedule is in the store: a record in verse's own half that holds
// its period, its phase, the instant the daemon is armed against, and its standing tick.
//
// A tick is a commit. Nothing about a schedule lives only in a process: the daemon reads the
// record to arm a timer, and when the timer fires it writes one commit that advances `next` and
// replaces the standing tick with the one now due, under a precondition on `next` so two timers
// write one tick. A rule takes the tick as its subject and fires once per tick, because only a
// replacement ever touches the place. The design is git issue 9e9nnjbeh12ksgqan1n0; the plan is
package schedule

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/mirror"
)

// System is where the records live: verse.schedule.<id>. The id is mirror.RecordID(at, name),
// so a package's schedule carries the package's address, as its rules and levels do.
const System = "schedule"

// The record's fields.
const (
	EveryField = "every"
	FromField  = "from"
	NextField  = "next"
	TickField  = "tick"
	// AtField is the package address a schedule was declared in, "" for baseline; ByField its
	// installer. The same names a rule's record carries.
	AtField = "at"
	ByField = "by"
)

// Calendar is the names reserved under verse.schedule for the calendar the base charter derives
// from the hourly tick. A schedule may not take one of them.
var Calendar = []string{"date", "time", "year", "month", "day", "weekday", "timezone"}

// IsCalendarName reports whether a name is one the calendar holds.
func IsCalendarName(name string) bool {
	for _, c := range Calendar {
		if c == name {
			return true
		}
	}
	return false
}

// Ref addresses one schedule's record.
func Ref(id string) entity.Ref { return entity.VerseRef(System, id) }

// TickRef addresses one tick: the schedule and the instant it was due.
func TickRef(id, due string) entity.Ref { return entity.VerseRef(System, id, TickField, due) }

// IsRecord reports whether an address is a schedule's record — a child of verse.schedule that is
// not a calendar fact.
func IsRecord(r entity.Ref) bool {
	return r.ChildOf(entity.VerseRoot, System) && !IsCalendarName(r.Name())
}

// IsTickSubject reports whether a subject's segments name ticks: verse.schedule.<id>.tick.<due>,
// with the id and the instant literal or `*`. It is what the latch lint and the spawn dedup ask,
// since a rule about a tick has an end state the store gives it and an id that never recurs.
func IsTickSubject(segs []string) bool {
	return len(segs) == 5 && segs[0] == entity.VerseRoot && segs[1] == System && segs[3] == TickField
}

// Schedule is a declaration: what a charter's head document says beside the strata.
type Schedule struct {
	Name string `tony:"field=name"`
	// Every is the period, a Go duration: "1h", "24h", "168h".
	Every string `tony:"field=every"`
	// From is the phase: ticks stand at from + n*every. RFC3339, optional; empty is Epoch.
	From string `tony:"field=from,omitzero"`
}

// Epoch is the phase a schedule declares none against: a Monday at midnight in the daemon's
// zone, so the standard periods align — hourly on the hour, daily at midnight, weekly on Monday.
func Epoch(loc *time.Location) time.Time {
	return time.Date(1970, time.January, 5, 0, 0, 0, 0, loc)
}

// Validate refuses a declaration that cannot tick: a name the calendar holds or a record cannot
// carry, a period that does not parse or is not positive, a phase that is not an instant.
func (s Schedule) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("a schedule needs a name")
	}
	if strings.ContainsAny(s.Name, ":@.") {
		return fmt.Errorf("schedule %q: a name may not contain ':', '@' or '.'", s.Name)
	}
	if IsCalendarName(s.Name) {
		return fmt.Errorf("schedule %q: the name is the calendar's, derived under verse.schedule "+
			"from the hourly tick; pick another", s.Name)
	}
	if _, err := s.Period(); err != nil {
		return err
	}
	if s.From != "" {
		if _, err := time.Parse(time.RFC3339, s.From); err != nil {
			return fmt.Errorf("schedule %q: from %q is not an RFC3339 instant: %w", s.Name, s.From, err)
		}
	}
	return nil
}

// Period is the declared period as a duration.
func (s Schedule) Period() (time.Duration, error) {
	d, err := time.ParseDuration(s.Every)
	if err != nil {
		return 0, fmt.Errorf("schedule %q: every %q is not a duration: %w", s.Name, s.Every, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("schedule %q: every %q is not positive", s.Name, s.Every)
	}
	return d, nil
}

// Phase is the declared phase as an instant in loc, Epoch when none was declared.
func (s Schedule) Phase(loc *time.Location) (time.Time, error) {
	if s.From == "" {
		return Epoch(loc), nil
	}
	t, err := time.Parse(time.RFC3339, s.From)
	if err != nil {
		return time.Time{}, err
	}
	return t.In(loc), nil
}

// Key is how an instant is spelled as a tick's name and a record's `next`: RFC3339 in UTC with
// no fraction, which is a legal segment and sorts.
func Key(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// ParseKey reads an instant a Key wrote, or any RFC3339 instant.
func ParseKey(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// wholeDays is the period as a count of calendar days, 0 when it is not one.
func wholeDays(every time.Duration) int {
	if every >= 24*time.Hour && every%(24*time.Hour) == 0 {
		return int(every / (24 * time.Hour))
	}
	return 0
}

// Step is the grid point n periods from t. A PERIOD OF WHOLE DAYS STEPS BY CALENDAR DAYS in t's
// zone, so a daily schedule ticks at the same wall-clock time across a DST change rather than
// drifting by the hour the clocks moved; anything else is a fixed duration.
func Step(t time.Time, every time.Duration, n int) time.Time {
	if days := wholeDays(every); days > 0 {
		return t.AddDate(0, 0, n*days)
	}
	return t.Add(time.Duration(n) * every)
}

// Next is the first grid point strictly after `after`: from + n*every.
func Next(from time.Time, every time.Duration, after time.Time) time.Time {
	if !after.After(from) {
		return from
	}
	// A first guess by division, then walk: a day is 23 or 25 hours across a change.
	n := int(after.Sub(from) / every)
	t := Step(from, every, n)
	for !t.After(after) {
		t = Step(t, every, 1)
	}
	for prev := Step(t, every, -1); prev.After(after); prev = Step(t, every, -1) {
		t = prev
	}
	return t
}

// Due is the last grid point at or before now: the tick a timer firing at now writes.
func Due(from time.Time, every time.Duration, now time.Time) time.Time {
	return Step(Next(from, every, now), every, -1)
}

// Missed is how many grid points between the instant a timer was armed against and the one it
// writes went unticked: the points in [armed, due). 0 for a tick on time.
func Missed(armed, due time.Time, every time.Duration) int64 {
	var n int64
	for t := armed; t.Before(due); t = Step(t, every, 1) {
		n++
	}
	return n
}

// Tick is a tick's payload: when it was due, how late it is in periods, and the calendar of the
// due instant in loc, so a charter can project the calendar without computing it.
func Tick(due time.Time, missed int64, loc *time.Location) entity.Payload {
	local := due.In(loc)
	return entity.Obj{
		"due":      Key(due),
		"missed":   missed,
		"date":     local.Format("2006-01-02"),
		"time":     local.Format("15:04"),
		"year":     local.Year(),
		"month":    int(local.Month()),
		"day":      local.Day(),
		"weekday":  local.Weekday().String()[:3],
		"timezone": ZoneName(loc, due),
	}.Payload()
}

// ZoneName is the zone as a client can read it: an IANA name. time.Local prints as the word
// "Local", which says nothing to anybody but this process, so the name is recovered from where
// the process got the zone — TZ when it is set, else the zoneinfo file /etc/localtime points
// at — and, when neither names it, the offset in force at the instant, which is at least true.
func ZoneName(loc *time.Location, at time.Time) string {
	name := loc.String()
	if name != "" && name != "Local" {
		return name
	}
	if name == "Local" {
		if tz := os.Getenv("TZ"); tz != "" {
			return strings.TrimPrefix(tz, ":")
		}
		if target, err := os.Readlink("/etc/localtime"); err == nil {
			if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
				return target[i+len("zoneinfo/"):]
			}
		}
	}
	if _, off := at.In(loc).Zone(); off == 0 {
		return "UTC"
	}
	return at.In(loc).Format("-07:00")
}

// RecordID is the id a declaration's record carries: the name for baseline, `<at>:<name>` for a
// package's. mirror.RecordID, so a schedule and a rule of one package agree.
func RecordID(at, name string) string { return mirror.RecordID(at, name) }

// NameOf is the declared name inside a record id: the tail after a package's `<at>:`.
func NameOf(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Standing reads the ticks a record holds, by their keys.
func Standing(rec entity.Payload) []string {
	var out []string
	for k := range entity.Fields(entity.Get(rec, TickField)) {
		out = append(out, k)
	}
	return out
}
