# A task's raise: the relay carries only an ask, `due` takes a duration, a spawn stays on the task's runner and credential

Review findings on a1e22475b and f6812035e (verse/verse-system-base), to fix together:

1. **The relay works on every route.** A runner's `Verse-Task` is admitted as the task's owner on any request, and only `POST /asks` holds what follows within the task, so a relayed `POST /tasks` or `PATCH /asks/<id>` gets no limits. The relay admits `POST /asks` (and `GET /whoami`) and refuses the rest.
3. **`due: 48h` is refused.** `question-raise` offers a duration, and versed takes only an RFC 3339 date. The tool turns a duration into a date as `verse ask` does (dateOf).
4. **An edit is held by what the ask says, not by who edits.** `editBound` holds the owner's own edits of a task-raised ask within the task, and after a restart refuses them, blaming the task. With the relay carrying only `POST /asks`, no edit is a task's: the owner's edits are theirs.
5. **What is held ignores `runner` and `virtualKey`.** A then's spawn can name another runner, verse's own runtime, or another credential. It is held to the task's own.
6. **A 401 or a 403 about the runner reaches the task as a refusal.** A runner's fault (an expired token, a subject that does not match) is an error to the task, not a no.
7. **`strsIn` copies `agent.strsOf`.** One, exported.

Not taken: a then's `answer` held within the task (finding 2). A task uses the tools as a person can, and a spawn's author who does not trust the agent with it turns off the ask. Telling the forms apart in a row is the tony match issue.