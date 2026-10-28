# An autonomy row matches a step's shape with a tony match, not only its fields by glob

## The problem

An autonomy row's `match` is verse's own matcher (`task.Match.Matches`, task/autonomy.go): a system, a verb, and glob patterns over a step's fields. A step's fields are a flat `map[string]string` (`task.Step`). So a row can decide only what a string field can say.

`question.raise` (tzved3g6h12ks7y3w5n0) shows the limit. A task raises an ask as its owner with a `then` and an `else`, and a spawn's author may want to allow a raise whose `then` spawns but not one whose `then` answers another of the owner's asks. Today no row can say that: the step has no fields. The shape that matters is nested, too: `then: {ask: {then: {answer: …}}}` names `ask` at the top, so a field holding the top-level form would be easy to get around.

Made-up fields would cover this one case: runner-computed booleans `spawns`, `answers` and `asks` over `then` and `else`, nested asks included, like git push's `exists` and `own`. But the next verb with structured input would need its own, and it is mechanism added before the model.

## The direction

A step carries what it is as a document, and a row's match is a tony match over it:

```tony
- match: {system: question, verb: raise, then: {answer: …}}
  then: deny
```

A match on the nesting is just another pattern, and nothing per verb is invented.

## What it touches

- What a step is: `task.Step.Fields` becomes, or sits beside, a document the runner fills from the call; the runner side computes it from the act, never from what the task says (delivery's `s.refused`).
- Every reader of a row: `Matches`, `covers`, ceilings, `Autonomy.Within` (a then's spawn held no looser than its task), and the check that refuses a row that can match nothing the environment gives (systems.Accepts and forms).
- Systems documents: a verb's fields and forms, and how a tony match is checked against them where it is written.
- Docs: tasks.md "The autonomy", the systems pages.

## Part of

The plan and design for verse system work. This is bottom-up: the coding on verse/verse-system-base drives it. To sync with Daniel (runner face) before it is built.