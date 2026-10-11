# coderunner: how a task's work leaves — the agent, the task process and the runner, written down as one protocol

Feedback from the codex demo on staging (pvk89kvah12kry4rs1n0, run-d44ea09819f9): a branch `refs/heads/verse/auto/<sha>/<task>` appeared on origin while the agent never committed, and the parent-child protocol that the push tool uses to coordinate the agent's clone with the runner's mirror needs clarifying, documenting and discussing.

## What happens today, as the code does it

Three parties, and the agent is the one that does the least.

1. **The agent** (codex, or the llm harness's model) edits files in the task's workspace, a clone the runner made for this task on branch `verse/auto/<task>`. It is told not to commit. It has no credential and, for a codex task, no git credential in any environment.
2. **The task process** (`coderunner -task`, trusted Go, inside the sandbox) runs the loop. When the driver goes quiet, the loop proposes the runner's closing action: a `push` to the destination named `auto`, with the ref it resolves to (`task.Finisher`, push.go). The vector gates it like any other action. Under the default vector there is no push gate, so it is `auto` and goes without asking; a charter that wants to be asked writes `verbs: [push]` or `onPush: true`.
3. **Approved**, the task process commits everything in the workspace, as `verse coderunner <coderunner@invalid>`, with the trailers that are the only externally checkable link back into verse (`Verse-Task`, `Verse-Principal`, `Verse-Environment`, …). An agent that committed on its own is taken as it stands, trailers lost (workspace.go, commitWork). Then it asks the parent over fd 5 for the credential for that destination.
4. **The parent** (the runner) re-checks the destination against the environment, stages the commit in its own bare mirror under `refs/pending/<task>/<repo>/<target>`, and only then answers with the resolved ref and the token (push.go, release). A destination that names no credential releases the runner's own `-token-env`.
5. **The task process pushes** the branch to the ref under that token, and what the remote said is the action's output on the read plane: `pushed verse -> refs/heads/verse/auto/…`, or the remote's refusal as an answer rather than an error. The parent's sweep drops the pending ref once it is on the remote, retries it if it is not, and moves a refused one to `refs/held/`.

So the strangeness is by design and the design has a reason: the delivery is the RUNNER's act, not the agent's. The agent cannot push, cannot name a ref, and holds no token; the runner commits with attribution the agent would not write and pushes to a destination the environment declared. That is why the commit is by `verse coderunner` and why it lands without the agent having said `git commit`.

## What is unclear, and should be settled

- **Where it is written.** The pieces are in `docs/reference/examples/README.md` ("A push is a capability with named destinations", "Work waits in the mirror", "Who enforces what") and in long comments in push.go, stage.go, taskproc.go and workspace.go. There is no one place that walks the five steps above with the three parties named. A page or a section should.
- **The default is to push without asking.** Both default vectors (harness.Vector and the codex driver's) gate writes, commands and delegation, and not `push`. For a foreign agent that is the one action whose effect leaves the verse. Should an ad-hoc run's default vector ask before the push, so the delivery is visible on the docket rather than only on the read plane? The demo would then show `push: auto → refs/heads/verse/auto/…` as a question.
- **The commit's author.** `verse coderunner <coderunner@invalid>` is deliberate (the runner made the commit, not the person) and reads oddly on a forge. The principal is a trailer. Is that the right split, and should the message say the agent's report or something the runner words?
- **What the agent is told.** The prompt says "do not commit" and the agent sees no reason for it. The brief could say that the runner commits and delivers, so the agent's report reads as a proposal rather than as unfinished work.
- **Two clones.** The agent's workspace is a clone from the runner's bare mirror; the mirror is the staging post on the way out. Whether that two-hop shape should be visible to the agent at all (it is not, and should not be) and how it reads in the docs.

Related: km5mzagq (the coderunner's residuals), 1qdmzhac (narration).