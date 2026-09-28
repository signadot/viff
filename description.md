# docs: tasks.md and gates.md disagree on what a restart does to a firing's question

Two user pages say different things about a question raised by a firing (a gate, or a provider selection) when verse restarts. At least one of them is wrong.

**docs/reference/tasks.md** ("A restart settles what nobody waits on"):

> - a question that a task raised survives the restart exactly as its task does;
> - verse settles a question that a firing raised, because what waited on the answer was a goroutine in the verse process.

**docs/reference/gates.md** ("A question outlives the verse that asked it"):

> The record, its options and every ballot cast into it are entities. So a restart finds the question exactly where it stood. [...] Startup gives the awaiter back.

Its table: a **gate**'s firing is evaluated again, poses the same question again and joins the standing record, and the ballots already cast still count. A **selection** is re-entered by following the question's `chain`. Only a question that nothing can answer any more is settled.

So tasks.md says a firing's question is settled on restart, and gates.md says it stands and gets its awaiter back.

**To do:** check the code for the startup path (re-evaluation of gated firings, the selection chain resume, and what settles as `orphaned`). Then fix the page that is wrong, and make the other link to the one home.

Found during the docs repetition pass on `verse/docs-cut` (feaf959d). Both passages are unchanged by that pass.