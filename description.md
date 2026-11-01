# a spawn no runner offers is refused at an install, and held only while the runners are still to come

On main (ba7d2b445, the reconciler) `Engine.vet` holds a spawn no runner offers (`ErrNoOffer`) with the cause "waiting for a runner", for every caller: an install, a check, the restore. So an author who installs `agent: {name: claud}` is answered accepted, rule held, and the rule waits for a runner that never comes. Before the reconciler (deploy/staging.27) the install refused it with the author present, and the docs still say so (docs/runners.md, docs/trigger-model.md, docs/reference/runners.md: "refused at install where no runner offers the doer"). PR #62's test expects the refusal and fails on main for this reason alone.

**The model.** A spawn no runner offers is a refusal, as every other offer failure is, wherever there is an author to hear it. The exception is a verse that is starting. The runners' offers are records, so a restore reads what each runner last described, and most spawns pass; what misses is a repopulate's runner, one registered with `--no-probe`, or a pod not up at `runner add`, which `probeRunners` fills after the restore. While the runners are still to come, a record's rule is held "waiting for a runner". Once versed has probed them, a record's spawn no runner offers is "refused: no runner offers it", pending, and arms if one registers, as a pending rule does today.

**The flag is on the call, not the engine.** An engine-wide mode would make an install or a check during startup answer differently from one an hour later. So `vet` takes it as an argument and each caller says which vet it wants:

- An install (`InstallTriggerAs`) checks the runners, always. A user's install never passes the other.
- A check (`trigger.Check`, `POST /charter/check`, `verse c --check`, the MCP tool) checks the runners by default; an explicit option asks for the restore's vet, for someone checking a charter against a verse whose runners are not up yet.
- The follow's placing of a record, and the pass's re-vet of a pending rule, hold a no-offer spawn while the runners are still to come. versed says once, after `probeRunners`, that they are known (`Engine.RunnersKnown`, idempotent, to every engine it runs and to each it makes after that), and kicks a pass. The engine has no notion of startup of its own.
- The system charters versed installs at start (`SystemCharters`, after the probe) are installs as the system: one refused for want of a runner is kept in `systemWaiting` and tried again on a runner's registration, as one refused for want of an io is on an io's.

**Breaks:** an install whose spawn names an agent no runner offers was accepted with the rule held; it is refused. A check's answer for it was `installs` with nothing held (the check's engine had no offer check at all, PR #62); it is `refused`.

Plan with the file list: scratchpad/runner-vet-plan.md in the reconciler worktree (local).