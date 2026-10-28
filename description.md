# versed: review fixes to the task charter hold — a scoped task's rule, a script's credential, the check's verdict

Review of 86259b702..8b9c7c453.

1. **A scoped task's charter never fires.**
   - A task spawned in scope X has its record in X's layer.
   - Its install carries no `?scope`, so it goes to baseline (whether it should is the open scope-parity question, not decided here).
   - At firing, `heldByItsTask` reads the record through the baseline store. It finds nothing and refuses every firing.
   - Fix: the rule records the task's scope beside its id (`taskScope`), and the firing reads the record there.
2. **A task with a virtualKey cannot install a script rule.**
   - `holds` requires the spawn's key to equal the task's, and a script spawn's is empty. The ask hold (taskBound) has the same rule.
   - A script spends no credential: the stock runner's sub-agents are the verse agent's alone.
   - Fix: a prompt spawn names the task's credential; a script names none or the task's.
3. **The check answers a 403 for a task with no launch on record.** Its contract is that a refusal is the verdict. Fix: fold it into the verdict.
4. **Duplicated meanings:**
   - `ruleOfSpawn` re-cuts the chain the engine already cuts (Engine.Principal, chainRule). Fix: one engine method.
   - `verseSystem`/`charterInstallVerb` restate task.StepVerseCharterInstall.
   - lessCharterInstall is applied at three sites over `launched.environment`. Fix: one method on launched, the reach a task hands on.
5. **The verse.tony charter-remove description** omits schedules, which the verb removes.