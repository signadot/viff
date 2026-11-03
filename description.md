# spawn: a spawn's strings are $[…] expressions — strict on a plain path, $HOME stays literal

The spawn half of j28egpp2h12ksbzzp9n0, and a gate's pose with it. Patches keep `$x` for now and are likely next.

## What changes

A spawn's strings (prompt, subject, agent name and settings, environment vars and system attributes) are expanded with go-tony's expressions (`eval.ExpandStringWithOptions`), not verse's own `$name` substitution. A gate's `pose` is expanded the same way, against the same environment.

Each expression is evaluated against:

- `subject`: `{ref, name, value}`. `ref` is the address and `name` its last segment. In a completion hook, `value` is the subject at the commit its rule fired at (verse PR #70), read only when an expression reads `subject` other than as `subject.ref` or `subject.name`.
- the payload's top-level fields. In a hook those are the task's `status`, `result`, `data` and `err`, each always present: `result` and `err` are `""` where the task said nothing, `data` null where its result is no document.

```tony
onDone:
  spawn:
    agent: {name: verse}
    prompt: "Open a pull request for $[subject.value.branch]: $[data.summary]. Use $HOME/.cache."
    environment: {vars: {ROUND: $[subject.value.round]}}
```

| Case | Result |
|---|---|
| everything present | `Open a pull request for feat-x: adds retries. Use $HOME/.cache.`, `ROUND=3` |
| `data` has no `summary` | plain-path strict (go-tony v0.0.251): `no "summary" in data`; the launch is refused as a failed task on record, by field, and `onFailed` runs with `$err` (PR #70's machinery) |
| `$HOME`, `$5`, `$subject.value.branch` | text: a `$` that does not open `$[` is text everywhere, at install as at the firing |
| `$[data.summary ?? "none"]` | the author's default; an expression with `??`, `?.` or a call is not a plain path |
| `$[subject]` | the subject as one-line tony, `{name: …, ref: …, value: …}` |
| `$[now()]`, or a syntax error | refuses the launch, as an absent path does; the clock is disabled (`DisableBuiltins`) |
| `.[x]` as a whole string | an expression (`eval.GetRaw`); `.[` anywhere else is text, so a jq filter reaches the agent as written |
| `$[subject.value["pr-number"]]` | a key that is not a word, by index; `$[pr-number]` is a subtraction |

Install reads a value as bound exactly where the expansion would replace it (`systems.Templated`: a `$[…]`, or a whole `.[…]`): ceilings, agent names and settings, system attributes, autonomy matches, a credential's audience, a spawn written by hand. A composite rendered into text is tony in name order. A script's text is never expanded.

## Not here

- Patches, still `$x`.
- One scanner for expressions: verse has two of its own (the ceiling's stand-in, `Spec.Reads`); tony-format y5n12a3gh12kraxfx5n0 asks go-tony to export the one it evaluates with.

## Plan

- Base: stacked on verse PR #70 (`verse/dev/hook-spawn-vars`), whose `$name` scanner this replaces.
- go-tony v0.0.251.
- Respelled: `.verse/charters`, `deploy/charters`, `system/repocharters`, `docs/`, `scripts/`, tests; verse-workshop's examples on its own branch, to merge once staging runs this; spawn strings in open pull requests as they rebase.
