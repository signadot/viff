# A completion hook reads the subject at head, and a hooked spawn holds its subject until the hook has run

A completion hook (onDone, onFailed, onCancelled, onOrphaned) reads `subject.value`, `!get-path` and `!list-paths` at the commit its rule fired at. A task that runs longer than the store's compaction cutoff loses that commit, the read is refused (ErrReplayTooOld), and the hook writes nothing: the subject stays latched at the stage the rule set, and only the log says why. Found on a local verse on 2026-11-02 (cutoff 1h, an agent that ran 2h03m). signadot/verse#76 answered it with a capture: the places the rule reads, copied onto the task's record at the firing, read by the hook instead of the store's history.

This replaces the capture with a simpler contract, proposed by Scott:

- **A hook reads at head.** `subject.value`, `subject.commit`, `!get-path`, `!list-paths`, a derive and the strings of a hook's spawn read the store as it is when the task ends. Head is always retained, so no cutoff and no restart can lose it. A sequence element resumed off a completion reads the same way, so a later element sees what earlier elements wrote.
- **A value from the firing's moment is the firing's to write.** A hook that needs the subject as the rule fired on it reads a field the firing's patch wrote beside its spawn. The one such hook on main, the repo-charters `propose` rule's `askedAt: .[subject.value.seen]`, moves into the firing's patch.
- **A hooked spawn claims its subject.** A rule whose spawn carries a hook takes a claim on (rule, subject) when it spawns, as a sequence run does with claimRun, and does not spawn again about that subject until the hook has run, cleanly or not. The claim comes off when the hook has written, when the task is orphaned, and at a restart whose resume did not bring the task back (reconcileClaims, unchanged). On release the rule is evaluated again at head, so a change made while it was held is not missed.

Why not the capture: it narrows the failure without removing it (a record from before the deploy, a rule whose read places carry a `$`, a charter edited while its task ran, a capture whose read failed at spawn), it is copied onto every status write of every hooked task, and a hook listing task records would hold the captures of every earlier run.

Kept from #76, independent of how a hook reads: a failed completion hook is written on its subject under `verse.charter.<rule>.hook` until the next clean hook of the rule; a sequence element that fails off a completion ends the run with `ended: error` and a reason; a commit that changes only a rule's own bookkeeping does not wake that rule; a scoped watch's commit carries its delta.

Contract change (Bindings in docs/reference/action.md): a hook that read a value the firing's own patch changed, such as the stage the rule set, now reads the value after that patch. Tier: trigger. Breaks: a charter whose hook reads `subject.value` for what the subject was before the rule's own patch.

Replaces signadot/verse#76.