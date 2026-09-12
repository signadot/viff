# status renders a read that timed out as absence

Seen on a store saturated by the sources' first pass after a
wipe: `verse status` answered `charter: {rules: 0, strata: 0}`, `tasks: 0`, and no `sources:`
or `runners:` at all, while versed had 0 restarts, its restore phases had run in milliseconds,
and `fired: 499` and `flows: 11` — the engine's own memory — were still climbing. The store
listings behind those fields (sourceRefs, runnerRefs, taskCount, the charter census) hit their
read deadline and were rendered as empty, with `perms.unreadable` the only hint.

/state and /gates refuse a read that did not happen ("the store could not be read, so this
answer would be an empty verse rather than the verse"); status should say the same per field
rather than lie in the one place an operator looks first. Either the fields that could not be
read are named (`unreadable: [sources, runners, tasks, charter]`) and left out, or the whole
answer is 503 the way the listings are. Naming them is the more useful answer: the head, the
fired count and the flows are still true and still worth showing.