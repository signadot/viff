# an action's patch lands before its derive fails, so the subject latches over a write that never happened

Split out of an earlier (since-closed) issue, whose main claim is fixed (`9c08af0b`). This is the residual
that issue names in one line — "a `patch` and a `derive` in one action are two writes with no
atomicity between them, and the patch is the one that latches" — reproduced.

## The shape

`Action.applyOne` writes in order: patch, then derive, then spawn. Each failure aborts what
follows, which is right on its own terms — "a task started about a state the trigger FAILED to
establish is worse than no task at all". What it does not undo is what already landed.

    - name: rel
      stratum: low
      condition: {subject: {system: svc, kind: deploy, value: {stage: requested}}}
      action:
        patch: {stage: deploying}
        spawn: {prompt: "go"}
        onDone:
          patch: {stage: deployed}
          derive:
            notes:
              release:
                shipped: !get-path {at: [svc, meta, nowhere], path: version}

Run against a store where `svc:meta:nowhere` does not stand:

    subject          = {stage: deployed, version: v9}      ← the patch landed
    hook             = {chain: rel.onDone, status: failed,
                        err: "!get-path svc:meta:nowhere: nothing stands there"}
    derived entities = 0                                    ← the release note was never written

## Why it is worse than a failed firing

THE PATCH IS THE LATCH. `stage: deployed` is exactly what takes the subject out of the rule's
selection, so the rule will never fire again to try the derive. The verse now says the deploy
finished and holds no record of what shipped, and nothing will ever reconcile the two. A hook
that failed CLEANLY — writing nothing — would leave the subject where it was and the next commit
would re-fire it.

It is recorded, at least: `hook.status: failed` sits on the task record and (since af26354a) a
WARN line names it. So this is not silent. It is unrecoverable, which is a different problem.

## What it is not

Not the read-vocabulary bug: `$subject.value` and the path reads resolve in a hook now. This is
reachable from any derive that can fail at the firing — a `!get-path` at an absent entity, an
`over:` naming a field the answer did not carry — which is the ordinary way an agent's answer
disappoints a charter.

## The options, none of them free

- ORDER IT THE OTHER WAY: derive, then patch. The latch lands last, so a failed derive leaves the
  subject re-firable. It inverts the current argument (a derive is state the task is started
  about) but a hook has no spawn after it, so that argument is weaker in a hook than in an action.
- STAGE BOTH WRITES and commit them together. The store has preconditions; whether it can carry
  two addresses in one commit is a question for entity/, not trigger/.
- SAY SO AT INSTALL: a hook carrying both a patch that latches and a derive is warned, the way
  the deny-latch note works. Cheapest, and does not fix anything.

I lean to the first FOR HOOKS specifically, and to leaving an action's own order alone.