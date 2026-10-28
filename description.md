# tasks.md: a firing's question at a restart is settled orphaned, then posed again by its rule

docs/reference/tasks.md, under "The question record", says at a restart "Verse settles a question that a firing raised, because what waited on its answer was inside the verse process". That is the first half. Startup does settle a firing's pending question as `orphaned` (versed reconcileQuestions). But if its rule still selects the subject, the reinstalled charter poses it again under the same id, and the re-pose of an orphaned record keeps the ballots already cast (mirror clearPriorAnswer). Read alone, the sentence says the question is gone.

Say both halves, as task-control.md and gates.md do, and link to gates.md's "A question outlives the verse that asked it".