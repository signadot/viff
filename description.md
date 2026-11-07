# delivery: an answer performs only the act it was given about, by its step as well as its decision

**Status:** found while folding MCP's arguments into its step (1v5art3x). Nothing is built. It
blocks the merge of `verse/secret-exec` (pa35grmz) and 1v5art3x.

## What is wrong

An `ask` row promises that what a person approved is what is performed. The runner does not
hold it.

- **The runner keeps the step with the answer.** When it relays an answer, it reads the call the
  question was about from the task's pending gate (`proc.gateAbout`,
  `pkg/runner/runnerd/subprocess.go`) and keeps it beside the decision's id
  (`Channel.Answer`, `pkg/runner/runnerd/delivery/channel.go`).
- **It matches an act by the id alone.** `Channel.answered` takes the first answer whose decision
  is the one the act names and whose choice is proceed. The kept step is compared only for an act
  that names no decision.

So, under `{match: {system: secret, verb: exec}, then: ask}`, a person approves
`npm publish --ignore-scripts` in `packages/a`. The task's process then sends `secret.exec` with
`--tag next`, or in `packages/b`, naming the same decision, and the runner performs it. The same
holds for every op: a push of another ref, a post to another channel, an issue in another team.

What still holds: the environment and the ceiling. The act is still one the spawn gave and the
operator allows. What does not hold is the row.

## The fix

In `Channel.answered`, an answer whose step is known performs only an act whose step is the same
(`task.Step.Same`, which already compares fields however they were spelled):

- the decision matches, the step is known, and the step differs: no answer, and the act is
  refused, saying that it is not the act that was answered (by its fields, never a value);
- the decision matches and the step is unknown (the runner could not read the gate): as today,
  by the id, and the runner's log already says the answer was "about no call this side read".

The answer is taken only where it performs: a refused act does not spend it.

## What it needs of every op

**The task's tool and the runner build the same step.** Today that is true where both read one
function (`delivery.StepOf` for verse, `delivery.ReadExec` for `secret.exec`), and assumed
elsewhere. Where it is not, an approved act is refused.

- **A conformance test over the table** (`delivery/ops.go`): for each op that asks about a
  step, the step its stock tool builds (`Fields` or `Build`) from a call is `Same` as the step
  the runner builds from the request the tool sends.
- **A push carries facts the runner reads at the forge** (`exists`, `own`, `pushFields`). The
  task asks for them (`state`) before its decision, so the two agree, unless the branch changed
  between the question and the act. Then the act is refused: the person approved other facts.
  That is right, and the refusal says so.

## Tests

- An act approved for one step, sent as another under the same decision: refused, for
  `secret.exec` (another argv, another dir, another variable), `git.push` (another ref) and
  `slack.post` (another channel). The answer stays to be taken.
- The approved act is performed, and a second act on the same decision is asked again (as
  today).
- An answer with no step kept: performed by the id, as today.
- The conformance test above.

## Not in this issue

- **What is not a field is not bound.** A Slack post's text is not in its step (1z5art3x has the
  note), nor an MCP call's arguments until 1v5art3x adds them. This issue binds what the step
  says; those make the step say more.
- **What the approver sees** of the step is q3k89kva.