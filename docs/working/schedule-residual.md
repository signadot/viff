# Schedules: what is left

Status: residual of a landed feature. Schedules, ticks, the base charter and the calendar are
built and documented in [the trigger model](../trigger-model.md#schedules-time-as-a-cause); the
design is the discussion on git issue `9e9nnjbeh12ksgqan1n0`. What is here is what that design
named and the landing did not build, and what the building found, so the next person does not
reconstruct why each was left.

## Not built, by decision

- **Minute resolution.** The base charter's finest schedule is hourly, so `verse.schedule.time`
  is `HH:00`. A `minutely` schedule is 1440 ticks and 1440 derives a day; a deployment that wants
  it declares one and pays for it. The doc says the price.
- **Per-period backfill.** A verse down across several periods ticks once on the way up, with
  `missed` saying how many instants went unticked. One tick per missed instant, capped
  (`catchUp: {each: N}` on the declaration), is the other spelling and waits for a charter that
  owes work per period. A person backfills one instant with `verse schedule tick`.
- **A cron expression.** `every` + `from` covers hourly, nightly at a fixed time and weekly on a
  day, and the calendar covers "at nine". An `at:` field would be a second way to compute
  `next` and changes nothing else.
- **A scope's schedule.** Refused at install: a schedule ticks for the verse. A scope reads the
  baseline's ticks through its overlay.

## Found while building

- **The base charter's strata occupy two baseline levels**, 100000 (`ticks`) and 100010
  (`calendar`), because a derive needs a level for its subject and a strictly higher one for
  what it writes. A baseline charter of your own declaring either level is refused with the
  level's own words. A better answer is a stratum the system owns that no charter's level can
  collide with; none is designed.
- **A literal `id` on a derive** was admitted for the calendar: one entity for every subject the
  rule fires about, its linkage recording the rule and not the subject. It is general, and the
  doc for derives does not mention it yet.
- **The system phase waits for the restore.** Its comment said it installed over what stood;
  it ran before a restore still being asked again had answered. Fixed here; noted because the
  same shape may exist elsewhere in Start.
- **A schedule's owner record outlives the schedule.** `removeSchedule` deletes the record
  under `verse.schedule`; the perms mirror's row for it is not swept. Harmless until the name
  is declared again by somebody else, who is then refused as if the first installer still held
  it. The perms sweep (`611hz249h12krtm2mnn0`) is the place to fold this into.
- **The grid follows the daemon's zone.** `from` defaults to a Monday at midnight in `TZ`, and
  `next` is re-stamped only when the declaration's text changes, so a deployment that changes
  its zone keeps the old instant until the next tick, which then lands on the new grid.
- **`verse c` does not list schedules**, and `DELETE /charter?path=` does not reach one;
  `verse schedule` lists them and the remove-by-name path takes one out. Folding schedules into
  the charter tree is the natural next step once somebody reads the two side by side.
- **The patch-before-spawn looseness** (`epst4v76h12kr6jygsn0`) is untouched; a tick rule has
  no patch, so it does not meet it.

## Owed elsewhere

- A page under `docs/docd` or `docs/logd` in tony-format stating the rules for a scheduled
  writer against the substrate — a tick is a commit rather than served state, the caller
  carries the time, `next` advances under a precondition in the same commit as the write, a
  mount serves what is asked for rather than what is reacted to — with retention as the worked
  example. Named on the issue; not filed on that repository yet.
