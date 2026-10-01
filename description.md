# a spawn's `let:` binds !get-path / !list-paths reads for its strings

A spawn's strings (`prompt`, `frame`, …) are filled from one name → text map, `bindingFor(root, subj)` → `launch.Bind` (`trigger/trigger.go:1061`). `bindText` renders each value, a leaf as its text and a composite as tony. Only the subject's payload (or a hook's cause) and `$subject` go into that map. So a prompt cannot carry anything the firing is not about. A `patch` or `derive` already can, through `!get-path` and `!list-paths`, answered from the firing's snapshot at the causing commit (`trigger/path.go`, `occasion.snapshot`).

## The ask

A spawn takes a `let:` list, written like the `!let` conditions already use. Each entry names one `!get-path` or `!list-paths` read, and the name binds in the spawn's strings:

```tony
spawn:
  driver: llm
  let:
  - calc: !get-path defs.sandboxes.billing.limit-calc-prompt
  - plans: !list-paths {at: "usage.*.subscription", path: plan}
  frame: $calc
  prompt: |
    Org $subject.name, plans:
    $plans
```

## Semantics

- **Same read as a patch's.** It is answered from the firing's one snapshot, at the causing commit. A launch that is resumed (`ResumeSelections`) or released from a park reads at the run's `occurrence`, which is that same commit. It reads through the rule's reader, as its owner.
- **Same rendering as payload fields** (`bindText`). A leaf is its text, a composite is tony, and a `!list-paths` result is a tony list.
- **Known at install.** The addresses join `Trigger.outsideReads`, so a package must declare them in `reads:`, and `firingWithin` stays narrowed. `!get-path` refuses a glob, as it does now.
- **A `let` name wins over a payload field of the same name.** Payload names are not known at install, and the explicit binding should beat the ambient one, for the reason `$subject` is set last.
- **A read that fails fails the firing.** A missing node, or one refused by perms, is recorded on the rule (`trouble.go`) and does not substitute as empty text.
- **Copied, not tracked**, like every action read. Editing the def changes the next firing, and a rule that should re-run when it changes adds an `on:` covering it.

## Not asked

- `$<store path>` directly in a string. The substitution is a flat `ReplaceAll`, so it cannot tell a store path from a payload field, and the read would not be known at install.
- Expressions (`.[…]`) in actions. That is a separate question: whether a cap or band is computed in verse rather than by a model.
