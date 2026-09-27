# Schedules

This page holds schedules, the records that make time a cause that a rule can fire on. It
says what a schedule writes, how a rule reads it, and how a charter declares its own. The
model of a rule is in [the trigger model](../trigger-model.md).

## Schedules: time as a cause

A rule fires on a commit, so for time to be a cause it has to be written down. A **schedule**
is a record in verse's own half, `verse.schedule.<name>`, holding its period, its phase and
`next`, the instant the daemon is armed against. When the timer fires, the daemon writes one
commit as the schedule: `next` advances to the next point on the grid, and the schedule's
standing **tick** is replaced by the one now due, at an address that is the schedule and the
instant, `verse.schedule.hourly.tick.2026-09-18T03:00:00Z`. Nothing else ever writes there.

A rule takes the tick as its subject:

```tony
- name: drift-report
  condition:
    subject: {path: "verse.schedule.hourly.tick.*"}
  action:
    spawn: {driver: scripted, prompt: "refresh the drift report for the hour ending $subject.name"}
```

It needs no latch, and may not write one: the tick is consumed by the store, since only a
replacement touches the place, so the rule fires once per tick, and every rule reading the
schedule does. A spawn about a tick is once-ever rather than only while live — a tick's address
never recurs, so a task record standing for the id is the spawn having happened, and a restart
re-firing the standing tick starts nothing. A gate denied on one tick stands until the next
replaces it, and is then withdrawn as moot. A completion hook records its outcome by `derive`
into the charter's own place; it cannot patch the tick, which is the schedule's.

The tick carries `due`, `missed` (how many instants before it went unticked, so a verse that
was down ticks once on the way up and says how late), and the calendar of its due instant in
the daemon's zone (`TZ`): `date`, `time`, `year`, `month`, `day`, `weekday`, `timezone`.

**The calendar is the cron line.** Every verse carries the base charter: four schedules,
`minutely`, `hourly`, `daily` and `weekly`, phased on a Monday at midnight in the zone, and
two rules deriving one entity per calendar fact — `verse.schedule.time`, to the minute, from
the minutely tick, and `.date`, `.weekday` and the rest from the hourly one. The same date is
the same bytes and a no-op, so `date` commits once a day and a rule reading it wakes once a
day, while `time` costs two commits a minute, the tick and the derive, that wake nothing unless
a charter reads there. A rule whose `on` names a fact rises when it becomes true:

```tony
    on: {path: "verse.schedule.time", value: {time: "09:05"}}      # daily at five past nine
```

Two readings of downtime, and a rule says which it means by what it reads. A verse down from
08:30 to 10:30 writes the 10:00 hourly tick with `missed: 1` and the 10:30 minutely tick with
`missed: 119`; `time` goes from `08:30` to `10:30` and `09:05` never rises. A rule on the
calendar is strict cron: a missed instant is skipped. A rule on the tick is anacron: it fires
late, once, and `missed` says by how much.

A charter declares a schedule of its own in its head document, beside the strata, and it is
the installer's — its ticks are written with the installer's grant, and removed with the
charter:

```tony
- name: nightly
  every: 24h
  from: 2026-01-01T02:00:00Z     # the phase: ticks stand at from + n*every; default is Monday 00:00
```

A period of whole days steps by calendar days, so a nightly schedule keeps its wall-clock time
across a DST change. `verse schedule` lists every schedule with its `next` and its standing
tick; `verse schedule tick <name> --due <instant>` backfills one, writing the same entity the
schedule would have, so a rule about it fires as it would have.
