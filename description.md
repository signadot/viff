# spawn: a spawn's strings are $[…] expressions — strict on a plain path, $HOME stays literal

The spawn half of j28egpp2h12ksbzzp9n0. Patches keep `$x` for now and are likely next.

## What changes

A spawn's strings (prompt, environment vars and attributes, agent name and settings) are expanded with go-tony's `$[…]` (`eval.ExpandString`), not verse's own `$name` substitution (`launch.Bind` over `bindingFor`'s flat map of texts).

Each expression is evaluated against:

- `subject`: `{ref, name, value}`, the parts a patch's `$subject…` has. `ref` is the address and `name` its last segment. In a completion hook, `value` is the subject at the commit its rule fired at (verse PR #70).
- the root payload's top-level fields, as now. In a hook those are the task's `status`, `result`, `data` and `err`.

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
| `data` has no `summary` | plain-path strict (tony-format 5860f534) errors with `no "summary" in data`; the launch is refused as a failed task on record, and `onFailed` runs with `$err` (PR #70's machinery) |
| `$HOME` | literal: it isn't `$[` |
| `$[data.summary ?? "none"]` | the author's default; an expression with `??` isn't a plain path |
| `$[subject]` | refused: an object; the address is `$[subject.ref]` |
| `$[now()]`, or a syntax error | refused at install: each `$[…]` compiles when the charter installs, with builtins disabled (`DisableBuiltins`) |
| the old `$subject.value.branch` | refused at install, naming `$[subject.value.branch]` |

At install, only the names verse binds (`subject`, `status`, `result`, `data`, `err`) can be recognised in the old spelling. Any other `$word` stays as written, since nothing at install can tell it from `$HOME`. A composite rendered into text is tony (`Composite`), as `bindText` renders it now. A script's text is still never expanded.

## Plan

- Base: stacked on verse PR #70 (`verse/dev/hook-spawn-vars`), whose `$name` scanner this replaces.
- go-tony: 5860f534 (plain-path strict) with 8bc03d32 (`DisableBuiltins`, `Composite`, brackets ending at their own `]`, `$$[`). A pseudo-version while building, then a release before merge.
- Respelled: `.verse/charters`, `deploy/charters`, `docs/reference/examples`, `docs/reference/action.md`, verse-workshop's examples, and spawn strings in open pull requests.
- A gate's `pose` already uses `$[…]` but puts the address at `subject.name`. That's left as is here and noted on j28egpp2.