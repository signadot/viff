package versed

import (
	"errors"
	"testing"
	"time"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/schedule"
)

// A schedule is a record holding `next`; the daemon arms a timer at it and, when it fires,
// writes one commit that advances `next` and replaces the standing tick.

// writeSchedule stands a record up by hand, armed against `next`, with a tick already standing
// so the replacement has something to remove.
func writeSchedule(t *testing.T, store entity.Store, id, every string, next time.Time, standing string) {
	t.Helper()
	rec := entity.Obj{
		schedule.EveryField: every,
		schedule.NextField:  schedule.Key(next),
	}
	if standing != "" {
		rec[schedule.TickField] = entity.Obj{standing: entity.Obj{"due": standing}}
	}
	if _, _, err := store.Write(entity.Write{At: schedule.Ref(id).At(), Value: rec.Payload()}); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
}

// standingTicks waits until the record holds exactly the ticks named, and answers the record.
func standingTicks(t *testing.T, store entity.Store, id string, want ...string) entity.Payload {
	t.Helper()
	var rec entity.Payload
	for range 400 {
		ent, ok, err := store.Get(schedule.Ref(id))
		if err == nil && ok {
			rec = ent.Payload
			got := schedule.Standing(rec)
			if len(got) == len(want) {
				match := true
				for _, w := range want {
					if entity.Get(entity.Get(rec, schedule.TickField), w) == nil {
						match = false
					}
				}
				if match {
					return rec
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the record never held ticks %v: %v", want, rec)
	return nil
}

// A `next` that passed while the verse was down ticks on the way up: once, for the last
// instant due, saying how many were skipped, with the old tick gone and `next` ahead.
func TestAScheduleWhoseNextHasPassedTicksOnTheWayUp(t *testing.T) {
	srv := testServer(t)
	srv.Zone = time.UTC
	now := time.Now().UTC()
	armed := now.Truncate(time.Hour).Add(-2 * time.Hour) // three grid points have passed
	old := schedule.Key(armed.Add(-time.Hour))
	writeSchedule(t, srv.Store, "hourly", "1h", armed, old)

	if err := srv.armSchedules(); err != nil {
		t.Fatalf("armSchedules: %v", err)
	}
	due := now.Truncate(time.Hour)
	rec := standingTicks(t, srv.Store, "hourly", schedule.Key(due))
	tick := entity.Get(entity.Get(rec, schedule.TickField), schedule.Key(due))
	if got := entity.Get(tick, "missed"); got == nil || got.Int64 == nil || *got.Int64 != 2 {
		t.Fatalf("missed = %v, want 2: the two grid points before the one written", got)
	}
	if entity.Str(tick, "date") == "" || entity.Str(tick, "time") == "" || entity.Str(tick, "timezone") != "UTC" {
		t.Fatalf("the tick carries no calendar: %v", tick)
	}
	next, err := schedule.ParseKey(entity.Str(rec, schedule.NextField))
	if err != nil || !next.Equal(due.Add(time.Hour)) {
		t.Fatalf("next = %q, want %s", entity.Str(rec, schedule.NextField), schedule.Key(due.Add(time.Hour)))
	}
}

// Two timers armed against one `next` write one tick: the second finds `next` moved, is
// refused by its precondition, and writes nothing.
func TestTwoTimersWriteOneTick(t *testing.T) {
	srv := testServer(t)
	srv.Zone = time.UTC
	now := time.Now().UTC()
	armed := now.Truncate(time.Hour).Add(-time.Hour)
	writeSchedule(t, srv.Store, "hourly", "1h", armed, "")

	// Both timers read the record before either wrote: the same `next` in hand.
	ent, _, err := srv.Store.Get(schedule.Ref("hourly"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.tickFrom("hourly", ent.Payload, now); err != nil {
		t.Fatalf("the first tick: %v", err)
	}
	if err := srv.tickFrom("hourly", ent.Payload, now); err == nil {
		t.Fatal("the second timer's tick was not refused")
	} else if !errors.Is(err, errTickRefused) {
		t.Fatalf("the second timer's tick failed for another reason: %v", err)
	}
	// Once `next` is ahead, a timer that fires early writes nothing at all.
	if err := srv.tickAt("hourly", now); err != nil {
		t.Fatalf("a tick before its instant: %v", err)
	}
	standingTicks(t, srv.Store, "hourly", schedule.Key(now.Truncate(time.Hour)))
}

// Re-arming reads the record: a `next` moved by hand into the past is the next instant the
// timer fires at, and the tick it writes replaces the one standing.
func TestReArmingReadsTheRecord(t *testing.T) {
	srv := testServer(t)
	srv.Zone = time.UTC
	now := time.Now().UTC()
	first := now.Truncate(time.Hour).Add(-3 * time.Hour)
	writeSchedule(t, srv.Store, "hourly", "1h", first, "")
	srv.armSchedule("hourly")
	rec := standingTicks(t, srv.Store, "hourly", schedule.Key(now.Truncate(time.Hour)))

	// The verse is "down" past the next instant: move it into the past and re-arm.
	ahead, _ := schedule.ParseKey(entity.Str(rec, schedule.NextField))
	if _, _, err := srv.Store.Write(entity.Write{
		At: schedule.Ref("hourly").At(schedule.NextField), Value: entity.Val(schedule.Key(ahead.Add(-2 * time.Hour))),
	}); err != nil {
		t.Fatalf("backdating next: %v", err)
	}
	srv.armSchedule("hourly")
	// The same instant is due again (it is still the last grid point at or before now), so
	// the tick written carries that key — and only that key: the replacement is one write.
	// What moves is `next`, back ahead of now.
	for range 400 {
		ent, _, err := srv.Store.Get(schedule.Ref("hourly"))
		if err == nil {
			if next, _ := schedule.ParseKey(entity.Str(ent.Payload, schedule.NextField)); next.After(now) {
				standingTicks(t, srv.Store, "hourly", schedule.Key(now.Truncate(time.Hour)))
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the re-armed timer never ticked: next stayed in the past")
}

// A backfill writes the tick for a past instant beside the standing one, create-only, and
// refuses an instant that is off the grid or ahead.
func TestABackfillIsTheSameTickTheScheduleWouldHaveWritten(t *testing.T) {
	srv := testServer(t)
	srv.Zone = time.UTC
	now := time.Now().UTC()
	standing := schedule.Key(now.Truncate(time.Hour))
	writeSchedule(t, srv.Store, "hourly", "1h", now.Truncate(time.Hour).Add(time.Hour), standing)

	past := now.Truncate(time.Hour).Add(-5 * time.Hour)
	if err := srv.backfill("hourly", past); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := srv.backfill("hourly", past); err != nil {
		t.Fatalf("a second backfill of one instant is one write, not an error: %v", err)
	}
	standingTicks(t, srv.Store, "hourly", standing, schedule.Key(past))
	if err := srv.backfill("hourly", past.Add(7*time.Minute)); err == nil {
		t.Fatal("an instant off the grid was accepted")
	}
	if err := srv.backfill("hourly", now.Add(2*time.Hour)); err == nil {
		t.Fatal("an instant ahead was accepted")
	}
	// The next tick removes every tick older than the one it writes, the backfill included.
	if err := srv.tickAt("hourly", now.Add(time.Hour)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	standingTicks(t, srv.Store, "hourly", schedule.Key(now.Truncate(time.Hour).Add(time.Hour)))
}
