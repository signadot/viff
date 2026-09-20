# residual: what the drive left

# What the drive left

Status: residual of the drive — verse coordinating verse, the bootstrapping that is
now carried by individual efforts instead ([done.md](done.md)). What it built runs: the
comment-drift charter
([deploy/charters/verse/repo/verse/comment-drift.tony](../../deploy/charters/verse/repo/verse/comment-drift.tony),
read as state machines in [comment-drift-states.md](../../deploy/charters/verse/repo/verse/comment-drift-states.md)) and the coding
runner ([reference/examples/README.md](../reference/examples/README.md#running-the-coderunner)).
This is what those landings did not.

## Does a notice want to be a gate at all?

[contributing.md](contributing.md#coordinating-a-break) uses `git issue` for a contract notice
because it works today and needs nothing. But read what that mechanism actually asks for:
**visible to all, objectable by any, blockable by none, carried by a clock.** That is not an
issue. That is a gate carrying a formula ([tutorial §5](../tutorial.md#5-gates-and-the-docket))
— and `verse docket` is somewhere a dogfooder is already looking, which is exactly the
visibility problem `git issue` has.

The drive earns its keep here immediately, because the formula may not be expressible. The
documented shape is:

```tony
formula: !until [ !quorum 2, !deadline "4h" ]    # the clock defaults it to DENY
```

A notice wants the inverse — it carries on the clock, and an objection is what interrupts it:

```tony
formula: !until [ <the clock carries>, !quorum { choice: reject, n: 1 } ]   # expressible?
```

**Both of those spellings are dead.** `!until`, `!quorum` and `!deadline` are refused by name at
the root of a gate now, and a gate carries `mode: ask` with `deadline: {within, default}` — so
the question below is still open and its formulas need restating before it can be asked.

Three ways this resolves, and we do not know which: `!until` inverts cleanly and the notice is
four lines of charter; it needs a new requirement leaf, and the ask is a small addition to a
grammar that is otherwise settled; or a notice is not a gate at all, because a gate holds an
*action* and there is no action here — only a fact somebody may want to argue with.

That third possibility is the interesting one, and it points at the same gap
[channel-observe](../roadmap/channel-observe.md) names: durable, named, and conversational is
precisely the middle that a `say`, a motion, and an entity each miss.


## What holds a drive's state

The charters and seeds lived on `use/acme`, but the entities they react to are this repo.
Whether a drive about the repo can run against a scope of the repo without the two confusing
each other is untested.

## The read plane is blank for the entire run

`--attach` shows a boundary, the gate, the decision — and then nothing at all for as long as the
agent works. The driver invokes the binary with `--output-format json`, which returns one
document when the whole turn finishes, so the executor has nothing to emit until it is over. An
operator watching a ten-minute task sees a task that might be dead.

This is worse than an ergonomic gap, and the design note said so before the code did:

> Narration onto the read plane. The agent's streaming output maps onto `exec` frames, so `verse
> task get` shows which commands ran inside the foreign agent — the observability that a
> containment-by-absence design otherwise gives up.

The reasoning still holds and the code went the other way. verse gates the *invocation* and not
the agent's steps — deliberately, because it cannot see inside one — so **narration is the entire
compensating mechanism**. Without it, "verse supervises this work" is true only at the boundary
where the work starts.

`--output-format stream-json` is the fix, and it carries one real decision rather than being
mechanical: how much to emit. Every tool use is faithful and chatty, and a task's read plane has
a bounded retain window. `launch.Spec.Retain` is per-spawn for exactly this reason — *"a task
that narrates every token and one that reports four boundaries an hour want windows two orders of
magnitude apart"* — and a streaming foreign agent is the first thing in the tree that is
emphatically the former. So the question is what an observer needs rather than what the agent
said, and that is a judgement to make once, here, rather than per charter.

## Nothing says where the work is happening

The runner knows the workspace path. It never tells anyone. `verse task get` reports status,
result and which runner — not the one piece of live information a coding task has, which is the
directory changing under the agent's hands. The path reaches stderr only at teardown, and only
under `-keep`, so it is recoverable during a run solely by knowing the layout convention
(`<data>/workspaces/<task-id>/<repo-name>`). Found the ordinary way: by looking for it and not
finding it.

There is a home for it already. `task.RepoContext` carries `Root`, the runner sets it through
`SetRepo`, and the mirror already projects task state onto `verse.task.instance` — so the path could
surface with no new vocabulary.

But it should not simply be printed, and the reason is the more interesting half. **A filesystem
path is only meaningful to someone on the runner's own machine.** For a remote runner it is a
string nobody can `cd` into, and publishing it would be naming something no reader can
dereference — the opposite of what index-not-warehouse asks. The honest version is that a
workspace is exactly the kind of thing [expose.md](../roadmap/expose.md) federates: a directory
made readable *through* the verse rather than described by a path. So "where is this work
happening" is a local convenience today and an exposure later, and the distinction is worth
keeping rather than papering over with a string field.

Both say the same thing from different sides: **verse can tell you that work is happening on a
foreign agent, and almost nothing about what it is doing or where.** That is the price of an
opaque leaf, and it is payable — narration and a readable workspace both exist as designs. It is
only unpaid.


## The record cannot say whether a run was governed

The contract run completed `done` with an answer that **does not satisfy its contract** —
checked against the real result: *"file: absent: expected !irtype string"*. So either the
contract never reached the run, or it was not enforced. The plumbing is wired end to end (CLI →
`api.Spawn` → `versed` → the spawn wire → `taskd` → `harness.Build`), and a model told about a
contract would almost certainly have emitted the field it names, so the likely answer is that
the flag was not passed.

Landed: the envelope is on the record — `mirror.Meta` publishes doer, grant, tools, resolver and
gates for every spawn. What follows is the half that is not.

**The point is that this cannot be settled from the record.** `verse.task.instance` carries prompt,
status, result, runner and resolver; `mirror.Meta` can carry `grant` and `tools`, and the
charter path fills them. It has no field for the answer contract at all. So "was this run
governed, and by what?" is unanswerable an hour later, which is exactly the question an operator
asks after a run does something surprising.

[authority.md](../roadmap/authority.md) already names this as an obligation, about a derived
envelope rather than a declared one, and the argument does not care which:

> The derived envelope must be recorded on the task entity so it is inspectable (`task get`),
> reviewable after the fact, and **diffable against what the task actually did**.

That last clause is the one this run failed. The task did something (produced an answer of one
shape) and the record cannot say what was required of it, so no diff is possible. It is the same
complaint as findings 6 and 7 — verse says *that* work happened and little about it — moved from
the read plane to the entity, and it is worse here because the envelope is precisely the part a
person needs after the fact rather than during.


## One file is not a unit of work

Three runs, same prompt, three different sizes of job:

| file | lines | boundaries | outcome |
|---|---|---|---|
| `docs/runners.md` | 240 | 35 | clean, contract shape, delivered nothing |
| `docs/trigger-model.md` | 1126 | 43 | died on a model timeout, mid-finding |
| `docs/trigger-model.md` | 1126 | 49 | stopped mid-verification, ungoverned |

"One file per task" was this drive's own fix for finding 8, and it worked for the file it was
chosen against. It is not a unit: a 4.7× bigger document is a 4.7× bigger job, and the prompt says
*check EVERY such reference in that file*, which on the largest doc in the tree is not a thing that
converges in one task.

The contract makes `done` mean something. It does not make the checking complete — a run that
answers `status: clean` having verified a third of the file is a run that lied without meaning to,
and nothing in the shape can tell.

Which arrives back at the fan-out from the other side. The natural next scoping is per SECTION or
per CLAIM, and that is exactly a fan-out over a list nobody can write down in advance. `!each`
has landed since ([strata.md](../strata.md)), so the list is expressible; what the drive kept
walking into is the half below it that did not land.

What blocks it turns out not to be a missing loop, and it is written up in
[derive-residual.md](derive-residual.md): a derive may carry the firing's IDENTITY and never its
STATE, because a derived entity's address is a function of (rule, subject) and that is what keeps a
re-fire a no-op. A fan-out over an answer breaks it by construction. The generalization that might
not — *a derive may carry state iff its address is a function of that same state* — is there with
the two questions it turns on.


## The width, measured

The note at the top of
[comment-drift.tony](../../deploy/charters/verse/repo/verse/comment-drift.tony) rejected this
shape once: *"a rule over `git:commit` is as wide as the reflected history: 131 commits,
all 131 firings timing out inside the store, every derive lost."* That is the risk this
plan reintroduces on purpose, so it was measured rather than argued about
(`scratchpad/stress-width.sh`, N=131, local):

| phase | what | result |
|---|---|---|
| A | 131 commits land, no charter | 2s — 15ms each |
| B | install the charter; 131 fire at once | 131 records by **14s**; tasks 0 at 14s, 0 at 49s, 22 at 68s, 41 at 90s |
| C | 131 more with the charter running | abandoned — minutes, with reads timing out throughout |

**Nothing is lost.** All 131 derived, all 131 gathered, tasks arriving steadily. The note's
"every derive lost" does not reproduce — what reproduces is the latency, and a first pass
misread that as loss because `verse data --paths` defaults to depth 3 while these records are
four deep, and because a store that cannot answer prints an empty list rather than the error the
long form gives.

**The verdict: a window cannot arrive at once.** Not because anything is lost — it all
completes — but because it takes minutes and degrades reads while it does, and phase C says a
loaded store makes it worse rather than better. The steady state is untouched: a push of one to
five commits is five spawns at ~1/s, which is what the flow already does today.

**The derive half is cheap and the spawn half is the wall.** 131 records in 14 seconds; then the
spawn rule fires nothing for the better part of a minute and trickles at ~0.9/s, which is about
what a single spawn costs end to end — so spawns do not parallelize. A push of one to five
commits is unaffected; only a window arriving at once is. Two hypotheses were checked and are
wrong: dispatch is already parallel (`go e.fireOne` per subject in `fireSubjects`), and the
cascade throttle did not trip (`DefaultTripPace` is exactly 1s and matches, but there is no
`cycle:` line and the chain is 3 deep against a cap of 4). Something inside the spawn path
serializes what the dispatcher parallelized — issue `bteb1a97h12kr7nagnn0`, with the isolating
experiment written down there.

**What that costs in writes.** verse's own log showed ~3900 store commits while working through
131 source commits — roughly 30 writes per commit once the record, the gather patch, the task
run entity and its state transitions are counted. At width 131 that is ~4000 commits arriving
together, per-commit latency climbing (295ms → 1.05s in the slow-commit warnings), and that is
when a concurrent read times out.


## The derive must not name the flow's state

The first attempt at the above failed, and the reason is worth more than the fix. Rule 1 derived
`reviewed: pending` along with the facts. **A derive is a tony patch merged at depth, not a
replace** — it overwrites exactly the fields it NAMES — so every install rewrote every record's
`reviewed` and re-armed every review that had already run. The baseline lasted until the sweep
reached it; the new commit's spawn was starved behind 131 re-gathers.

So rule 1 now states only what it owns, and rule 2 starts on `!not.has-path reviewed`. The start
state is that nobody has written one, and an absence is nobody's to overwrite.

**Which spelling of absence** is not a detail: `!not {reviewed: null}` cannot be stored — verse
writes absence as a bare null and hides every bare null on read, so the durable rule comes back
as `!not {}` and selects nothing, working until the first restart and then silently stopping.
That warning is in
[comment-drift.tony](../../deploy/charters/verse/repo/verse/comment-drift.tony) and it cost a
drive an evening. `!not.has-path` is the spelling that survives: a commit is picked up, verse
goes down and comes back, the stored rule still reads `!not.has-path reviewed`, and the next
commit is picked up too.

This was a latent defect in the flow before any of this: at width one, a charter edit re-armed a
single review and nobody noticed.

## Still untried

- **A commit with a comment that IS stale.** Both real reviews so far were of commits whose
  comments were written with the code, and both correctly reported nothing. That is the harder
  half of the question answered — a model asked to find fault in something that is fine did not
  invent any, twice, and its reasoning is legible in the runner's log ("`r.running()` naming
  coincidentally matches new `c.running()` but no conflict — different scope/type"). What is
  untested is whether it catches one that is really there.
- What a fan-out of ten `call_agent` children costs against one task reading ten files itself.
- Whether the answer contract survives a real report — the empty one satisfied it, and a
  multi-line `report:` is a different question.
- One loose end, recorded rather than chased: the run that failed its answer contract wrote
  `err: placeholder` onto the entity. The runner's log had the real story; the fact had a word
  that says nothing. Where that string comes from is not in this tree.