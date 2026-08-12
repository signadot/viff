# verse-dev: a coding pipeline, and what it is expected to break on

Status: **phase 1 works; twelve findings, and the drive has moved to the `llm` driver.** The first clean run verified a document against the
code and correctly changed nothing. The run before it produced fourteen real findings and the
runner destroyed them, which is finding 9. The first
experiment of the [verse-dev](verse-dev.md) drive: verse coordinating work on verse, done by
[coding runners](coding-runner.md). The predictions below were written before the first run and
are kept as written; [what running it found](#found-by-running-it) is recorded after them.

The point of writing predictions first is that a wall you predicted confirms the model and a
wall you did not refutes it. Undifferentiated breakage teaches much less, and a drive that only
records what happened cannot tell the two apart afterwards.

## The pipeline

Four phases, each picking up what the previous one left in git:

| | | |
|---|---|---|
| 1 | find work | an agent finds docs that contradict the code, and pushes a branch |
| 2 | review | another picks that branch up, reviews it, fixes what it finds |
| 3 | land locally | a **gated** trigger merges to local `main` |
| 4 | publish | a **gated** trigger pushes local `main` to the remote |

Deliberately branches and not pull requests. A PR is a forge's opinion about a workflow, and the
[coding runner](coding-runner.md) pushes a ref and stops there — what happens next is a charter's
business, which is the thing this drive is testing.

## What runs today

**Phase 1, entire.** A commit lands, a charter fires, a coderunner opens a workspace over this
repository, an agent works in it, and the runner pushes `agent/<task>` carrying provenance
trailers. Every piece exists: `environment:` reaches the runner, the git source reflects both the
ref and the commit's trailers, and a trigger can condition on either.

## Predicted wall 1 — phase 2 has no way to say which branch

An environment fixes `base:` in the runner's config, and a review task needs the branch phase 1
*just produced* — per-task, unknowable when the environment was written. There is no per-spawn
way to say which ref, and that is not an oversight: `repo:`/`base:`/`push:` on a spawn was
refused as the second place a ceiling gets expressed.

The resolution the model already predicts is the **claim** — [expose.md](../roadmap/expose.md)
and [authority.md](../roadmap/authority.md) both put request-specific data there, not on the
shared definition. So:

- if phase 2 hits exactly this, the design was **incomplete on schedule**, and the claim record
  is what it was always going to need;
- if it hits something else first, the design was **wrong**, which is the more interesting
  outcome and the reason to write the prediction down.

## Predicted wall 2 — phases 3 and 4 are not agent work

Merging to local `main` and pushing to the remote are deterministic git operations whose only
interesting part is the gate. Verse expresses the gate today. It has no vocabulary for the
operation: [sources.md](../sources.md) reserves an outbound-executor path that is not built.

Both available shapes are wrong in a way worth naming in advance. Spawning an agent to run a
push is absurd — a model call to perform a fixed command. Giving the coderunner a merge driver is
worse, because [coding-runner.md](coding-runner.md) closed exactly that: *a runner that opened
pull requests would be a runner with opinions about a workflow*. So the prediction is that these
two phases either wait for outbound effects or stay a human's job, and that the drive makes that
case better than an argument can.

## The loop is not a hazard, and the prediction about it is different

Phase 3 merges to `main`; the git source reflects a new commit; phase 1 fires again. It is worth
being precise about why that is fine, because the first instinct — condition phase 1 on commits
*without* a `Verse-Task` trailer, to break the cycle — is the wrong lesson.

[cycle-throttle.md](../cycle-throttle.md) already settles it:

> A cycle is not wrong. A cycle is how you write a control loop — poll, compare, correct, poll
> again — and a charter that could not express one would be missing something. What makes the
> runaway pathological is not that it cycles but that it cycles *at the speed of the store*, at
> no declared rate at all.

and it lists, among the places the causal chain deliberately does not survive, *"through the
**world**, where an effector's write returns via the mirror as external"* — which is this
pipeline exactly. A round trip through git is paced by git, by a source pass, and by a model
call. It cannot spin. In that doc's own analogy it is a cycle through a register, not a
combinational loop, and the throttle staying out of it is the mechanism working rather than
failing.

Capping it would be the actual mistake: the bound is the number of installed triggers, so a
repository that legitimately keeps receiving commits would stop being worked on for reasons that
have nothing to do with the work.

**So the prediction is not "the loop runs away". It is "the loop runs correctly and is
invisible."** Each generation arrives as a well-formed, properly-paced firing on a fresh external
commit, and nothing in verse links it to the generation that caused it. That is a claim about
lineage, not about safety, and it is the one worth measuring.

There is a second-order prediction underneath it, and it is the one this experiment is really
for: **if phase 1 fires on its own output and finds more work, the task terminated too early.**
It said done and the world disagreed. The response is another turn in the same task — a steer, or
a stronger answer contract — rather than another flow. A loop that converges says the task knows
when it is finished; one that does not says the answer contract is too weak or the driver stops
short, and both are measurable.

## What the loop exposes, generalized

The throttle drops the chain at four deliberate hops: a spawned task, the world via a mirror, a
blocking gate, and a ticker. Bounding correctly stops at each of them — and *lineage* goes dark
at each of them too, which is a different concern with a different owner.
[questions.md](questions.md) names it as the thing to get right:

> I think this is one of the key contributions of something like verse — to be the decision log +
> lineage tracker for each flow.

So each of those four hops needs a carrier, and they are all the same question: **what does a
cause say about itself on the way out, so the effect can name it on the way back?** Commit
trailers are that carrier for the world-via-git hop, and a spawned task's writes returning through
the API are the same hop with no carrier at all — which is where
[attribution.md](attribution.md)'s parameter-at-install / argument-at-launch lands.

## Found by running it

Twelve, in three kinds. The pipeline's first job was to exercise verse, and verse is what broke.

One through five **stopped the work**, and three of those are verse rather than the coderunner.
They also hid each other, which is the part worth remembering: fixing the first is what exposed
the second, and the second is why no test had ever caught the third.

Six and seven stop nothing. They mean **nobody can see the work happening** — the quieter
failure, and the one a drive is uniquely good at finding, because it is only visible to somebody
sitting and waiting for a task that might as well be dead.

Eight, nine and ten came from runs that worked, and they are the ones that matter most. Eight is
a missing primitive the right shape asks for; nine is the runner destroying the first real result
this drive produced; ten is that its own fix cannot tell success from failure.

### 1. Reaching a task on a per-spawn runner was written three times, all wrong

`runner:` on a spawn exists precisely so work goes somewhere other than this verse's default
runner. Three separate lookups consulted `s.runner` — the default — and so could not see it:

| | symptom |
|---|---|
| answering a gate | `verse gate` lists it; `verse gate approve <id>` → `404 no such question` |
| streaming a read plane | `verse task attach` → `404 page not found` |
| deciding whether an id is a task at all | which is what made the first read as a typo |

The gate one was diagnosed by posting the identical body straight at the runner's
`/tasks/{id}/resolve`, which returned 204 — so the runner and the gate id were both fine and the
break was in verse. `Server.ownerOf` recovers the owning task by cutting the id at `~` and asks
`Server.isTask`, which knew only the local taskd and the default runner, so the owner came back
empty and the answer was offered to the local registry, which had never heard of the question.

The blast radius on that one is worth being exact about: `verse task approve <task-id>
<decision-id>` hands the owner over explicitly and always worked. What failed is `verse gate
approve <id>` — the **docket** path, which has only the question id. That is the path an operator
uses when answering from the list of what is waiting rather than from a task already in hand,
which is the ordinary supervision case and the one a resolver automates.

The read-plane one is worse than it looks. `streamEvents`' own note says *a resolver reads THIS
stream*, so it was not only `attach` that was broken — supervising remote work at all was.

**Fixed**, and it was a lookup gap rather than a design one: `task:instance` already records the
runner url precisely so a restart can reconstruct it without depending on the runner entity
still existing. `Server.placeOf` reads it back the way `reconcileTasks` does, and one
`Server.attach` helper now serves all three call sites. Order there is load-bearing and not a
preference — an HTTP runner's `Attach` hands back a handle for **any** id without asking, so
consulting the default first would reach whichever place happens to be configured rather than the
one holding the task, silently, and only for work sent somewhere by name.

### 2. Every claude task failed the instant its gate was approved

`no spawn factory configured`. The driver set `ToolUse.CallAgent` to make its delegation effect
derivable — but that field is not a label. It is the instruction that routes an action to the
loop's child-spawn path, which needs a `SpawnFunc` this runner does not and should not have: the
agent binary is a foreign one-shot the executor runs, an opaque leaf rather than a recursive
invocation of our own loop. The `Effects` entry alone was always what the vector gates on.

No test caught it because every test stopped **at** the gate — which is exactly what defect 1
forced. The regression now asserts a turn carries the effect and not the request.

### 3. The claude driver ASK-gates its own invocation

So an unattended `task run` parks at once. That is the designed default and stays: verse cannot
see inside a foreign agent, so the invocation is the only thing there is to decide about. It is
recorded because it is a surprise the first time, and because with defect 1 unfixed the park was
permanent.

### 4. The agent's credential is keyed on the OS user, and the floor withheld it

With defects 1–3 fixed the gate was approved, the binary ran, and it failed on its own terms:
`Not logged in · Please run /login`. Bisecting the passthrough allowlist found one variable —
**`USER`**. `PATH` and `HOME` are not enough; the agent looks its credential up by OS user.

Adding it is a one-word fix and the wrong place to stop, because of what it says. The failure
was **loud, attributable and safe**: the agent reported it, `verse task get` carried it, and
nothing ran unauthenticated. That is the floor working — an allowlist's whole cost is discovering
what a program actually needs, and this is what paying it looks like. Had the runner inherited
the environment wholesale it would have worked by accident, and nobody would have learned the
next sentence.

**The agent authenticates as the runner's OS user.** So `principal: distinct` — the containment
rung that gives each task its own uid, and the one the credential split depends on — will break
agent auth the day it lands. The two designs collide exactly there, and the collision is
predictable now rather than mysterious later. Whatever answers it (a credential the environment
hands the task, a login per pooled uid, an API key rather than a session) is a decision the
containment work has to make rather than discover.

### 5. The agent was not told what its workspace holds

The layout is the *runner's* convention — one directory per repository under a root nobody
outside chose — so a prompt written against it was guessing, and one written against a
single-repo workspace ("look in `docs/`") is simply wrong the moment a second repository appears.
`Workspace.Brief` now says what is there and which repository is reference-only, so an agent does
not spend a turn editing a tree whose changes are discarded.

### 6. The read plane is blank for the entire run

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

### 7. Nothing says where the work is happening

The runner knows the workspace path. It never tells anyone. `verse task get` reports status,
result and which runner — not the one piece of live information a coding task has, which is the
directory changing under the agent's hands. The path reaches stderr only at teardown, and only
under `-keep`, so it is recoverable during a run solely by knowing the layout convention
(`<data>/workspaces/<task-id>/<repo-name>`). Found the ordinary way: by looking for it and not
finding it.

There is a home for it already. `task.RepoContext` carries `Root`, the runner sets it through
`SetRepo`, and the mirror already projects task state onto `task:instance` — so the path could
surface with no new vocabulary.

But it should not simply be printed, and the reason is the more interesting half. **A filesystem
path is only meaningful to someone on the runner's own machine.** For a remote runner it is a
string nobody can `cd` into, and publishing it would be naming something no reader can
dereference — the opposite of what index-not-warehouse asks. The honest version is that a
workspace is exactly the kind of thing [expose.md](../roadmap/expose.md) federates: a directory
made readable *through* the verse rather than described by a path. So "where is this work
happening" is a local convenience today and an exposure later, and the distinction is worth
keeping rather than papering over with a string field.

### 8. A task can find N things, and verse cannot make them N facts

The task as written was **too big**: it conflated find, decide and do, and only the last needs a
workspace at all. The symptom was that nothing was visible for ten minutes — a task with no
intermediate artifact has no checkpoint, so working and stuck are indistinguishable. The shape
that fixes it is a fan-out: one task finds and *answers with a list*, a second prioritizes, a
third fixes each item.

That decomposition should be **entities, not a chain of three tasks**, which is the difference
between using verse and scripting through it. As entities, each finding is independently
matchable, prioritizable, gateable, re-fireable and visible in the flow tree, and "prioritize"
and "fix" become rules over them rather than steps in a pipeline. `derive` is the mechanism — it
writes an entity that is not the subject, the one thing `patch` cannot do.

**And there is no way to get from a list to entities.** `deref`'s own doc is explicit that it
*"names a value, it does not compute one"*: `$result.findings[0].severity` is a fixed index.
`derive` writes one entity per firing. Fan-out fires per matched **entity**, over what is already
in the store. So the hop from "one task answered with twelve findings" to "twelve finding
entities" has no spelling in the grammar.

Every available answer is unbuilt and costs something: derive-per-element is a loop, which cuts
against deref's stated non-computation; the task writing entities itself needs the agent holding
verse credentials, reopening the grant question the credential split exists to close; and one
entity carrying the list cannot be fanned over, which only moves the problem.

This is a sharper form of the complaint `deref.go` opens with — that a task's output could never
become a fact the next `when` matches on. That was closed for one value and not for a list. It is
also a wall neither prediction anticipated, and it came from wanting a shape that is obviously
right rather than from anything failing.

**Interim**: one file per task, named in the prompt. Minutes rather than tens of minutes, a diff
small enough to judge, and a cheap failure — without needing the missing primitive.

### 9. The runner destroyed the first real result it produced

The agent completed, reported fourteen verified findings with file, line and evidence across a
dozen documents — the first genuinely useful output of this drive — and **nothing survived**. No
branch on the target repository, no branch in the mirror, no dangling objects, no workspace.

Two defects, both in the coderunner, and the second explains the first.

`Runner.finish` removed the workspace **unconditionally** after attempting delivery. A delivery
that failed, or that found nothing to deliver, therefore destroyed the tree the work was in —
and because a task's commits live in its own clone, `os.RemoveAll` takes the object store with
them. The disk argument for removing is an argument about *successful* runs; it says nothing
about keeping the only copy of work nobody has seen. Now: an error keeps it, and so does a
workspace that was supposed to deliver and did not, both said loudly with the path.

And `deliver` tested for **dirt** rather than for **work**: `len(changed()) == 0` meant "nothing
happened". An agent that commits its own edits — which a coding agent will do, told not to or
otherwise — leaves a clean tree with its work in commits, which read as nothing to deliver. Now
the test is "the tree is dirty **or** the branch moved off the sha it started from", and an
already-committed branch is pushed as it stands.

That second path costs the trailers, since they are written by the runner's own commit. Work
preserved without provenance beats work destroyed, and it is the reason a prompt should still say
*do not commit* — the trade is deliberate rather than a preference.

The generalizable form, and the reason this is worth more than its fix: **a runner is the last
place a result exists before it is anywhere else.** Every cleanup path in one is a chance to
destroy the only copy, and "clean up after yourself" is the instinct that does it. Cleanup must
be conditional on the work having left.

### 10. "Nothing to do" and "something went wrong" are the same signal

The first clean run: one file, `docs/runners.md`, about four minutes. The agent checked roughly
eighteen claims against the code — exported symbols, route tables, the exact wording of a
refusal message, which test asserts the doc's own charter parses — reported every one as true
with file and line, and **changed nothing**, which the prompt explicitly allows as a real result.

The runner then said:

> KEEPING … — this environment pushes somewhere and nothing was delivered, so either the task
> changed nothing or something is wrong

Correct, and useless, because it is a disjunction the runner cannot resolve. It sees a clean
tree and an unmoved branch, and those look identical whether the work was done and found nothing
or the work never happened. So finding 9's safety net — keep what did not leave — turns every
legitimately clean run into a workspace nobody reclaims, and disk fills at the rate of good news.

**The party that knows is the task**, and it said so in prose: *"Nothing in `docs/runners.md` is
wrong. I changed nothing."* Unreadable to a runner. This is finding 8 from the other end — a
result that is text cannot be acted on — except that here the fix already exists rather than
being missing: `launch.Spec.Answer` is a contract the claude driver enforces, so a spawn can
require `{clean: bool, findings: [...]}` and the runner can tell a finished job from a failed
one without guessing.

Worth stating as a rule, because it will recur wherever a runner decides anything about what a
task produced: **a runner should not infer intent from a filesystem.** Absence of change is not
evidence of anything. Ask the task, or do not ask.

### 11. A foreign agent has no autonomy dial, which is most of what verse is for

The sharpest finding, and it is about kind rather than degree. verse's central claim is a dial
between "just do it" and "ask" — per action, slidable, observed. **A foreign agent has two
positions on that dial: invoke it, or do not.** Everything the vector can express between them is
unreachable, because there is nothing between them to see. Findings 6 and 7 are that fact showing
up as missing narration and a missing path; this is the fact itself.

It also cost containment. The agent escaped its workspace and edited the real checkout — writing
files was never observed, never gated, never confined, because none of those things exist for a
box that gets a prompt and returns a paragraph.

**So the drive moves to the `llm` driver.** Not because the harness is better at the work — the
foreign agent's output was excellent — but because every action it takes is one the vector sees,
gates, and narrates onto the read plane. That is the autonomy dial actually working rather than
being nominally present, and it is what verse is *for*.

The containment comes with it, and needs no kernel feature. `read_file`, `list_dir` and
`write_file` are confined to the workspace by construction — `harness.safePath` refuses a path
that resolves outside the root — while `run_bash` gets the root as a working directory and
nothing else. So **the shell is the one tool that can leave**, and an environment whose work is
reading and editing files does not need one. Withholding it is a stronger statement than any
prompt, and the only one available on a machine with neither runc nor bwrap.

`Environment.Tools` is that floor, checked when the runner starts rather than at the first
firing. The drive's environment offers `read_file`, `list_dir`, `write_file` and no shell, which
is what makes it safe to point at a real checkout again.

One thing this exposed on the way: the runner had **no place for model credentials at all**, so
`driver: llm` worked only if `PORTKEY_*` happened to be in the runner process's environment.
verse's own configured creds live in another process and are invisible here. That is the ambient
credential this design refuses everywhere else, and it is now named in the runner's config the
same way the git token is — as the NAME of an environment variable, so a config that is copied
carries no secret.

### 12. Nothing records whether a run was governed

The contract run completed `done` with an answer that **does not satisfy its contract** —
checked against the real result: *"file: absent: expected !irtype string"*. So either the
contract never reached the run, or it was not enforced. The plumbing is wired end to end (CLI →
`api.Spawn` → `versed` → the spawn wire → `taskd` → `harness.Build`), and a model told about a
contract would almost certainly have emitted the field it names, so the likely answer is that
the flag was not passed.

**The point is that this cannot be settled from the record.** `task:instance` carries prompt,
status, result, runner and resolver; `mirror.Meta` can carry `grant` and `tools`, and the
charter path fills them. It has no field for the answer contract at all, and the ad-hoc path
records none of the envelope — not vector, not grant, not tools. So "was this run governed, and
by what?" is unanswerable an hour later, which is exactly the question an operator asks after a
run does something surprising.

[authority.md](../roadmap/authority.md) already names this as an obligation, about a derived
envelope rather than a declared one, and the argument does not care which:

> The derived envelope must be recorded on the task entity so it is inspectable (`task get`),
> reviewable after the fact, and **diffable against what the task actually did**.

That last clause is the one this run failed. The task did something (produced an answer of one
shape) and the record cannot say what was required of it, so no diff is possible. It is the same
complaint as findings 6 and 7 — verse says *that* work happened and little about it — moved from
the read plane to the entity, and it is worse here because the envelope is precisely the part a
person needs after the fact rather than during.

### Findings 6 and 7 are one complaint

Both say the same thing from different sides: **verse can tell you that work is happening on a
foreign agent, and almost nothing about what it is doing or where.** That is the price of an
opaque leaf, and it is payable — narration and a readable workspace both exist as designs. It is
only unpaid.

## Phase 1, concretely

### The first task has an oracle, on purpose

"Docs out of sync with reality" has no oracle, so a disagreement in phase 2 teaches nothing about
the pipeline — you end up debugging taste. The first task is therefore the checkable subset:
**doc references that no longer resolve** — a function, file, flag or route named in `docs/` that
does not exist in the tree. Same pipeline, verifiable payload, so a false positive is visible as
one. The tree already tests in this spirit (`TestDocCharartersInstall`,
`TestDocsCharterCarriesRunner`), which is also why there is prior evidence the task is worth
doing: this drive's own work found `versed.launchOf`'s comment and `expose.md`'s "no referent"
sentence both describing a tree that had moved.

Once the pipeline holds, swap in the open-ended version. That is where the interesting defects
are, and it wants a pipeline that is already known to work.

### Running it

The CLI must be built from this branch — a `verse` predating the fixes above has neither
`--environment` nor an answerable gate.

```sh
go build -o ./verse ./cmd/verse

# the runner, over the checkout named in its config
go run ./docs/reference/examples/coderunner \
  -config docs/working/verse-dev/coderunner.tony -data ~/.verse-coderunner -addr :9802

# a verse
./verse up --data ~/.verse-drive --addr :19797
# host:port, not a url. Without it the CLI talks to :9797 and reports a refused connection.
export VERSE_ADDR=localhost:19797

# Prints `reach: "a runner"`. NOTE: no trailing `# comment` on any line in this block —
# interactive zsh has INTERACTIVE_COMMENTS off, so a pasted comment arrives as ARGUMENTS. Here
# that made `runner add` see four extra positionals, fall through to its `[file]` form, and
# report `open pool:code: no such file or directory`.
./verse runner add pool:code http://localhost:9802

./verse task run --runner runners:pool:code --environment verse-docs \
  --as "$USER" --name docsdrift --attach \
  --answer docs/working/verse-dev/docs-drift.answer \
  "$(sed 's|@FILE@|docs/runners.md|' docs/working/verse-dev/docs-drift.prompt)"
```

**`--answer` is not optional here**, and leaving it off is a mistake this note made for
several runs. Without a contract the loop's only definition of finished is the model declining to
call another tool — which is also what a model narrating its next step looks like. Observed twice:
a run ended mid-verification with a page of ✓ marks as its `result`, and `done` on the read plane.
[docs-drift.answer](verse-dev/docs-drift.answer) says exactly this in its own header, which is
what makes the omission worth recording rather than just fixing.

**One file per task**, named where the command runs. That is not a shortcut, it is the fix for
finding 8: a task scoped to one file answers in minutes, produces a diff small enough to judge,
and fails cheaply. `docs/runners.md` is a reasonable first target — dense with references to
code, and recently moved under it.

It stops at a gate — defect 3, which is the design rather than a bug. Answer it from the docket,
which is the path defect 1 broke and this drive fixed:

```sh
# `verse gate` alone lists what is waiting.
./verse gate
./verse gate approve <id> --as "$USER"
```

`--vector auto` on the spawn skips the asking entirely. It is not the default here because
watching the first generations is the point.

Then read what it produced, before anything else. Nothing has left the machine: `push` targets a
**local** ref, and phase 4 is the gated step that would change that.

```sh
git -C <checkout> log --format='%B' -1 agent/<id>   # trailers, and the agent's own summary
git -C <checkout> diff main..agent/<id>             # what it actually changed
```

### One file is not a unit of work

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

Which arrives back at finding 8 from the other side. The natural next scoping is per SECTION or per
CLAIM, and that is exactly a fan-out over a list nobody can write down in advance — the primitive
that does not exist. The drive keeps walking into the same missing thing from different directions,
which is the strongest evidence yet that it is the right thing to build.

What blocks it turns out not to be a missing loop, and it is written up in
[derive-residual.md](derive-residual.md): a derive may carry the firing's IDENTITY and never its
STATE, because a derived entity's address is a function of (rule, subject) and that is what keeps a
re-fire a no-op. A fan-out over an answer breaks it by construction. The generalization that might
not — *a derive may carry state iff its address is a function of that same state* — is there with
the two questions it turns on.

### What changed under this since the last drive

Four layers landed between that run and this one, and three of them alter what an operator sees.

**The task is its own process.** Every runner log line is tagged `[<task id>]`, because that
output is a child's stderr copied onto the runner's log. A task that dies now becomes a `failed`
this runner reports with the exit status in it, rather than a stream that goes quiet.

**The push is an action of the task**, gated by its vector, narrated on the read plane before the
terminal status. Under the harness's default vector it runs `auto` — `Vector()` gates write, exec
and call-agent, and `push` is a verb none of them name — so phase 1 behaves as it did. Phase 4,
whose whole point was a gated push, can now say so:

```tony
vector:
  default: auto
  gates:
  - {name: delivery, mode: ask, match: {verbs: [push]}}
```

**Workspaces are gone when a task ends, and that is safe now.** Work is staged into the runner's
mirror before any push is attempted, so a refused delivery, a failed push and a dead task all end
with the commit in the mirror rather than in a directory. Which means the old habit — go find the
workspace under `-data` and look — no longer works. Ask the runner instead:

```sh
curl -s localhost:9802/runner        # `undelivered:` lists it, with the command to fetch it
```

Each entry carries a pasteable `git fetch <mirror> <ref>:refs/heads/recovered/<task>-<repo>`,
because the mirror is a directory named after a hash of a url and nobody should have to know that.
`-keep` still keeps workspaces if watching one live is what you want.

**Nothing has run through bwrap or the egress proxy.** This drive is `-confine host`, which claims
almost nothing, so the tool floor — no `run_bash` — is still the containment that is actually
doing the work here.

### Then the charter

[verse-dev/docs-drift.tony](verse-dev/docs-drift.tony) fires on a commit landing on `main`, and
is deliberately **not installed yet**. It conditions on nothing about trailers: the loop is the
experiment, not a thing to prevent — and since the runaway guard correctly does not engage across
a paced hop, the bound during the experiment is a person.

## What to record

Against the predictions above, and in this note as it goes:

- did phase 1 produce a branch a person would merge, and how much of it was noise;
- which wall phase 2 hit, and whether it was wall 1;
- whether a second generation fired, and whether it found anything — the convergence measurement;
- what, if anything, let a reader connect generation N+1 to N without being told.
