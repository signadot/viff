# verse-dev: a coding pipeline, and what it is expected to break on

Status: **phase 1 runs end to end; five defects found, none of them in the agent's output.** The first
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

Five, and three of them are verse rather than the coderunner. **None came from the agent's
output** — four of them stopped the agent from running at all, and the fifth is the one that let
it run and then be understood. Which is itself the finding the drive was for: the pipeline's first job was to exercise
verse, and verse is what broke.

They also hid each other, which is the part worth remembering. Fixing the first is what exposed
the second, and the second is why no test had ever caught the third.

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
export VERSE_ADDR=localhost:19797                     # host:port, not a url

./verse runner add pool:code http://localhost:9802    # prints: reach: "a runner"

./verse task run --runner runners:pool:code --environment verse-docs \
  --as "$USER" --name docsdrift --attach \
  "$(cat docs/working/verse-dev/docs-drift.prompt)"
```

It stops at a gate — defect 3, which is the design rather than a bug. Answer it from the docket,
which is the path defect 1 broke and this drive fixed:

```sh
./verse gate                              # what is waiting
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
