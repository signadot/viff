# verse-dev: a coding pipeline, and what it is expected to break on

Status: **predictions, written before the first run.** The first experiment of the
[verse-dev](verse-dev.md) drive: verse coordinating work on verse, done by
[coding runners](coding-runner.md). Phase 1 is wired; the rest is a sketch of where it goes and
what each step is expected to hit.

The point of writing this first is that a wall you predicted confirms the model and a wall you
did not refutes it. Undifferentiated breakage teaches much less, and a drive that only records
what happened cannot tell the two apart afterwards.

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

### The runner

[verse-dev/coderunner.tony](verse-dev/coderunner.tony) declares one environment over this
repository. `push` targets a **local** ref for the first generations — nothing should reach a
remote until the output has been read by a person.

```sh
go run ./docs/reference/examples/coderunner \
  -config docs/working/verse-dev/coderunner.tony -data ~/.verse-coderunner -addr :9802
verse runner add pool:code http://localhost:9802
```

### By hand first

The charter is written and deliberately **not installed yet**. The runaway guard does not engage
here, by design, so the bound during the experiment is a person — and the first observation
should not also be the first bill.

```sh
verse task run --runner runners:pool:code --environment verse-docs --as "$USER" \
  --attach "$(cat docs/working/verse-dev/docs-drift.prompt)"
```

### Then the charter

[verse-dev/docs-drift.tony](verse-dev/docs-drift.tony) fires on a commit landing on `main`. It
conditions on nothing about trailers: the loop is the experiment, not a thing to prevent.

## What to record

Against the predictions above, and in this note as it goes:

- did phase 1 produce a branch a person would merge, and how much of it was noise;
- which wall phase 2 hit, and whether it was wall 1;
- whether a second generation fired, and whether it found anything — the convergence measurement;
- what, if anything, let a reader connect generation N+1 to N without being told.
