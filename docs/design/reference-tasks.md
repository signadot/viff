# Why: Tasks

The reasons behind [Tasks](../reference/tasks.md). They are for somebody changing viff; the page itself says what viff does.

## Why a script gets its data in `environment.vars`

A value spliced into a script's text is part of the script, so a value with a quote in it can
change what the script does. A variable cannot: `environment.vars` sets it beside the script, and
`viff-call-tool` takes it as a flag's value, whole.

## Why a script's calls go through the gates

A script is one action and no step, because viff decided to start it. What it does beyond its
workspace is a call to the task's tools, and each call is the step an agent's use of the tool
is: the one action with an effect outside viff is not the one action that nobody can gate.

## Why a question outlives its answer

A docket that forgets cannot be audited.

## Why a losing answer is recorded

A docket that keeps only the winner cannot show that a question was contested. The `409` and
the `404` differ so that a caller can tell a typo from a race.

## Why the question is its own entity

A gate does not always belong to a task. A firing poses a rule's gate before anything spawns.
If somebody denies that gate, nothing ever spawns.
