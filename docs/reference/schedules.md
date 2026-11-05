# Schedules

This page describes schedules: the records that make time a cause that a
[rule](../glossary.md#rule) can fire on. It says what a schedule writes, how a rule reads it,
and how a [charter](../glossary.md#charter) declares its own.
[The charter reference](../trigger-model.md) is the model of a rule.

## Schedules: time as a cause

A rule fires on a commit. So for time to be a cause, somebody must write it down.

A **[schedule](../glossary.md#schedule)** is a record in
[verse's own half](../glossary.md#verses-own-half), `verse.schedule.<name>`. It holds its
period, its phase and `next`. `next` is the instant that the daemon is armed against.

When the timer fires, the daemon writes one commit, and it writes as the schedule. The commit
does two things:

- `next` advances to the next point on the grid.
- The tick that is due replaces the standing **[tick](../glossary.md#tick)** of the schedule.

The address of a tick is the schedule and the instant:
`verse.schedule.hourly.tick.2026-09-18T03:00:00Z`. Nothing else ever writes there.

A rule takes the tick as its subject. Here a team that keeps its code in `acme/app` has an agent
post, each hour, what landed on `main` in the hour before:

```tony
- name: hourly-summary
  condition:
    subject: {path: "verse.schedule.hourly.tick.*"}
  action:
    spawn:
      agent: {name: verse}
      prompt: "post to #deploys a summary of what landed on main in the hour before $[subject.name]"
      environment:
        systems:
          network: {mode: open}
          git: {repo: acme/app, fetch: {}}
          slack: {post: {channel: deploys}}
      autonomy:
      - then: allow
```

`subject.name` is the tick's last segment, the instant it is due.

A rule about a tick needs no [latch](../glossary.md#latch), and may not write one. Each tick has
an address of its own, and only the next tick replaces it. So the rule fires once for each tick,
and so does every rule that reads the schedule.

| Part of the rule | How it behaves about a tick |
|---|---|
| a spawn | It is once-ever, and not only while live. The address of a tick never recurs. So a task record that stands for the id means that the spawn happened, and a restart that fires the standing tick again starts nothing. |
| a gate that was denied | It stands on one tick until the next tick replaces that one. Then verse withdraws it as moot. |
| a completion hook | It records its outcome by `derive` into the charter's own place. It cannot patch the tick, because the tick belongs to the schedule. |

The tick carries these fields.

| Field | What it holds |
|---|---|
| `due` | the instant at which the tick is due |
| `missed` | how many instants before it went unticked. A verse that was down ticks once when it comes up, and `missed` says how late. |
| `date`, `time`, `year`, `month`, `day`, `weekday`, `timezone` | the calendar of the due instant, in the daemon's zone (`TZ`) |

**The calendar takes the place of a cron line.** Every verse installs a charter of its own when it
starts ([internal/versed/system.tony](../../internal/versed/system.tony)). It has four schedules and two rules.

The four schedules are `minutely`, `hourly`, `daily` and `weekly`. Their phase is a Monday at
midnight in the zone.

The two rules derive one record for each calendar fact.

| The rule reads | The rule derives |
|---|---|
| the minutely tick | `verse.schedule.time`, to the minute |
| the hourly tick | `.date`, `.weekday` and the rest |

A derive of the same date writes the same bytes, and it is a no-op. So `date` commits once a
day, and a rule that reads it wakes once a day. `time` costs two commits a minute: the tick
and the derive. Those commits wake nothing unless a charter reads there.

A rule whose `on` names a calendar fact rises when the fact becomes true:

```tony
    on:                        # daily at five past nine
      path: "verse.schedule.time"
      value: {time: "09:05"}
```

**Downtime has two readings.** A rule says which reading it means by what it reads.

Take a verse that is down from 08:30 to 10:30. It writes the 10:00 hourly tick with
`missed: 1`, and the 10:30 minutely tick with `missed: 119`. `time` goes from `08:30` to
`10:30`, and `09:05` never rises.

| A rule on | Behaves as | What it does about a missed instant |
|---|---|---|
| the calendar | strict cron | It skips the instant. |
| the tick | anacron | It fires late, once, and `missed` says by how much. |

**A charter declares a schedule of its own** in its `schedules:`, beside its `strata:`. The
schedule belongs to the installer. Verse writes its ticks with the grant of the installer, and
removes them with the charter. This is the start of the charter that proposes a next step for
each issue of the verse repository ([issue-next.tony](../../.verse/charters/issue-next.tony)):

```tony
at: repos.verse.git-issue
reads: [git-issue, verse.schedule]
schedules:
- name: sweep
  every: 3h
  # from: 2026-01-01T02:00:00Z   the phase: ticks stand at from + n*every; default is Monday 00:00
```

A package's schedule carries its address, as a package's rule does:
`verse.schedule."repos.verse.git-issue:sweep"`.

A period of whole days steps by calendar days. So a nightly schedule keeps its wall-clock time
across a DST change.

| Command | What it does |
|---|---|
| `verse schedule` | lists every schedule, with its `next` and its standing tick |
| `verse schedule tick <name> --due <instant> [--at <address>]` | writes one tick for a past instant. It writes the same data that the schedule writes, so a rule about the tick fires in the same way. `--at` names the package of a schedule a charter declared. |
