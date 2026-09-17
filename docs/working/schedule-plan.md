# Schedules: a tick is a commit, and the calendar is derived from it

Status: **planned, nothing built.** Branch `worktree-schedules`. The design is the discussion on
git issue `9e9nnjbeh12ksgqan1n0`, entries of 2026-09-15 to 2026-09-17; this plan is that design
against the code, and it changes two spellings the issue settled, both because of what the code
already is (§2 and §7 below). Read the issue first; this says where each piece lands and what
pins it.

## The model, in one paragraph

A schedule is a record in verse's own half. The daemon arms a timer at its `next` and, when it
fires, writes one commit that advances `next` and replaces the schedule's standing tick with the
one now due. A tick is an entity whose address is the schedule and the due instant; nobody but
the schedule writes it, and it stands until the next replaces it. A rule takes the tick as its
subject and fires once per tick with no latch, because only replacements touch the place. The
base charter declares the standard schedules and derives the calendar from the hourly tick, so a
rule whose `on` names a calendar fact is a cron line. Nothing is asked of logd or docd.

## 1. The record: `verse.schedule.<id>`

```tony
every: 1h
from: 2026-09-18T02:00:00Z        # phase; ticks stand at from + n*every
next: 2026-09-18T03:00:00Z        # the instant the daemon is armed against, stamped once
tick:
  2026-09-18T02:00:00Z: {due: …, missed: 0, date: …, time: …, …}   # the standing tick
```

The id is `mirror.RecordID(at, name)`: `hourly` for baseline, `verse-dev.drift:hourly` for a
package, exactly as a rule's record (`mirror.RuleRef`) and a stratum's (`trigger.StrataRefAt`).
The record is an owner record whose owner is the installer, so its ticks are written
`via:verse.schedule.<id>` with the installer's grant, and perms answer reads as they do for
every other verse record (`docs/perms.md`, the table near line 296: owner writes, everyone
reads).

Three names under `verse.schedule` are reserved for the calendar (§7): `date`, `time` and
`timezone`, plus `year`, `month`, `day`, `weekday`. A schedule may not take one; install
refuses it by name.

**Where:** a new package `schedule/` (record shape, ref helpers, `Next(from, every, now)`), so
neither `mirror` nor `trigger` grows a clock. `mirror` already imports nothing of it and
`versed` imports both.

## 2. The tick lives under its record, so the replacement is one write

The issue put ticks at `verse.schedule.tick.<id>.<due>`. `entity.Store.Write` takes one `At`
(`entity/write.go`, `type Write`), and a commit is one write, so replacing tick T with T+1
while advancing `next` under a precondition on `next` is one commit only if all three are
under one address. They are, once the tick is under the record: one `Write` at
`verse.schedule.<id>` with

```
Value: {next: <T+2>, tick: {<T>: !delete null, <T+1>: {due: <T+1>, missed: <k>, …}}}
When:  [{At: <record>.next, Pattern: Unchanged(<T+1>)}]
```

A patch merges, so the write touches `next` and the two tick keys and nothing else the
record holds, and `!delete` at a key is the removal the lifecycle doc names. The precondition
is on one small path, which is the write-cost rule (`Write cost follows the precondition path`).
A failed precondition is another timer having ticked; the daemon re-reads `next` and re-arms.

A rule reads `verse.schedule.hourly.tick.*`. Check: `checkLiteralDerive` and `mayWrite` say
nothing about reads, and `trigger/match_test.go:219` already matches `verse.gate.question.*`,
so a subject in verse's half is ordinary.

The Go spelling is `entity.Removed()`: a null wearing `!delete`, which nests at a key inside a
write's value and executes as the key's removal. `entity/attribution_test.go:46` writes a perms
record that way, `rec[entity.GrantsField] = entity.Removed()`, and `entity.Obj.Payload` passes a
node through unchanged. The one caveat is `Modify`, which escapes its value with `AsData` before
adding its own removals, so the replacement is a plain `Store.Write`, not a `Modify`.

## 3. The timer, and the phase that arms it

Mirrors `armDeadline` / `armDeadlines` / `settleByDeadline` (`versed/server.go`, near line
2700) line for line:

- `armSchedule(store, id)`: read the record, `time.AfterFunc(until(next), fire)`.
- `fire(id)`: read the record again; compute the first `from + n*every` strictly after now;
  `missed` is the number of whole periods between the old `next` and now; write the
  replacement (§2) under the precondition; on a refused precondition re-read and re-arm; on
  success re-arm at the new `next`. A record that is gone (uninstalled) arms nothing.
- `phaseSchedules` after `phaseCharter` and `phasePackages` beside `phaseDeadlines`
  (`versed/startup.go`): list `verse.schedule.*` skipping the reserved calendar names, arm each.
  `entity.IsUnread` on the listing means ask again, as every phase does.

A `next` in the past at arming fires at once with `missed` set. That is the anacron reading,
and the default.

The stamp: `next` is written once, under `Absent` on `<record>.next`, as
`mirror.StampDeadline` does, so a re-install or a restart arms against the standing instant.

## 4. Declaring a schedule in a charter

The head document of a charter file is the strata list (`docs/examples/level-ping-pong.yaml`);
a schedule joins it:

```tony
- name: hourly
  every: 1h
- name: nightly
  every: 24h
  from: 2026-09-18T02:00:00Z
```

An entry with `every` is a schedule, one with `level`/`match` a stratum; `charterDocs`
(`versed/charter.go:423`) already splits the file, and the install path that persists strata
(`trigger/stratum.go`, `LoadStrata` near line 689 and the write near 671) is the shape for
persisting schedules: create-or-update `every`/`from`, stamp `next`, arm. Uninstall removes the
record and the timer. A schedule in a package carries the package's address in its id, as a
rule does.

No cron expression. `every` + `from` covers hourly, nightly at a fixed time and weekly on a
day; the calendar (§7) covers "at nine". A cron field would be a second way to compute `next`
and changes nothing below it, so it waits for a charter that needs it.

## 5. The rule: a tick is a subject, and it needs no latch

```tony
- name: drift-report
  condition:
    subject: {path: "verse.schedule.hourly.tick.*"}
  action:
    spawn: {task: "refresh the drift report for the hour ending $subject.name", …}
    onFailed: {derive: [{path: "verse-dev.drift.runs", id: $subject.name, value: {failed: true}}]}
```

Two changes in `trigger`, both scoped to a subject under `verse.schedule.*.tick`:

- **The latch lint** (`trigger/deselect.go`, `latches`; the refusal for a spawn that cannot
  stop spawning is raised where `AddTrigger` calls it) admits a spawn about a tick subject: the
  system consumes it by replacement, so the rule has an end state the lint cannot see.
- **Once-ever spawn dedup** (`trigger/engine.go`, `spawnHooked`, the live check near line
  1310): for a tick subject, a task record standing at `mirror.Ref(id)` — live or done — means
  the spawn already happened; skip it with a trace line. A trigger fires on what already
  matches when it opens, so without this a restart after a finished run within the period runs
  it again. A tick's subject never recurs, so once-ever is the right meaning there, and the
  general rule for world subjects is untouched.

Hooks cannot patch the tick (perms refuse it: the schedule is the writer), and a hook reads
`$subject.value` at the firing's commit (`applyHook`), so `due` and the calendar resolve in a
hook after the tick was replaced. A gate denied on T stands until T+1 replaces it and is then
withdrawn as moot (the firing watches its subject), so no `onDenied` is needed.

## 6. The base charter is embedded in versed

The base charter ships inside the binary, `go:embed` of `versed/system.tony`, and
`installSystemCharter` (`versed/server.go:3737`) installs it at every start as
`entity.SystemCharter`. `verse up` therefore has the standard schedules and the calendar with
no file and no config. `config.Config.SystemCharter` stays what it is, a deployment's own
additions installed after the embedded one, and nothing in `deploy/` needs to name anything.
The embedded charter holds:

- the standard schedules: `hourly` (from the top of the hour), `daily` (from 00:00 in the
  configured zone), `weekly` (from Monday 00:00);
- the calendar rule (§7).

The system charter is baseline, so its derives are unconfined (`mayWrite`), and
`validateDerive` refuses only a suspension under verse's half.

## 7. The calendar: derived facts, as objects

The tick carries the calendar of its due instant in the deployment's zone
(`schedule.timezone` in `config`, UTC when unset): `date`, `time`, `year`, `month`, `day`,
`weekday`, `timezone`. The daemon computes; a charter cannot.

A derive must be an object (`checkLiteralDerive`, `trigger/stratum.go:1560`: the engine writes
`verse.meta.derivedFrom` into it), so each fact is a one-field object, not the scalar the issue
wrote:

```tony
- name: calendar
  condition:
    subject: {path: "verse.schedule.hourly.tick.*"}
  action:
    derive:
    - {path: "verse.schedule", id: date,     value: {date: $subject.value.date}}
    - {path: "verse.schedule", id: time,     value: {time: $subject.value.time}}
    - {path: "verse.schedule", id: year,     value: {year: $subject.value.year}}
    - {path: "verse.schedule", id: month,    value: {month: $subject.value.month}}
    - {path: "verse.schedule", id: day,      value: {day: $subject.value.day}}
    - {path: "verse.schedule", id: weekday,  value: {weekday: $subject.value.weekday}}
    - {path: "verse.schedule", id: timezone, value: {timezone: $subject.value.timezone}}
```

Same bytes are a no-op the store ends, so `verse.schedule.date` commits once a day and a rule
reading it wakes once a day. The cron line is

```tony
    on: {path: "verse.schedule.time", value: {time: "09:00"}}
```

which rises once a day. Minute resolution is not in the base: a `minutely` schedule is 1440
ticks and 1440 derives a day, and a deployment that wants it declares it and pays.

Downtime has two honest readings and the rule picks by what it reads: a verse down 08:30 to
10:30 writes the 10:00 tick with `missed: 2`, so `time` goes `08:00` to `10:00` and a calendar
rule for `09:00` is skipped (strict cron), while a rule on the tick fires late once with
`missed` (anacron).

## 8. The CLI and the route

- `verse schedule list`: each record with `every`, `from`, `next` and the standing tick's `due`.
- `verse schedule tick <name> --due <instant>`: a backfill. The daemon writes the tick for
  that instant beside the standing one, create-only, as the schedule, for a caller the owner
  record admits; the rules fire for it; the standing tick's re-selection at that commit is
  absorbed by the once-ever dedup; the next replacement removes every tick older than the one
  it writes.

`cmd/verse/flow.go` (`flowCmd`, `cli.Command`) is the shape; the route joins the mux in
`versed/server.go` beside the flow routes.

## Order of work, each step green on `make check` before the next

1. `schedule/`: record, refs, `Next`, calendar fields; unit tests on `Next` across a DST
   change in the configured zone and on `missed`.
2. `versed`: the replacement write, the timer, `phaseSchedules` (§3). Tests in
   `versed/deadline_test.go`'s shape: a `next` in the past at start ticks once with `missed`;
   two armings agree on one tick (precondition); a restart re-arms against the stamped `next`.
3. `trigger`: the lint admission and the once-ever dedup (§5). Tests: two rules on one
   schedule each fire once per tick; a restart after a finished run does not re-run it; a gate
   denied on T is withdrawn at T+1.
4. Charter grammar for a schedule beside the strata (§4), persisted and uninstalled with the
   charter; a package's schedule carries its address.
5. The embedded `versed/system.tony` with the standard schedules and the calendar rule (§6,
   §7), installed at every start; a test that `verse.schedule.date` commits once across 24
   hourly ticks, and that `on` `time: "09:00"` fires once a day.
6. CLI and route (§8).
7. Docs: a section in `docs/trigger-model.md` under "Time as a cause" replacing the
   `firing-residual.md` §1 entry, `docs/reference/entities.md` for the new records, and the
   tony-format doc ask filed as its own issue there.

Steps 1 to 3 are the mechanism and are testable with a schedule record written by hand; 4 to
6 are the surface. A staging ship needs no wipe: every record here is new.

## What this does not do

- No heartbeat, no fixed frequency anywhere: a commit exists only where a schedule owed one.
- No per-period backfill by default; `missed` carries the count, and one tick per missed
  instant is a later spelling for a charter that needs it.
- No cron grammar (§4), no minute resolution in the base (§7).
- Nothing in logd or docd. The precondition write and the commit sequence are already there.
- The patch-before-spawn looseness (`epst4v76`) is not fixed here; a tick rule has no patch, so
  it does not meet it.
