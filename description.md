# io: a sink is written by any package and read by no charter

> **The design is in the comment "Design: declared outboxes" (2026-10-01,
> `discussion/20261001T170356Z-e10f55b8.md`).** A package declares outboxes, which are places
> inside its own address that an io reads. That design supersedes the sink proposal below, which
> is kept as the option considered and not taken.

Status: proposal, nothing here runs.

## What is wrong

A package cannot ask an io to do anything. A charter installed at an address may only write
inside it (`trigger/confine.go`), and an io's out half only reads places inside its own claim
(`CheckClaimed`, `io/registration.go`) — `slack.example.out` for the Slack io. The two never
meet, so every rule that asks Slack to say something is baseline's:

- `gate-ask.tony` — a DM per person a gate waits on;
- `deploy-announce.tony` — a channel message per deploy tag;
- the daily git-issue digest, drafted and waiting on this.

Baseline costs them: they are not under `verse c acme`, they cannot be suspended or removed
with the drive they belong to, and they share baseline's one level ordering — `deploy-announce`
was refused twice on install, once for a level `gate-ask` holds and once for overlapping its
`gate-asked` stratum, which covers the whole out place.

It is not a Slack problem. Any package that wants something said, filed or posted by any io's out
half hits the same wall.

## The proposal: a sink is written by anyone and read by no charter

An io's out places are SINKS. Confinement exists so that nobody else's state MOVES: two packages
are isolated when neither can write where the other reads. A place no charter may read cannot
move anybody's state, so letting every package write there costs isolation nothing.

1. **Declared by the registration.** A sink is an out place an io's registration already names
   (`out.places`). No new declaration, and nothing becomes a sink that no io reads.
2. **No charter reads one.** Refused at install when any subject, condition read (`!get-path`,
   `!list-paths`, `on`/`when`) or stratum match falls in a sink. The check is the containment
   `checkLevels` already asks, in the other direction.
3. **Every package may write one.** A derive or patch whose target is in a sink is exempt from
   confinement.
4. **And from the level check.** A derive's target must sit in a stratum strictly above its
   subject's, which is the only reason `gate-asked` exists. A sink is terminal — nothing reads it,
   so nothing downstream can feed back — so it needs no level; it is above everything.
5. **Prefixed by the writer's address.** A package's writes land under its own address inside the
   sink: `slack.example.out."acme.announce".…`. Nobody in verse reads a sink but the world
   does, and without the prefix two packages writing the same input decide by last-writer what
   gets said — one package moving another's effect. The prefix makes that impossible and says in
   the tree who asked for each message. The out half already walks to any depth for an input
   carrying `to` (`io/out.go`, walk), so it needs no change to find them.

## What it costs

- **A package cannot read its own sends.** No `~status`, so a rule cannot react to a failed post.
  Failure is visible to people — `verse data`, the io's health on `verse io` — and not to rules.
  That is the trade a sink makes.
- **The latch-in-the-message idiom goes.** `deploy-announce.tony` reads
  `slack.example.out.deploy.*` to know what it has announced, and the digest draft renders with
  its subject in the sink (fill a structure, then `text: !quote null`, since `!quote` quotes what
  is already at the field). Both move the latch or the draft into the package's own address and
  derive `{to, text}` into the sink from there — which also keeps the record of what a package
  asked for in the package.

## Not a concern here

**A cycle through the world.** The Slack in half reflects channel messages into
`slack.example.message.…`, which charters may read, so a package can react to its own post. Every
connector with an in half has that loop; it is not a property of sinks.

## Done when

`gate-ask`, `deploy-announce` and the digest are installed as packages rather than baseline,
and a charter reading a sink is refused at install naming the sink.