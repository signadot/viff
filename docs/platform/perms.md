# Permissions on the platform

This page is the part of permissions that somebody who runs or extends a verse needs: the
`system:` parts, the root and the config, the seed, and how installed things get their records.
The model is [perms.md](../perms.md), and the detail is [reference/perms.md](../reference/perms.md).

## The `system:` parts

`system:<part>` names the part of verse that wrote. The parts are in the table below. The parts
are attribution, not authority. They are one owner, any of them owns the root, and nothing
grants to one part and not another.

| Part | What it writes |
|---|---|
| `docket` | questions |
| `mirror` | the task projection, and a worker's grant |
| `perms` | records |
| `seed` | the seed |
| `charter` | the deployment's charter |
| `default` | everything else |

## Routes that name their own party

Verse refuses every method but GET and HEAD with 400 when it has no `Verse-Principal`. Four
routes are registered in another way. Each of them acts for a party that the route itself names:
an io's `hint` and `pass`, and a cover's `verdicts` and `position`.

## The root and the config

The root is a node with a record, like any other node. A deployment says how open it is in that
record:

```tony
# the root's record in an open verse
owner:        user:admin@verse
grants:       [{child-owner: ["*"]}, {read: ["*"]}]
child-grants: {read: ["*"]}
```

| Deployment | Who makes a top-level place | Who reads what is created |
|---|---|---|
| open | anybody, and they own what they make | everyone, until its owner says otherwise |
| `perms: {admins: […]}` in the config | only an admin: the first line reads `child-owner: [group:admin]`. Everybody else works in places that an admin opens to them. | as in an open verse |

In both, `user:admin@verse` owns the root, and so co-owns everything.

The root's owner is `user:admin@verse`. It is an identity of the deployment, not of a person. It
co-owns everything, and it is what restores the top. `below` holds out what is beneath the root
and not the root itself. Nobody owns the root itself, and everybody may read it.

`perms: {admins: […]}` puts the named grantees into the `admin` group at the seed. It also makes
the root's line `child-owner: [group:admin]`. What an admin creates is recorded as the person
who acted.

The list **only seeds** the group. After the seed, `user:admin@verse` changes the membership
live with `verse group`. So an edit to the list on a running verse changes nothing. At start,
versed says when the root's record is not what the config would write.

`--reseed-perms` is the one start that rewrites the top from the config. It replaces the root,
`verse` and the admin group whole. `perms: {child-grants: {…}}` sets the root's `child-grants`
in the same way. `{}` is a setting: it says that what anybody creates is theirs alone.
([up.md](../reference/up.md#configuration).)

The config keys are `perms.admins`, `perms.child-grants` and `--reseed-perms`, in
[reference/up.md](../reference/up.md#configuration).

## The seed

`system:seed` writes the seed at start, each row once. A row that stands is left as it is, and
this includes an operator's edit.

| at | level | to |
|---|---|---|
| `verse.task.instance.*`, `verse.task.run.*`, `verse.task.claim.*` | child-owner, read | everyone |
| `verse.trigger.rule.*`, `verse.trigger.strata.*`, `verse.trigger.package.*` | child-owner, read | everyone |
| `verse.io.*`, `verse.runners.*`, `verse.resolvers.*`, `verse.schedule.*` | child-owner, read | everyone |
| `verse.principal.group.*` | child-owner, read | everyone |
| `verse.principal.*`, `verse.covers.*`, `verse.trigger.suspension.*` | read | everyone |
| `verse.gate.question.*` | read | everyone |
| `verse.gate.question.*` | read-write | `user:*`, `agent:*` |

Two `child-grants` stand beside the rows:

| at | `child-grants` | effect |
|---|---|---|
| `verse.gate.question` | `{read: ["*"], read-write: [user:*, agent:*]}` | copied onto each question at pose. A private verse narrows it to close the docket. |
| `verse.schedule` | `{read: ["*"]}`, whatever the root's says | schedules, their ticks and the derived calendar are readable in a verse that holds out nothing else |

## Installed things and their records

The charter a deployment ships (`systemCharter`, and verse's built-in clock and calendar) is
installed as the system and acts as the system.

- **An install leaves its installer's record, or verse refuses it.** Each of those four installs
  ends by asking who the place acts as. The answer may be nobody, or somebody whose record stood
  at the name first. Then verse takes the install back out and answers 409.
- **A store can hold one with no record**, for example one written by hand. At start, verse
  names each in its log. Remove it and install it again.
- **The deployment's charter** (`systemCharter`, and the base charter in the binary) is installed
  as `system:charter`, with the system's record on each rule. It fires as the system.
- **What the system makes under `verse` carries the system's record.** Examples are a task that
  a system rule spawns, and the clock and calendar that the base charter derives. The record
  stops the row's `child-owner` line from reaching inside the node.
- **An io takes the nodes its claims name** for its installer, at install. A claim names its
  levels up to its first glob. What the io reflects beneath is owned through those nodes. Verse
  refuses an io over a node that somebody else owns. So two ios that share a node share an
  installer.

## What the system writes

| act | writes |
|---|---|
| a worker's claim | `read-write` and `control` for the claimant on the task's record and the run's, as `system:mirror`. Every report after that is written as the claimant. A runner reached by url gets the same at the first reflection, as `runner:` its `identity`. |
| the deployment's charter | as `system:charter`, at start, after the restore |

## Reads by a worker and by a task's routes

- **A task is read as its record is.** `GET /tasks/{id}`, its `/events` and its `/inbox` hold
  the task's prompt, its transcript, its result and the answers queued for its worker. Each asks
  for `read` on the task's record, `verse.task.instance.<id>`. Verse refuses each without it.
- **Where to dial is not checked.** `GET /tasks/{id}/dial` answers a brokered task's dial
  address and nothing else. A worker reads it before it has claimed the task, so before it holds
  anything on the record. The claim is what grants it. The route gives the same answer for a
  task that is not brokered and for a task that is not there.

## A relayed task's frames

- **A relayed task's frames are not evidence of who did what.** A *frame* is an event that a
  running task streams back: a status, a pending gate, a decision, a `say`, a vector change. For
  a task that verse hosts, verse makes the frames. For a task that verse relays to, such as a
  broker's worker or a remote runner, the frames are whatever the worker sent. So for a relayed
  task:
  - who is working the task is who claimed it;
  - an answer that the route accepted is already on the question, and the worker's decision
    frame only acknowledges that it arrived;
  - a gate that nobody was asked about is settled with the frame's verdict, by the worker of
    record (or the runtime's `auto`, `deadline`, `cancelled`), and casts no ballot;
  - only the question that the task is waiting on is the task's to settle;
  - a vector change is by whoever the control route relayed that mode for, or else by the
    worker.

## The author of a commit

**The store knows who wrote every commit.** The principal on a write goes into the substrate as
the commit's author. So the log itself says who wrote, without a second record. A delta that is
replayed or watched from the store carries the author back. The CLI does not print the author.
It prints the record that decided, which is the same fact seen from the other side.

## What a resolver service holds

**What a service holds.** A `url` resolver is answered through a
[cover](../glossary.md#cover-of-a-resolver): one resolver on one gate stream,
`verse.covers.<name>@<stream>`. The cover acts as the resolver. Its verdict, its escalation and
its position are written as `via:verse.resolvers.<name>`. That name is what the question's line
holds.

The service says what it decided, and never who it is. `by` on the decision frame is the
resolver's name, and verse stamps it. A cover's own address is not a principal. As a grantee,
verse refuses it, and never reads it as a person's email.

## Walked on a deployed verse

These two steps continue [the walk](../perms-guide.md) on a deployed verse.

### Step 3: a charter, and what it owns

The commands in this step read a deployed verse, where walter@example.com installed the charter
`verse-dev.merge`.

A rule has no identity of its own. It acts **via the address of its record**,
`via:verse.trigger.rule.<package>:<name>`. The check resolves that principal to the record's
owner: the person who installed the charter. Everything follows from that one resolution:

- **Its writes are the installer's.** A `patch` or `derive` that a firing makes is written as
  the rule. Verse admits or refuses it as it would the installer. Install a charter that patches
  a place you may not write, and verse refuses the firing with your name in the 403. The terms
  are the same as for a `verse data -put`.
- **What it spawns is the installer's.** Whoever owns the rule owns a task that a firing
  starts, as a task you ran by hand is yours. You co-own the task's questions.
- **It reads as the installer.** Verse evaluates a rule's condition over what the rule's owner
  may read. A change that the rule may not see does not wake the rule, and the rule does not
  latch on it. Two people with different grants who install the same rule get two rules. The
  two rules fire on different views of the store.
- **It co-owns every question its rules pose.** A gate's question carries `under`, the rule's
  address, and the check follows that link for ownership. The installer answers what their own
  charter asked, without being named on any line.

Installing is open in the seed: `verse.trigger.rule` says `child-owner: ["*"]`. So anybody may
install. A rule's record holds out `read: ["*"]` by the root's default. So anybody may read what
is installed.

**A package takes its address.** Installing at `verse-dev.triage` writes an owner record there
for the installer. So every row that the package's rules derive beneath that address is owned
through that one record. The rows do not each carry their own record. Verse refuses somebody
else who installs at the same address, and says who owns it.

The one place a rule may not read is the perms mirror. `verse.perms` is the system's, with
nothing held out on it. Verse refuses a rule that watches it at install, and says why. The
charter that the system stands behind may watch it. That charter comes from `systemCharter:` in
the deployment's config.

A package's `reads:` declaration is a separate question. It says what the package's rules may
reference *outside their own address*. Verse checks it at install, beside all of this.

The installer of `verse-dev.merge` owns its rule:

```sh
verse perms verse trigger rule 'verse-dev.merge:train-run'
```

```tony
owner: user:walter@example.com
grants:
- read: ["*"]
```

Every train the rule cuts, every task it runs, and every `merge-to-main` question it poses is
scott's to control and to answer. It is nobody else's unless a line says so.

### Step 4: a gate with nobody behind it

The commands in this step read a deployed verse, and the question id is one that was posed
there.

A charter gate poses the same record as a task's gate. The record says everything about who may
settle it. Take the gate from the previous step:

```sh
verse perms verse gate question 'verse.trigger.train-run.merge-to-main~adaa5e99'
```

```tony
owner: system:docket
grants:
- read: ["*"]
  read-write: [user:*, agent:*]
```

Read the record as three questions:

| Question | Answer |
|---|---|
| **Who may see it?** | `read: ["*"]`: everyone, runners included. That is the docket's default, copied onto the record at pose. A line with no `at` is about the node the record is on. Nearly every line is. |
| **Who may answer it?** | The `read-write` line. This gate named no `answerers:`, so it took the default too: any person, any agent. A runner is not on the line, and verse refuses it. |
| **Who owns it?** | The system, `system:docket`, which posed it. Also the `train-run` rule's owner, through `under`. `verse perms` does not print `under`, but the check follows it. Owners answer. Only owners hold `control`: to cancel, or to escalate as the owner and not as an answerer. |

**To narrow the next ones,** name the answerers on the gate in the charter, and re-install the
file. The questions posed after that carry that line instead of the default:

```tony
  gates:
  - name: merge-to-main
    mode: ask
    answerers: [walter@example.com, group:release]
    need: 2
    deadline: {within: "24h", default: deny}
```

`need` over the answerers decides whether one or many must answer. Each answerer has one
[ballot](../glossary.md#ballot). `need: 1` is a race. `need` equal to the number of answerers is
all of them.

The install refuses a gate that nobody could settle:

- a threshold above the number of answerers named;
- a name outside the grammar;
- a group that does not stand.

So verse catches a misspelling at install. Otherwise a question would stand unanswerable for a
day. A re-install poses nothing again. A question already on the docket keeps the record it was
posed with.

**To narrow the one that stands,** write its record. You are a co-owner through the rule, and
`verse perms` is the owner's verb:

```sh
verse perms verse gate question 'verse.trigger.train-run.merge-to-main~adaa5e99' \
  -grants '[{read: ["*"], read-write: [user:walter@example.com]}]'
```

**To close the docket for a whole verse,** narrow the `child-grants` on `verse.gate.question`.
Verse gives it to each question's record when the question is posed. A private verse sets it to
`{read: ["*"]}` or less. After that, only co-owners, named answerers and resolvers answer
anything.

**Handing it on.** `verse gate escalate <id> -as <who> [-say <why>]` is open to whoever may
answer, and it takes no `-to`. The question goes to the next rung of the
[ladder](../glossary.md#ladder) that matches its subject. The ladder is the standing resolvers. On a
verse with no standing resolvers, an escalation puts the question back on the open docket, where
it already was. [Step 5](../perms-guide.md#step-5-a-gate-with-a-resolver-behind-it) gives the ladder a rung.
