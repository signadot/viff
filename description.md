# charter: two reference grammars — retire verse's $subject deref in favour of go-tony's $[…]

Status: proposal, nothing here runs.

## What is wrong

A charter has two reference grammars, and they are not the same one.

**Verse's own**, in `trigger/deref.go`: `$subject.value.sha`, `$result.findings[0].severity`,
resolved by handing the text after the `$` to `ir.Node.GetKPath`. It applies where the code says
it applies — inside a `patch`, inside a spawn's string fields — and it NAMES a value rather than
computing one, deliberately: "it is a deref rather than a template language".

**go-tony's**, in `eval`: `$[…]` and `.[…]`, evaluated with expr-lang against an environment,
where a string that is exactly one `.[…]` is a reference whose value replaces the node and keeps
its tag. It is what `!eval` does, and what `gates: [{pose: …}]` now uses (`d7a52bc6`).

So an author writes `patch: {sha: $subject.value.sha}` on one line and
`pose: "… $[subject.value.sha] …"` on the next, about the same field of the same subject. Two
spellings, two implementations, two sets of rules about where each is allowed.

## Proposal: keep go-tony's, retire verse's

One grammar to learn, one implementation to maintain, and the expressive half comes for free —
a conditional, a default (`?? "unknown"`), a join, a comparison — none of which a deref can do
and all of which a charter author eventually wants.

The direction was already taken when `pose` was built on `$[…]`; this is the other half of it,
and until it lands the two spellings sit beside each other in every gated rule.

## What it touches, and the questions it has to answer

- **Where references are resolved today** — `trigger/deref.go` (the walk, `refPayload`, the
  `fromSubject` / `fromCause` split), the spawn's string fields, and the patch expansion in
  `trigger/path.go` / `deref.go`. The environment go-tony would be given has to carry what each
  of those carries: the subject, and for a completion hook `$result`, `$err`, `$data`.
- **A reference that is not a string.** Verse's deref writes a value TYPED onto the subject —
  that is the point of it ("a task's output becomes a fact the next `when` matches on"). The
  equivalent is `.[…]` as a whole value rather than `$[…]` interpolated into text, so the
  conversion is not uniform: `{sha: $subject.value.sha}` becomes `{sha: .[subject.value.sha]}`,
  not `"$[subject.value.sha]"`.
- **What the environment may reach.** A charter runs as the system. The expressions are wanted;
  the eval OPERATIONS (`!exec`, `!file`, `!script`, `!osenv`, `getenv`) are not, and `getpath` /
  `listpath` would be an untracked read beside verse's own `!get-path`, whose read-set semantics
  a condition depends on (`trigger/condread.go`).
- **Every charter written so far**, including `scripts/charter-shapes.sh` and the docs' examples.
  This is a rewrite-and-declare rather than a migration — but it is a contract change to the
  largest surface there is.
- **Whether both spell it for a while.** Cheapest is a straight replacement with the old spelling
  refused by name, pointing at the new one; a period where both work is the thing that makes two
  grammars permanent.

## Why not the other direction

Extending verse's deref to do what `$[…]` does is writing an expression language, which go-tony
already has, tested and documented, and which the rest of the tony ecosystem already speaks.