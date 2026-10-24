# schedule: a backfill of the phase instant itself is refused as off the grid

`schedule.Next(from, every, after)` is documented as "the first grid point strictly after `after`", but when `after == from` it returns `from`, which is not after it. `Due` is `Next` stepped back one period, so `Due(from, every, from)` is `from - every`.

The one place this shows: `backfill` (versed/schedule.go) checks `Due(from, every, due) == due`. For `due == from` that check fails and the backfill is refused with "is not on the schedule's grid; the nearest before it is <from - every>". The nearest-before it names is also before the grid starts.

Found while writing versed/stalereview_test.go: `from: "2026-10-19T09:00:00Z"` with `every: 24h` refused `verse schedule tick --due 2026-10-19T09:00:00Z`. The charter moved its phase earlier, so nothing waits on this.

Size: only the single instant `from` is affected. A timer firing exactly at `from` would also compute a due one period early. Every later grid point is fine.

Likely fix: in `Next`, return `from` only when `after.Before(from)`, and step from there when equal. A row in the schedule package's Next/Due tests covers it.