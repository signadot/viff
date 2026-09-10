# One gate record: implementation plan

Status: plan, nothing built. The design is git-issue `sp27m3g1h12krdgve1n0`, with the reasoning
it rests on in [attached-resolvers-residual.md §1](attached-resolvers-residual.md). This file
does not restate either; it is the order of work, what each step must prove, and the decisions
the issue left to whoever planned it.

The shape being built, in one line: a question record that carries **options, a threshold per
option, and a deadline with a default**, settled by an answer path that tallies; and then the
removal of everything that existed only because a gate with a formula was a different thing.

## The spine: one settle path, and which one

Today two mechanisms settle a gate, and the plan turns on keeping one.

| | question (`gate:question`) | motion (`motion:channel`) |
|---|---|---|
| answer arrives as | `POST /gate/resolve`, pushed to a waiter | a ballot entity `vote.<mid>.<by>`, written by the CLI |
| tallied by | nobody: one answer settles | the engine, watching the vote log (`awaitMotion`) |
| delivered to | `task.Registry` waiter, a hosted task, or a broker queue | the firing goroutine |
| survives restart by | re-posing the same id and joining | `ResumeMotions` re-monitoring the channel |
| guards | `exclusive`, override, escalation, a resolver binding | revoke guard, mootness, withdrawal |

**Kept: the question path.** The answer path becomes a tally: an answer is a ballot, appended to
the record and checked against the thresholds **in one conditional write**, and the record is
settled in that same write when a threshold is reached. Delivery to the waiter is what it is
today. Three reasons, in order of weight:

- Everything the residual wanted for "a resolver over a motion" comes for free: a resolver
  binding, `exclusive`, override and escalation are all on this path already and know nothing
  about how many ballots settle a question.
- The revoke guard disappears. It existed because a ballot could be retracted between the
  engine noticing a threshold and committing; when the ballot that reaches the threshold and
  the settle are one write, there is no between.
- The hosted and brokered task side cannot watch a store (a worker across a broker pulls its
  answers), so the push delivery has to exist regardless. One tallier beside it is the fork
  again in a new coat.

What the motion path had that the question path must gain: a deadline that settles (step 2),
options beyond yes and no (step 1), and the mootness check on the firing side (step 3, where it
becomes a drop with a reason rather than a monitor ending).

## Order of work

Six steps. Each leaves the tree green. 1 and 2 are invisible to a charter author, since the
default record is exactly today's yes-or-no question; 3 is the first change a charter sees and
the one that deletes the fork; 4 moves provider selection onto the record; 5 is the surface;
6 is the deletion. 3 needs 1 and 2; 4 needs 3; 5 can start after 1; 6 is last because it is
what proves nothing still reads a formula.

### 1. The record tallies

`task.GateQuestion` gains `Options []Option` where an option is `{ID string; Need int}`.
Empty means `[{proceed, 1}, {deny, 1}]`, and every producer today leaves it empty, which is why
this step changes no behaviour. `mirror.PostQuestion` writes `options` on the record;
`mirror.AnswerQuestion` appends a ballot at `ballots.<by>` (`{choice, say, at}`) and, when the
chosen option's tally reaches its `need`, writes `verdict: <option>` and `status: answered` in
the same write. `verdict` is the record's word already, and on a two-option record it reads
exactly as it did. A ballot for an option that is not on the record is refused at the API,
not recorded.

The registry stays the arbiter of who settles, as it is today: the settling ballot is
delivered to the waiter first and recorded with the settle after, so two ballots that reach a
threshold together resolve the way two answers always have, with one winner and the other
told so. What the record adds is the tally the winner counted; the ballot that reaches the
threshold and the settle are one write, and a non-settling ballot is recorded and answered
202 with where the tally stands.

The wire loses a boolean. `api.Resolve.Approve` becomes `Choice string`; `task.Resolution`
gains `Choice` and keeps `Proceed` as `Choice == "proceed"` for every reader that asks a
yes-or-no question of a yes-or-no record (the task loop, `onDenied`, the attach prompt).
`api.Verdict.Verdict` is already a string and is now a choice: `proceed`, `deny`, or an option
id on a select record.

`answerQuestion` (`versed/server.go`) changes in one place: a ballot that does not settle is
recorded and returns 202 rather than 204, and the waiter is not told anything. `SettledBy` and
the stale-answer path are unchanged, since "already answered" means the same thing it did.

**Proves:** every existing gate test passes with `choice: proceed|deny` in place of `approve`.
A record with `need: 2` on `proceed` is still pending after one ballot, settled after the
second, and the settle and the second ballot are one commit (asserted on the commit stream,
not on state before and after). A second ballot by the same `by` replaces the first rather than
counting twice. A ballot names an option the record does not carry and is refused with the
options listed.

`Tier: contract`. The resolve wire changes shape, and `Breaks:` names `approve`.

### 2. A deadline settles

`task.GateQuestion` gains `Deadline *Deadline`, `{Within string; Default string}`. The record
carries `deadline: {within, default, at}` where `at` is the absolute instant, stamped once and
honoured across a restart, which is what `deadlineAt` (`trigger/engine.go`) does for a channel
today and moves here unchanged in spirit.

Escalation has no timer: `arm` stamps `escalatesAt` and `escalateOverdueIn` evaluates it when
a reader arrives, which is enough for a transfer only a reader is for. A deadline settles, and
what it releases (a firing, a task loop) reads no docket, so it gets a timer of its own:
`armDeadline` stamps `deadline.at` once and fires `settleByDeadline` at it, which settles
through the same `settle` an answer takes, with `by: deadline` and the default option. A
default left empty settles on `none`, which a waiter reads as a refusal nobody made and a
provider selection (step 4) reads as no pick. Startup re-arms every pending record with a
stamp, and the docket read settles an overdue one it finds, for the daemon whose timer died.

The default is an ordinary option: a ballot for it counts toward its own threshold like any
other, so a `deny` that needs one settles on one deny. The old behaviour, where a deadline's
default could not be reached by voting, was `!until` having no place for a threshold on the
deny side, and it goes with the formula.

**Proves:** a question with `deadline: {within: 50ms, default: deny}` and no ballot is
delivered as deny by the clock, with the record naming the deadline. A deadline with no default
settles on `none`. A restart re-arms against the stamped `at`, not against a fresh duration:
a stamp backdated while the verse was down fires on the way back up (the case
`TestResumeHonorsADeadlineThatPassedWhileDown` covers for a channel). An overdue question
settles when the docket is read.

`Tier: change`.

### 3. A charter gate is `{name, mode, need, deadline, options, onDenied}`

`trigger.Gate` gains `Need int`, `Deadline *task.Deadline`, `Options []task.Option`, and
loses `Formula`. Validation at `AddTrigger`:

- `need < 1` refused; `need` and `options` together refused (a threshold per option is
  spelled on the option).
- `deadline.within` must parse; `deadline.default` must name an option on the record.
- `mode: auto` with `need`, `deadline` or `options` refused: auto asks nobody.
- `formula:` refused with the message naming the new spelling, and `!fact` specifically
  refused with "waiting for a fact is `when:`".

`Gate.mode()` loses its formula branch: an empty mode is `auto`, as it is for every other gate.

`Engine.fire` loses the fork. Every blocking gate posts a `GateQuestion` carrying the gate's
options, need and deadline, and waits on the registry as a plain gate does today. The mootness
check moves to that wait: the firing watches the subject's subtree while blocked, and when
`stillSelects` says no, it drops the question through the registry with the reason
(`OnDrop` → `mirror.DropQuestion`), so the record reads `status: orphaned` with a `why`, which
is what `orphanMotion` wrote. On a re-fire the firing re-poses the same id; if the record is
already settled it reads the outcome and proceeds or refuses without waiting, which is the
restart case `ResumeMotions` handled for a whether-motion.

Deleted in this step: `monitorGate`, `compileFormula`, `optionFromFormula`, `deadlineDuration`,
the whether-motion half of `ResumeMotions` (`reviewWhether`), and the subject watch inside
`awaitMotion`. What the select axis still runs on — `openChannel`, `awaitMotion`, `drain`,
`commitMotion`, `commitMotionAtomic`, `closedMotion`, `deadlineAt`, `witnessPreconditions`,
`quorumFormula`, the `motion` struct — goes with step 4.

One behaviour changes for a plain `ask` gate, and it is the unification's consequence rather
than a side effect: a firing whose subject leaves the rule's selection while its question stands
is withdrawn, and an answer arriving after that finds nothing. The question arm acted on its
snapshot whenever the answer came; the motion arm withdrew; the trigger-model doc argued for
withdrawal and staging paid for the other reading twice. The test that pinned the old reading
(`TestSubjectValueReadsAtTheCausingCommit`) becomes the withdrawal it now is; the
read-at-causing-commit claim keeps its unit test.

Migrate the two charters that write a formula: `docs/working/comment-drift.tony` (a working
note, but it is the drive, and a drive that does not install is not a record of anything) and
the tutorial's gate. `!until [!quorum 1, !deadline "24h"]` becomes
`{need: 1, deadline: {within: 24h, default: deny}}`.

**Proves:** the tests under `trigger/` that exercised a formula gate pass re-expressed on the
new fields: `gateformula_test`, `deadlineoption_test`, `motionmoot_test`,
`motionwithdraw_test`, `ondenied_test` (both arms of "only a committed refusal runs it"). A
charter with `formula:` is refused at install with the new spelling in the message. A quorum to
deny, `options: [{id: proceed, need: 2}, {id: deny, need: 2}]`, settles on the second deny,
which no formula could say.

`Tier: contract`. The charter gate spelling changes; `Breaks:` names `formula`.

### 4. Provider selection is a question with doers for options

`selectProvider` posts a `GateQuestion` whose options are the eligible doers, each with
`need` from `Provider.Need` (1 for `ask`), and `deadline: {within, default: ""}` for `within`,
where an empty default means no pick: the record settles `outcome: no-pick` and the caller
does not spawn. The option carries the doer's envelope for display, as `motionOption` does
(`grant`, `tools`). The id is derived as it is today (`providerSpecSeed`), so a re-fire joins
the standing question.

The who-motion half of `ResumeMotions` becomes a resume over open question records carrying a
`chain`: navigate the chain back into the charter and re-enter the selection, which re-poses
and joins. `resumeSelection` and `stillSelects` stay; `ownsMotion` reads the question record.

`exclusive` on a record with any `need >= 2` is refused where the binding is applied, for the
reason the residual gives: it would turn a quorum into a threshold of one silently.

**Proves:** `provider_test`, `providerceiling_test`, `motion_seed_test` and both
`motionresume_test`s pass over the question record. A `select: vote, need: 2` over three doers
settles on the second ballot for one of them. A `within` that passes with no ballot records
`no-pick` and spawns nothing. An exclusive binding over a `need: 2` selection is refused at
install.

`Tier: change`. The channel record goes away, but nothing outside the engine read it except
`verse motion`, which step 5 removes.

### 5. One surface

- `verse gate <id>` shows the record: subject and what it says, options with their tallies,
  ballots, the deadline, the outcome. This is `verse motion <id>` today with the question
  fields added, and it replaces the bare listing entry for one id.
- `verse gate <id> --answer <option> --as <who> [--say ...]` is the one answer verb.
  `approve` and `deny` stay as sugar for the two default options and refuse on a record whose
  options are not those two, naming the options. `verse motion`, `--vote` and
  `cmd/verse/motion.go` go; `motionchoice_test` and `motioncmd_test` move to gate.
- A resolver's verdict is a choice (step 1 already made the wire so); `resolverVerdict`
  stops switching on `proceed|deny` and passes the choice through.
- `task gate <id> <name> --as` gains `--option <id>[:need]`, repeatable, and `--deadline
  <within>:<default>`, so a worker raises the same record a charter does. The brokered frame
  (`mirror.PostFrameQuestion`) carries the options too. The deriver's `resolve` effect is
  unchanged: the resource is still the question by id.
- The attach prompt (`⏸ gate … — approve? [y/N]`) stays for a two-option record and lists the
  options for any other.

**Proves:** an agent raises a gate with three options and a person answers it by option from
the CLI; a resolver answers the same record with a choice through its verdict route; the
attach prompt on a three-option record shows three and accepts one.

`Tier: contract`. Commands are removed and `task gate` gains flags.

### 6. The formula shrinks to what `when` uses

`trigger.Formula` keeps `OpAtom` with a `ReqMatch` leaf, which is every `on` and `when` in
existence. Deleted: `OpUntil`, the three stub operators, `ReqQuorum`, `ReqFact`,
`ReqDeadline`, `ReqJudgment`, `ReqAsk`, `ReqDefault`, `Formula.bind`, `quorumNeed`,
`Formula.Eval` if nothing else calls it, the tagged forms in
`trigger/tonycodec.go` (`fromTagged`, `fromTagKeyed`, `requirementFromNode`, `quorumChecked`,
and the `!until`/`!quorum`/`!fact`/`!deadline` rendering in `Formula.ToTonyIR`), and the
matching entries in `schema/curated.tony`. Whether `Formula` then collapses into `Match` with
`On`/`When` typed as `*Match` is a judgment for whoever lands this; the plan only requires
that a bare match in `on:` or `when:` parses and renders byte-identically to today.

**Proves:** every charter in `docs/` and under `docs/reference/examples` installs and reads
back unchanged. `!until` anywhere in a charter is refused with a message. `formula_test`
shrinks to the atom cases; `tony_test` and `condition_test` pass as they are.

`Tier: contract`, because the grammar loses tags, although no installed charter carries them
after step 3.

## What this decides

The issue left four things to the plan.

**Where the record lives: unchanged, for now.** It stays at `gate:question:<id>`, one
constant (`mirror.QuestionRef`). The issue asks for the address to be decided before the
layout freezes, and the decision is that this plan does not move it: relocating a record under
its origin is a change to every reader of the address and to nothing this plan changes, and it
wants the ownership tree from `yezdk01bh` to say what "under the origin" means for a firing
that has no task. The ballots live under the record (`ballots.<by>`), which is the shape that
survives the move.

**Who tallies: the answer path, atomically.** Argued above. The consequence to keep in view is
that a ballot is no longer an entity a client writes; it is an answer the API records. A
charter that wants to react to a ballot reads the record, which it could not do for `vote.*`
either, since those were keyed by a hashed channel id.

**Deadline and escalation share a timer.** They are two instants on one record, and one timer
fires at the earlier. Escalation moves the question to `⊤`; a deadline settles it. A record
may carry both, and the order they fire in is the order of the instants.

**A fact-gate is refused, not translated.** The one shape lost is "wait for a fact up to a
duration, else deny". Nothing writes it. The refusal message says `when:`, and a clock on a
rule, if it is ever wanted, is a rule feature and not a gate.

## What this does not do

- It does not move the record under its origin, or rename `gate:question` to `verse.gate`.
  That is the ownership follow-up.
- It does not address the two clocks the residual names (a provider's `within` against a
  resolver's `deadline.within`). After step 4 they are two fields on one record, which makes
  the collision visible and no less real.
- It does not give a task-runtime vector gate (`write`, `shell`, `delegate`) a threshold, a
  deadline or options. That is planned below as the follow-up, not left.

## Follow-up: the runtime vector gate

A vector gate is `{name, match, mode}` (`task.Gate`), and `Vector.Evaluate` builds the
`GateQuestion` from whichever gate matched. After step 1 the question carries options, a
threshold and a deadline, so the follow-up is to let the gate say them and to copy them across,
plus the surfaces a vector is spelled on. It is a follow-up rather than a step because nothing
in the fork depends on it, and because its one real design question is a runtime one.

**What it is for.** Two things the runtime cannot say today, both named elsewhere as gaps:

- *Who answers when nobody is attached* ([authority.md](../roadmap/authority.md), open
  questions): an unattended run that hits an `ask` parks until someone finds it. A deadline
  with `default: deny` fails the action back to the agent as an error it re-plans around,
  which is what that note says should happen, and it is the same field a charter gate has.
- *How a delegation is supervised*: `Resolution.ChildMode` (inherit the parent's gates, or
  careful) is decided by the answerer of a `delegate` gate today and carried on the
  resolution. That is an option set with two members, spelled nowhere on the question, so a
  person answering from the docket cannot see it and a resolver cannot choose it. Options on
  the `delegate` gate are those modes, and `proceed` is sugar for the default one.

A threshold above one on a runtime gate (`shell` needing two approvals) costs nothing extra
once the record tallies, and is the case the vector's own comment calls "multi-approval" for a
charter gate.

**Order of work**, each green:

1. `task.Gate` gains `Need`, `Deadline` and `Options`, the same types the record carries;
   `Vector.Evaluate` copies them onto the question it builds; the `verse-gate` schema follows.
   The harness's default vector (`harness.Vector()`) is unchanged: `write`, `shell` and
   `delegate` stay two-option `ask` gates. *Proves:* a vector document carrying
   `{name: shell, mode: ask, need: 2}` poses a question the task loop does not pass until the
   second ballot, and one carrying a deadline is delivered `deny` to the loop when the clock
   passes, which the agent sees as the error it sees on a denial today.
2. The brokered worker evaluates its vector on its own machine and reports the question as a
   frame (`mirror.PostFrameQuestion`); the frame carries the gate's options and threshold so
   the record versed writes agrees with the question the worker is blocked on. *Proves:* a
   brokered task with a `need: 2` gate is settled by two answers through the broker queue,
   not one.
3. `delegate` gains its options: the `ChildMode` values, with `proceed` as the default mode
   and `deny` as refusal. `Resolution.ChildMode` is read off the choice rather than set by
   the answerer separately. *Proves:* answering a delegate gate with `careful` from the CLI
   spawns a child whose every action is re-asked; answering `proceed` inherits.
4. Spelling. `task run --vector` and a spawn's `vector:` accept a name today and a document
   only through `task vector <id> --file`. The follow-up decides whether a charter spawn may
   carry a vector document inline, or whether named vectors grow variants. The recommendation
   is the document: `NamedVector` deliberately resolves a name to a bare default with no
   gates, and adding thresholds to names means adding names for every combination.

**What it does not decide.** Whether a deadline on a runtime gate should default to `deny` or
to escalation to `⊤` when the task has a resolver bound. Both are on the record after step 2 of
the main plan; which one a vector gate arms when it says nothing is the runtime question, and
it is the one to settle in the note before this follow-up is claimed.

## Tests, gathered

Moved or re-expressed rather than deleted, because each one names a behaviour that must
survive the fork's removal: `trigger/gateformula_test.go`, `deadlineoption_test.go`,
`motionmoot_test.go`, `motionwithdraw_test.go`, `motion_seed_test.go`, `motionresume_test.go`,
`ondenied_test.go`, `provider_test.go`, `providerceiling_test.go`; `versed/motionresume_test.go`,
`restartgate_test.go`, `resolve_test.go`, `exclusive_test.go`, `escalate_test.go`;
`mirror/mirror_test.go`; `cmd/verse/motionchoice_test.go`, `motioncmd_test.go`. Deleted with
their subject: `trigger/formula_test.go`'s temporal cases.

## Docs, on landing

`trigger-model.md` ("Two layers", `gates`, "A deadline's default option decides nothing on its
own", "A motion outlives the verse that opened it", "A gate's answer applies to the world it was
asked in"), `reference/tasks.md` (the paragraph beginning "A charter gate carrying a
`formula:` is not here"), `attached-resolvers.md` and its residual's §1, `convergence.md` and
`auto-supervisor.md` where they name a motion, the tutorial's gate, `schema/curated.tony`,
`reference/README.md`'s command block. Working notes stay as written.
