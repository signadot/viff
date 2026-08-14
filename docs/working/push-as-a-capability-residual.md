# Confinement: `principal: runner` says the quiet part, and nothing checks the rest

Status: residual of [push-as-a-capability.md](push-as-a-capability.md), which proposes putting a
push credential in the task process. Everything here is about whether that process actually
contains one, and none of it is written.

## What the Point already says

`confine.go`'s vocabulary has a leaf for this and it is being read as stronger than it is:

```go
"principal": {"runner", "distinct"}     // chains
"principal": "runner"                   // bwrapProvides — the ceiling
```

**`principal: runner` means the task process and the agent's subprocesses are the same
principal.** That is the honest coordinate, it is what bwrap delivers, and it is the whole of
what the strongest available confiner claims. `--unshare-user` maps the caller to itself, so the
model's shell runs as the same uid as `coderunner -task`.

A secret in the task process is therefore not separated from the model's shell by the principal.
It is separated by yama — `ptrace_scope=1` permits tracing descendants and not ancestors, and the
shell is a descendant — which is a host sysctl, outside the Point, and unverified.

Staging reads `ptrace_scope=1`, `uid=65532`. So it holds today. Nothing says it must.

## Two things to do, and they are not alternatives

### 1. `Provides` should refuse a point it cannot actually deliver

This is the discipline `confine.go` already applies at the network leaf, and it applies for the
same reason:

> a confiner that cannot unshare the network REFUSES the launch rather than running the task with
> a proxy it is free to ignore

`bwrapConfiner.Provides` asserts namespaces and mounts and says nothing about ptrace. Reading
`/proc/sys/kernel/yama/ptrace_scope` is one file:

| value | meaning |
|---|---|
| absent | no yama — the barrier does not exist |
| `0` | classic ptrace: any same-uid process may attach, ancestor or not |
| `>= 1` | descendants only, which is what the containment depends on |

At `0` or absent, a `principal: runner` point does not contain a secret in the task process from
the model's shell — and the launch proceeds anyway, silently, which is exactly the failure mode
the network leaf is written to avoid. It should refuse, in the same voice.

The awkward part, and the reason this is a residual rather than a patch: **what should it refuse
against?** Nothing today declares "I hold a secret this process must contain", so a bare check
either refuses launches that never needed it, or refuses nothing. The honest shape is a leaf —
something like `process.peek: {allowed, denied}` — that an environment holding credentials asks
for and bwrap answers by reading the sysctl. Then the refusal is about a stated requirement
rather than about a global opinion.

**Half of it is done, and it is the half that costs one file read.** The runner now READS the
sysctl at startup and says what it found, in a sentence that names what is at stake rather than
reporting a kernel setting (`coderunner/confine.go`, `ptraceScope`). That does not enforce
anything and is not meant to: it turns the assumption from unverified into merely accepted,
which is a different thing. The refusal is still owed, and still wants the leaf above.

### 2. `principal: distinct` is the thing that removes the question

A different uid for the agent's subprocesses makes same-uid ptrace not arise, whatever yama is
set to. The leaf exists; nothing provides it — `bwrapProvides` caps at `runner`, and
`hostProvides` at the weakest rung of everything.

bwrap can map a uid, so this is not out of reach in principle. What it costs is that the
workspace, the mirrors and every file the agent writes are then owned by one uid and read by
another, which is a real change to the checkout and delivery path rather than a flag.

Worth noting which way round these go: (1) makes the current arrangement honest, (2) makes it
unnecessary. Doing (1) first is right, because it turns an assumption into a stated requirement
that (2) would then satisfy properly.

## What raises the stakes rather than creating them

The LLM credential is in the task process today and depends on exactly this. So neither item is
introduced by the push proposal — it inherits them, and puts a second, more consequential secret
behind the same unverified assumption.

That is the argument for doing (1) whether or not the push change ever lands.
