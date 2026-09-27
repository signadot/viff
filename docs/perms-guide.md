# Permissions, walked: from a write of your own to a gate somebody else answers

[perms.md](perms.md) is the model: one owner per node, grants on the records, one check on
every write and read. This is the same thing walked, in the order a person meets it. Most of
what you own in a verse you never wrote by hand — a task record the runtime mirrored, a
question a rule posed, a branch a source reflected — and the permissions on those follow from
who asked for the work rather than from who typed. That is the part that surprises, so this
walks it in five steps: your own entities; a task you run and its gates; a charter and what it
owns; a gate with no resolver behind it; and a gate with one.

## Before the first step: who you are

Every request the CLI makes carries a principal, and the check holds every write and read to
what its place permits *for that name*. So the first thing to settle is what the name is.

The CLI reads its config in layers — `/etc/verse.tony`, then `~/.config/verse.tony` patched
over it, then the environment, then the flag on the command — and `user:` in the file is who
every command acts as ([reference/up.md](reference/up.md#configuration)):

```tony
# ~/.config/verse.tony
addr: "localhost:9797"
user: "walter@example.com"
```

`VERSE_USER` in the environment overrides the file for a shell, and `-as` overrides both on
the commands that take one, which is how you act as somebody else for one command to see what
they see. `VERSE_CONFIG=<file>` replaces both file layers outright, so a second verse gets a
second file rather than a second identity in one.

With no `user` anywhere, who the CLI acts as depends on where the verse is:

- **A verse on this machine** (`localhost`, `127.0.0.1`, `::1`): the CLI acts as the local
  user, `user:<login>@localhost`, where `<login>` is your login name. This is who you are in
  the [tutorial](tutorial.md) before you configure anything.
- **Any other verse:** the request names nobody. It reads what `*` may read, and it writes
  nothing. Every request that is not a read is refused `400 Verse-Principal is required`,
  since a write by nobody would be recorded as the system's.

The server refuses a request that names nobody wherever it comes from. The local user is the
CLI's default, so a program of your own that writes must name somebody itself.

```sh
verse data spaces scott notes -put '{todo: [buy milk]}'
verse perms spaces scott notes | grep owner             # owner: user:walter@example.com
VERSE_USER=ana@example.com verse perms spaces scott notes | grep owner   # the same record, read as ana
```

A name carries its kind before the colon, and a request presents one of three:
`user:walter@example.com` for a person, which a bare email in `user:` or `-as` is read as;
`agent:<name>` for a harness driving verse from outside, which sets `agent:` in its config
instead; and `runner:<identity>` for a worker, which claims work as `-as runner:<identity>` (a
runner reached by url acts as the `-identity` on its entity). A bare word such as
`-as coderunner` is refused, saying the grammar ([perms.md](perms.md#principals-and-grantees)
has it whole). The name is self-asserted — there is no login — and everything below is about
what a name may do, not about proving who holds it.

Three things to know before the first step, because each is a reasonable guess that is wrong:

- **A group is not a principal.** `group:<name>` is a grantee — on a grant line, in a gate's
  `answerers`, behind a resolver — and it admits the group's members, each acting as themselves.
  Nothing ever acts *as* a group, no request can present one, and a group owns nothing.
- **The `agent` principal exists and nothing uses it yet.** A harness driving verse from
  outside can present itself as an agent (`agent:` in the CLI's config) and is listed under its
  own kind, and today it is granted exactly what a person is. It is a name that grants can
  narrow against later; until they do, read `[user:*, agent:*]` as "people".
- **Identity is self-asserted.** A request is whoever the config, `VERSE_USER` or `-as` says,
  and the server trusts it as far as that. The check holds every write to what its place
  permits *for that name*; who is behind the name is a different question, not yet asked.

## Step 1: your own entities, and who wrote them

With `user:` set, everything you type is yours. The seeded entities from
[the tutorial](tutorial.md#2-entities) are a fine place to start, and so is an address of your
own:

```sh
verse data spaces scott notes -put '{todo: [buy milk]}'
verse perms spaces scott notes
```

```tony
at: spaces.scott.notes
grants:
- read: ["*"]
owner: user:walter@example.com
```

Two rules made that record. Outside `verse`, the tree is open to *create* until a record says
otherwise, so a write where nothing stood makes the writer the owner: `spaces.scott.notes` is
scott's because scott wrote it first. And a new node holds out what the nearest `child-grants:` above
it says, which at the root is `read: ["*"]` — readable by everyone, writable by its owner
alone. Another user is refused the write and told why:

```
VERSE_USER=ana@example.com verse data spaces scott notes -put '{todo: []}'
→ 403 refused: user:ana@example.com holds read at spaces.scott.notes and writing it needs
  read-write; the nearest owner on record is user:walter@example.com at spaces.scott.notes
```

The refusal is the check's own words, and it always says three things: what the requester
holds there, what the act needed, and which record decided. When something is refused that you
expected to pass, that last part is where to look.

**Take a place.** `verse perms` on a node you may create under gives it a record of its own,
and what you set there governs what is made beneath it — in the world's half, where a place is
taken AHEAD of what goes in it. A node that already stands has an owner, the nearest record
above it, and is not taken from them by arriving: holding `child-owner` somewhere is the right
to create there, and says nothing about what stands. Under `verse` a place is taken only where
something stands, and only by its owner:
what stands there is made by the verb that owns it (`charter -a`, `io add`, `task run`),
and is its maker's.

```sh
verse perms spaces scott -child-grants '{}' -grants '[]'  # nothing held out beneath, nothing on it
verse data spaces scott private -put '{…}'              # scott's alone: no read for anyone
verse perms spaces scott -grant '{read: [ana@example.com]}'
verse perms spaces scott -revoke '{read: [ana@example.com]}'
```

`-child-grants` is what a node created *beneath* starts out holding out, copied onto each new record at
creation; `-grants` replaces what the node itself holds out; `-grant` adds a line and
`-revoke` takes one off. A grant withdrawn stops future acts and touches nothing that exists.
Ownership changes only by create and delete: there is no transfer, and the way to hand
something over is to make it under a place the other party owns.

**The store knows who wrote every commit.** The principal on a write rides it into the
substrate as the commit's author, so the log itself says who, without a second record — a
delta replayed or watched from the store carries the author back. The CLI does not print it
yet; what it prints is the record that decided, which is the same fact from the other side.

**What is not yours to write.** `verse data -put` under `verse.` is refused whatever you hold:
`verse` is written through the verbs that own it (`verse c -a`, `verse task run`,
`verse gate approve`, `verse perms`), and beneath each verb the store checks the write against
its place, so nothing reaching the store can stamp a task or answer a question on your behalf.
`verse data verse perms` is refused too: a person reads records with `verse perms`, never the
mirror — bare, it lists the records of every node you may read, and with a path it reads one.

## Step 2: a task you run, and what it may do for its gates

A task is an entity the runtime mirrors, and the mirror writes it *as whoever asked for it*:

```sh
verse task run 'check the migration' -subject demo.pr.1 -vector ask
verse perms verse task instance <id>
```

The task is yours: the record beneath `verse.task.instance` names you as owner, because the
seed holds `child-owner` there out to everyone and the spawn wrote as you. Its `run` and `claim`
records follow it. Reads of the task go by the default it was created with — everyone — and
writes to it are yours and the runtime's.

**The runner working it holds the task and nothing else.** When a worker claims the task —
a brokered claim as `runner:<identity>`, or a runner reached by url acting as `runner:` the
`identity` on its entity — versed grants it `read-write` and `control` on the task and on this run, and every
report from then on is written as it. That grant stops at the task's record. A question the
task raises stands at its own address, `verse.gate.question.<id>`, and a grant on the task does
not reach through the task's record to it, so the executor may not answer its own gate. Nothing
special-cases this; it is what "a grant does not reach through an owner record" means.

**What you may say about its gates.** Four things, and they answer different questions:

- `-vector auto|ask|park` is *whether* the run stops to ask at all. `auto` asks nobody.
- `-gates <file>` is *which* actions ask, and what each question takes to settle: a list in
  the shape a charter's `gates:` uses — `match` over verbs and tools, `need`, `options`,
  `deadline`, and `answerers`:

  ```tony
  - name: shell
    mode: ask
    match: {verbs: [exec]}
    need: 1
    deadline: {within: "4h", default: deny}
    answerers: [ana@example.com]
  ```

  `answerers` is *who* may answer: it becomes the question's `read-write` line when the
  question is posed, in place of the docket's default. Left off, the default stands — `[user:*,
  agent:*]` in the seed, which is any person.
- `-resolver <resolver>` holds the run's questions out to a resolver instead ([step 5](#step-5-a-gate-with-a-resolver-behind-it)).
- `-as <who>` is who the run is, and therefore who owns it and co-owns what it asks.

What the task may *do* in the world — write files, push, delegate — is a different axis: the
`grant` on a spawn (`all`, `read-only`, `no-delegate`, `none`, [trigger-model.md](trigger-model.md#action--what-fires))
is a hard envelope the runtime refuses outside of, not a line on a record. A manual run takes
the runtime's default; a charter's spawn names one. The two do not meet: a permissions grant
says who may touch an entity, a spawn's grant says what a task may attempt.

**Answering.** You answer your own task's questions by co-owning them, whether or not you are
named; whoever is named on the line answers too; the runner is refused. Try it as somebody else
and read the refusal:

```
VERSE_USER=x@example.com verse gate <id> -answer proceed
→ 403 refused: user:x@example.com holds read at verse.gate.question."<id>" and answering it
  needs read-write; the nearest owner on record is system:docket at verse.gate.question."<id>"
```

`x` holds `read` because the docket's default gave every question `read: ["*"]`; it does not
hold `read-write` because it is not on the line. A worker that hits something the task cannot
decide raises a gate up itself, and may say to whom: `verse task do gate <id> <name> -as
runner:<identity> -to ana@example.com`. The spawn's own gate of that name, when it names answerers,
stands over what the worker says.

## Step 3: a charter, and what it owns

A rule has no identity of its own. It acts **via the address of its record**,
`via:verse.trigger.rule.<package>:<name>`, and the check resolves that to the record's owner —
the person who installed the charter. Everything follows from that one resolution:

- **Its writes are the installer's.** A `patch` or `derive` a firing makes is written as the
  rule, admitted or refused as the installer would be. Install a charter that patches a place
  you may not write and the firing is refused with your name in the 403, on the same terms a
  `verse data -put` would be.
- **What it spawns is the installer's.** A task a firing starts is owned by whoever owns the
  rule, exactly as a task you ran by hand is yours — and its questions are co-owned by you.
- **It reads as the installer.** A rule's condition is evaluated over what its owner may read,
  and a rule is neither woken by nor latches on a change it may not see. Two people with
  different grants installing the same rule get two rules that fire on different worlds.
- **It co-owns every question its rules pose.** A gate's question carries `under`, the rule's
  address, and the check follows that link for ownership. The installer answers what their own
  charter asked without being named on any line.

Installing is open in the seed — `verse.trigger.rule` says `child-owner: ["*"]` — so
anybody may install, and a rule's record holds out `read: ["*"]` by the root's default, so
anybody may read what is installed. **A package takes its address.** Installing at
`verse-dev.triage` writes an owner record there for the installer, so every row the package's
rules derive beneath it is owned through that one record rather than each carrying its own — and
somebody else installing at the same address is refused, told who owns it. The one place a rule may not read is the perms mirror:
`verse.perms` is the system's with nothing held out on it, and a rule watching it is refused at
install and told why; the charter the system stands behind, from `systemCharter:` in the
deployment's config, is the one that may. A package's `reads:` declaration is a separate
question — what its rules may reference *outside their own address* — and is checked at install
beside all of this.

On staging, `verse-dev.merge` was installed by walter@example.com, and so:

```sh
verse perms verse trigger rule 'verse-dev.merge:train-run'
```

```tony
owner: user:walter@example.com
grants:
- read: ["*"]
```

Every train the rule cuts, every task it runs and every `merge-to-main` question it poses is
scott's to control and to answer, and nobody else's unless a line says so.

## Step 4: a gate with nobody behind it

A charter gate poses the same record a task's gate does, and the record says everything about
who may settle it. Take the gate from the previous step, as it was posed on staging:

```sh
verse perms verse gate question 'verse.trigger.train-run.merge-to-main~adaa5e99'
```

```tony
owner: system:docket
grants:
- read: ["*"]
  read-write: [user:*, agent:*]
```

Read it as three questions:

- **Who may see it?** `read: ["*"]` — everyone, runners included. That is the docket's
  default, copied onto the record at pose. A line with no `at` is about the node the record
  is on, which is what nearly every line is.
- **Who may answer it?** The `read-write` line. This gate named no `answerers:`, so it took
  the default too: any person, any agent. A runner is not on it and is refused.
- **Who owns it?** The system — `system:docket`, which posed it — and, through `under`, which `verse perms` does not print but
  the check follows — the `train-run` rule's owner. Owners answer, and owners alone hold
  `control`: cancelling, or escalating as the owner rather than as an answerer.

**To narrow the next ones,** name the answerers on the gate in the charter and re-install the
file; the questions posed from then on carry that line instead of the default:

```tony
  gates:
  - name: merge-to-main
    mode: ask
    answerers: [walter@example.com, group:release]
    need: 2
    deadline: {within: "24h", default: deny}
```

`need` over the answerers is what one-or-many means: one ballot per answerer, `need: 1` a
race, `need` equal to their number all of them. The install refuses a gate nobody could settle
— a threshold above the answerers named, a name outside the grammar, a group that does not
stand — so a misspelling is
caught there rather than by a question standing unanswerable for a day. A re-install re-poses
nothing: a question already on the docket keeps the record it was posed with.

**To narrow the one that stands,** write its record. You are a co-owner through the rule, and
`verse perms` is the owner's verb:

```sh
verse perms verse gate question 'verse.trigger.train-run.merge-to-main~adaa5e99' \
  -grants '[{read: ["*"], read-write: [user:walter@example.com]}]'
```

**To close the docket for a whole verse,** narrow the `child-grants` on `verse.gate.question`,
which each question's record is given when it is posed: a private verse sets it to
`{read: ["*"]}` or less, after which only co-owners, named answerers and resolvers answer
anything.

**Handing it on.** `verse gate escalate <id> -as <who> [-say <why>]` is open to whoever may
answer, and it takes no `-to`: the question goes to the next rung of the ladder matching its
subject, and the ladder is the standing resolvers. On a verse with none, an escalation lands
the question back on the open docket, where it already was. Giving the ladder a rung is
[step 5](#step-5-a-gate-with-a-resolver-behind-it).

## Step 5: a gate with a resolver behind it

A resolver ([reference/resolvers.md](reference/resolvers.md)) is the installed record of who
answers questions about a subject, and its answerer is a service that dials in, a person, or a
group. It is a grant with a door in front of it. The door is `condition.subject`, a match over
the entities whose questions it takes, or `byRef`, so a spawn's `-resolver` or a rule's
`gating.resolver` can name it. The grant is what holding a question out to it does to the
record:

```sh
verse resolver add - <<'EOF'
name: ani
condition:
  subject: "verse-dev.merge.train.*"
method: {driver: person, who: peggy@example.com}
deadline: {within: "4h", then: escalate}
EOF
verse gate escalate verse.trigger.train-run.merge-to-main~adaa5e99 -as user:walter@example.com
```

Holding a question out to a resolver **replaces the `read-write` line** with whoever answers as
the resolver: `via:` the resolver's address for a `url` service, whose cover answers as it; the
person for `person`;
`group:<name>` for `group`, its members each as themselves. The default `[user:*, agent:*]` line
goes with it,
so nobody outside the resolver answers any more — except the co-owners, who hold it by
ownership and not by the line. `read` stays as the default gave it. Releasing it, when the
resolver is removed or the ladder runs out, gives the line back to whoever the gate named in
`answerers`, and to the default where the gate named nobody.

**The ladder.** Escalation walks the standing resolvers that match the subject **by name**, from
the one the question is on to the next, and past the last one to the open docket. A resolver's
`deadline: {within, then: escalate}` walks the same rung on its own when the question stands
unanswered that long, so a person who is away does not hold a question forever. A question's
own `deadline`, from the gate, is a different clock: it *settles* the question on its default.
Both are on the record, and the earlier fires first.

**What a service holds.** A `url` resolver is answered through a cover — one resolver on one
gate stream, `verse.covers.<name>@<stream>` — and the cover acts as the resolver: its verdict,
its escalation and its position are written as `via:verse.resolvers.<name>`, and that
name is what the question's line holds. The service says what it decided and never who it is —
`by` on the decision frame is the resolver's name, stamped by verse. A cover's own address is
no principal: as a grantee it is refused, and never read as a person's email. `exclusive` on a resolver decides
whose answer *counts* when several may answer, and is not a security boundary: the line is.

**Groups, here and on a gate.** A group is a named set of grantees, and `group:<name>` is how
anything names it:

```sh
verse group add release walter@example.com group:qa 'runner:*'   # stored at verse.principal.group.release
```

A member is any grantee: a person (`user:<email>`, or the bare email), an agent, a runner, a
place acting (`via:<address>`), a whole kind (`user:*`), everyone (`*`), or another group
(`group:qa`). `verse group add` refuses a member it cannot read — a group's address, a bare
word — saying the grammar. The group is its maker's to change, and membership is read when it
is checked, never copied, which makes it the one grantee whose meaning moves after the fact — on
purpose: change who is in the group and every line that names it follows. The members are
walked exactly: a group already met on the walk is skipped, so a group nested in a group nested
in a group admits its members however deep, and a cycle admits nobody because the walk ends.

A group resolver, `method: {driver: group, group: group:release}`, and a gate's
`answerers: [group:release]` hold a question out to the same people; the difference is that the
resolver is a rung on the ladder and the gate's line is not.

## What to check when something surprises you

- **A refusal names the record that decided.** Read that record with `verse perms <path>`; if
  you may not read it, you are outside the node's `read`, which is the first fact.
- **A question is not under the task.** Look at `verse.gate.question.<id>`, not at the task, and
  remember the runner is refused there by design.
- **A rule acts as its installer.** A firing refused, or a rule that never wakes, is usually the
  installer's own grants seen through the rule.
- **`verse status` counts.** `perms: {refused, unreadable, hidden, unparsed}` is the check's
  census since the process started: refusals, checks the mirror could not answer, what listings
  withheld, and grantee names on a record the grammar does not read — each admitting nobody,
  and logged by versed once per record. On a verse wiped since the grammar, `unparsed` is zero.
- **`[user:*, agent:*]` means people.** Until the agent principal is given a life of its own, a
  line holding both is the docket's openness, no more.
