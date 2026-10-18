# verse/next

verse next: verse finds work, proposes it as a contract, builds it, has it checked by a separate evaluator, and offers the result to the merge gate. Built on verse/cut (614b76e6). Based on issue-next (4pje9xn9 steps 3–4) and the generator/evaluator harness in https://www.anthropic.com/engineering/harness-design-long-running-apps.

## Constitution (CHARTER.md v1, proposed)

- **Purpose 1** (organised, observable, gated, steerable, controllable). Every stage is a fact on a record. A person can steer every agent loop while it runs, not only answer its questions.
- **Purpose 3** (return on human attention, rate of change). A person is asked twice: for the contract, which proceeds if nobody answers, and at the merge train, which is declined if nobody answers. A separate evaluator runs between building and asking, so the person reviews work that has already been checked. People can compose asks, so one answer can wait on another or escalate.
- **Purpose 5.** The next-charter kind proposes verse's own charters. Those pass through the repo-charters install gate like any other change.

## The model (`repos.verse.next`)

| entity | what it holds |
|---|---|
| `kind.<name>` | each kind's deliverable and the evaluator's hard thresholds. It is data a person edits, which is how the evaluator is tuned (the article tunes its evaluator over rounds). |
| `sweep` | one per tick. The planner's subject. |
| `contract.<id>` | kind, issue, deliver, doneWhen[], asks[] (open points the builder must ask rather than decide), then the stage, branch, verdicts and feedback. |
| `offer.<branch>` | a branch that passed, with what it says and its verdict. The merge gate reads this. |

Flow: tick → planner (one task, read-only) answers ≤N contracts → **contract gate** (ask, `deadline: {within: 12h, default: proceed}`) → **build** (verse agent, pushes `verse/auto/<task>`, answers with branch and sha) → **evaluate** (separate spawn on that branch: `make check` and the contract's checks run as a script, then an agent judges the diff against doneWhen and the kind's thresholds; it answers pass or fail with feedback) → a failure goes once to **rebuild**/**re-evaluate** (separate rules, so separate task ids and no arithmetic) → a pass becomes an `offer`.

| kind | source | deliverable | hard thresholds |
|---|---|---|---|
| flake | suite sweep, issue | branch | fails or flakes before the fix; passes `-count=N` after; diagnosis named |
| bug | issue | branch + test | the test fails with the fix reverted |
| design | issue with a decided shape (e.g. 30f9) | branch | each numbered point met; each open point became an ask |
| reorg | size sweep | proposal (issue + plan), not code | no import cycles; no wider API between the parts |
| charter | planner | branch under `.verse/charters/<pkg>` | installs; names the CHARTER.md purpose it serves |

## The merge gate (merge.tony) changes model

Today every `verse/auto/*` branch becomes a proposal the moment it appears, and `says` is always drift's report. Under next, a branch becomes **ready** only when its producer says so. Drift's branches are ready when they appear, matched by their task-id prefix (`verse/auto/repos_verse_drift_*`, SafeSeg of `repos.verse.drift:…`). Next's branches are ready when `repos.verse.next.offer.<branch>` passes. `train-load` takes only ready proposals. `proposal-outrun` retires only drift's proposals, because a stale drift fix costs almost nothing to regenerate, while next's take hours and are rebased by `train-run`. Drift's own files are unchanged.

## People in the loop, beyond answering

The walkthrough exercises each of these against a local verse, with 30f9 as the live contract:

- `verse task say <build>`: redirect the builder mid-run. The verse agent takes it at its next boundary; codex steers into the running turn.
- `verse flow suspend` / `resume`: hold the build, read its branch, let it continue.
- `verse task policy <build>`: tighten mid-run, e.g. `commands` → ask. The change applies to later steps and approves nothing already pending; a replacement cannot add verbs.
- answer a policy ask with words (`proceed` plus `say`): permission and direction in one turn.
- `verse task say <evaluate>`: "also check X", then move X into `kind.<name>` so it holds for the next contract.

## Composed asks

- Delegate the contract: `verse ask owner-ok --to <owner> --then '{answer: {ask: <contract question>, choice: proceed}}' --else '{ask: {…lead…}}'`. The gate's question id is deterministic (`repos.verse.next:verse.trigger.build.…~1`).
- Merge after review: compose an ask to a reviewer whose yes answers the train's `merge-to-main`.
- Open: charter-raised composed asks (mt9fce9d). A rule cannot raise an ask with `then`/`else` today. Next is the first real case for deciding that.

## Steps

0. merge.tony: `ready`, drift by prefix, outrun drift-only. Test that a branch from another package never reaches the train until it is offered.
1. Next core: strata, sweep, planner, contract gate, build, evaluate, one rebuild round, offer. versed tests with scripted doers.
2. Kinds: the five `kind.*` seeds.
3. Sources: a token-free script spawn (the suite with `-count`, package sizes) writes candidates the planner reads.
4. Walkthrough: interventions and composed asks on a local verse, 30f9 as the contract.
5. Docs.

Not in this branch: the git-issue out half (labels), the `question` reactive charter.