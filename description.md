# task: an action that is several steps goes ahead only if every step would; the policy and the runner's ceiling each decide every one

A claude Edit/Write through a symlink is two `files.write` steps (the path named and the file it lands on), and a codex patch is one per path. Both drivers hand the supervisor ONE of them (`stepOf`: the first step a non-allow, non-default row matches), and the supervisor decides that one. The policy is first-match and the runner's ceiling is never consulted in the pick, so the step picked is not the one that would refuse.

Reproduced on verse/claude-runner (05eabe5b) with the package's fake claude, `docs/link → ../db`, a Write to `docs/link/x`:

- policy `[allow files.write path: docs/**, deny files.write]` ("only docs"): the catch-all deny matches `docs/link/x`, so that step is picked; Decide on it hits the `docs/**` allow first; `decision gate=docs proceed=true`, `exec … written`. The file lands in `db/`.
- runner ceiling `files: [deny files.write path: db/**]`, task policy allowing `files`: no task row is non-allow, so `steps[0]` (the named path) is picked; the ceiling is checked against it alone and passes. The file lands in `db/`.

docs/reference/runnerd.md says "a row on `db/**` decides a write through `docs/link → ../db`", which these two cases contradict.

## The fix

The supervisor classifies an action as a LIST of steps (`SetStepsOf`; `SetStepOf` stays for the one-step classifiers), and decides each of them through the policy and the ceiling:

- any step refused (deny, withhold, not provided, the ceiling) refuses the action, in that step's row;
- else each distinct asking row is asked once, in order, about the first of its steps, and every answer must proceed;
- else it is allowed.

A one-step action decides exactly as now. The drivers' own `stepOf` pick goes away; claude and codex hand over their proposal's steps.

Shell writes (`echo > db/x`) stay `commands.run` and advisory, as docs/systems/inside.md says; the kernel seam is bst6zbs0. The claude section of runnerd.md gets a pointer to that.

Branch: on verse/claude-runner, since both drivers' callers are there; 0t4bdajwh12krvwatxn0's last comment recorded this as open.