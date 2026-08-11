# A coding runner: a workspace per task, and an agent in it

Status: **partly built** — the program under `docs/reference/examples/coderunner` runs, with
the containment stubbed at its weakest rung and the push ungated (both said so below). It is the
consumer half of
[runner-descriptor.md](runner-descriptor.md) — the containment vocabulary that note designs is
what this one needs a name for, and the descriptor is where the name is published.

## What it is

A second example under `docs/reference/examples/coderunner`: a runner that serves the
verse-facing [task-control](../task-control.md) contract, gives each task its own git checkouts,
and runs the work in it — either by the [LLM harness](https://github.com/signadot/verse/tree/main/task/harness) already in the tree, or
by invoking an external program — a coding agent binary, a script — named in its config.

**A second example rather than a bigger first one.** The existing sample
([reference/examples/runner](../reference/examples/runner/main.go)) is a hundred and thirty
lines whose whole argument is that it is *the smallest thing that serves the contract*, with the
two functions you change marked `CHANGE THIS`. Git, workspaces, credentials, two drivers, a
command allowlist and a containment ladder do not fit inside that argument, and folding them in
would destroy the teaching device — the marked functions stop being the two questions a runner
answers and become two of a dozen things to configure. Keeping both keeps a ladder: *here is the
contract*, then *here is what a real coding runner does with it*. Both compile with the tree, for
the reason the first one does — a stub in a fenced block goes stale the first time a signature
moves and nobody finds out.

## The workspace is the axis that already exists

A coding task needs to know which repository, at which base, and where its work should go. None
of that is on a spawn today, and the temptation is to add it: `repo:`, `branch:`, `push:`.

That is the second place. [runners.md](../runners.md) closes it:

> **A runner entity carries a url and nothing else.** A grant ceiling, a tool allowlist, a label
> — anything saying "work here may only do this" — is expressible and deliberately absent. It is
> the same narrowing a doer answers, and answering it in two places without deciding how they
> compose is how a ceiling comes to mean two things.

And `launch.Spec` already declares the axis, with no referent:

```
Vector      → the gate vector (the autonomy dial) — soft, slidable      resolved
Grant       → hard authority — what it may do at all                    resolved
Resolver    → how ASK gates are answered                                carried
Environment → the sandbox/workspace floor — unbypassable                read by nothing
```

`Environment` is parsed, `$var`-expanded by `Bind`, persisted, and rendered back by `trigger
show`. `versed.launchOf` says plainly why it goes no further — *"neither has semantics to resolve
TO yet … the environment floor is a design note"* — and [expose.md](../roadmap/expose.md) says
the same from the other side: *"a declared axis waiting for a named, durable, registered thing to
point at."* Even [task-control.md](../task-control.md) reserves a slot in the spawn body,
`{id?, prompt, subject, vector, claim?, runner-config}`, and `runner-config` appears nowhere in
the tree.

**So the coding runner is the first referent, and `environment:` is a name it resolves against
its own config.** Carried across the wire unresolved, exactly the way `driver:` already is: verse
does not know what drivers exist either, it passes the word, and a runner that cannot host what
it was handed refuses. The charter says *where the work is*, the runner says *what that means
here*, and the vocabulary lives with the party that can implement it.

The cost on the verse side is four lines — a field on `task.Launch`, a key in
`runner.HTTP.Spawn`'s request map, a field in `taskd`'s decode struct, and one assignment in
`launchOf`. No new charter grammar.

```tony
# the runner's own config, not an entity
environments:
- name: verse-dev
  repos:                                # a LIST: see below
  - name: verse                         # the directory the agent sees
    repo: "https://github.com/signadot/verse"
    base: main
    push: "refs/heads/agent/$task"      # empty: nothing leaves
  - name: tony-format                   # there to be read; no push
    repo: "https://github.com/signadot/tony-format"
    base: main
  driver: claude                        # or: llm
  commands: [git, go, make, gofmt]      # the ceiling; see below
  isolation: hardened                   # a profile this runner advertises; absent = its defaults
  env: [PATH, HOME, LANG]               # passthrough allowlist; everything else is unset
```

## A workspace per task, over several repositories

The runner keeps one bare mirror per repository and fetches before each spawn. A task gets a
working directory holding **every repository its environment names**, each cloned off that
mirror onto `agent/<taskid>` — its own branch from the first commit, so nothing a task does can
touch another's.

**Through go-git, not the git binary**, and the tradeoff is worth stating because it was not the
first design. Shelling out inherits an ambient world: whichever `git` is on `PATH`, whatever
credential helper the machine has, whatever `~/.gitconfig` says. The credential is the one that
matters — a runner whose whole delivery model rests on the task being unable to reach the
credential should be able to say where its own came from, and with a library it must
(`-token-env`, and nothing else works).

The cost is that **go-git has no linked worktrees**, so a task gets a clone rather than a
worktree and objects are copied per task. What the mirror still saves is the network fetch, which
is the expensive part. The next section is what the alternatives measured.

One thing got simpler in exchange, and it is not nothing: **with linked worktrees the task
branches live in the mirror's ref namespace**, so a `fetch --prune` on the next spawn deletes the
branch of every running task. Nothing errors; a worktree is just left on a ref that is gone, its
tree reads as entirely untracked, and its work cannot be committed. That defect was real, and a
clone per task makes it structurally impossible — task refs live inside the directory that gets
removed. A test guards the property anyway.

### What the prior art does, and what it costs here

**Nobody automating checkouts at scale uses worktrees for it.** The mainstream CI answer is a
bare mirror plus `git clone --reference` — object *alternates* — which is what Jenkins' reference
repositories and Buildkite's git mirror do. The reasoning is the one this design arrived at from
the other side: a worktree shares more than the object store. It shares the ref namespace, and a
branch cannot be checked out in two worktrees at once. That extra coupling is exactly what
produced the prune defect above.

So the prior art says alternates. Measured on this repository, in go-git:

| approach | objects per task | setup | |
|---|---|---|---|
| clone from the mirror | 3.2 MB | 0.55 s | what is built; ordinary git, well-travelled code |
| `CloneOptions{Shared: true}` | **3.2 MB** | 0.59 s | writes an alternates file that saves nothing |
| hand-rolled alternates, no fetch | **81 B** | 1.7 s | the real saving, and three times slower |

**`Shared: true` is a trap and is worth writing down.** It looks like the porcelain for exactly
this, and it does write `objects/info/alternates` — then fetches, and because a fresh repository
advertises no refs the fetch copies every object anyway. You get the file and none of the
benefit. The real saving needs the alternates file written by hand, the ref set straight off the
mirror, and **no fetch at all** — at which point it is three times slower, because go-git's
alternate lookup is a per-miss fallback that reconstructs the alternate storage on each miss
rather than holding a second store open. It also needs `filesystem.Options.AlternatesFS`, without
which an absolute alternate path is resolved relative to the `.git` directory and nothing is
found.

**The clone stays**, and the reasoning is cost against want rather than principle. The disk cost
is proportional to repository size and this example ships against small ones; swapping in
alternates is one function when somebody meets a monorepo, where 3.2 MB becomes gigabytes and the
argument inverts. Until then it would trade the well-travelled path for a hand-rolled storage
construction, in a file whose job is to be read.

Partial clone (`--filter=blob:none`), where git itself went, is not reachable: the filter exists
in go-git's protocol layer and is not plumbed to `CloneOptions`.

### The axis this does not touch: reuse

The bigger thing prior art suggests is not cheaper cloning but **not cloning**. GitLab's
`GIT_STRATEGY=fetch` and Jenkins' workspace reuse keep a warm checkout per runner and reset it
between jobs, and that — not clone speed — is how CI actually gets fast. It is the same idea as
the `SandboxWarmPool` [expose.md](../roadmap/expose.md) already cites from Agent Sandbox:
pre-provisioned instances so a claim does not cold-start.

It is also in **direct tension with containment**, which is what makes the isolation classes worth
having. A reused workspace is a channel between tasks: it undermines `principal: distinct` at
precisely the point that class exists to establish, and no amount of `reset --hard` closes it,
because the valuable part of reuse is the part that survives a reset.

That valuable part is the **build cache** — `node_modules`, a warm Go build cache, a populated
module cache. For a coding agent it is the difference between a two-minute task and a
twenty-minute one, and it is the same cross-task channel wearing a performance argument. Which is
the point: it should be a thing an environment *declares*, weighed against the containment it is
asking for, rather than something a runner arranges quietly because it makes the demo faster.
Nothing here builds it, and the shape it would take is a warm pool keyed by (repository, base)
with the pool's reuse policy named in the environment beside `isolation:`.

**A set rather than one repo, because the work usually is.** A change to a service and the
library it consumes; a generator and the code it generates; a tree being changed beside one that
only needs reading to make sense of it. Side by side under one root, that is one task rather
than two that have to be coordinated — and an agent that can only see one of them is guessing
about the rest. `push` is **per repository** for the same reason: the ordinary shape is one tree
being changed beside reference trees, and a reference tree that could push back is a blast
radius nobody asked for.

A repository that cannot be opened fails the whole workspace rather than yielding a partial one:
an agent handed three of the four it was promised does not know that, and reads an absent
directory as "this code does not exist" rather than "I was short-changed".

Removed on terminal, with `-keep` for the case an operator is debugging — one directory tree and
nothing else, since a clone per task keeps every task's refs inside what is being removed. A
branch outlives the workspace only if it was pushed; otherwise it goes with it, and *that is the
reason the push question cannot be deferred*. A coding runner that cannot push produces nothing durable: the work is in a
directory that is about to be deleted, and the task's `result` is a description of changes nobody
can read.

### One thing that does not fit

`task.RepoContext` is **singular** and a workspace is not. Root and ChangedFiles generalize
cleanly — the root is the working directory and paths are relative to it, so `verse/task/loop.go`
says which repository it is in, which is also what a charter's `paths:` predicate should be
written against. Branch does too, since every repository carries the same task branch. `Base` and
`PushTarget` do not, and today they report the **primary** (first) repository's. A workspace whose
repositories have different bases and different push targets is exactly the case that wants a
plural `RepoContext`, and nothing in the tree has needed one yet.

### The agent does not push

Once the task runs as a different uid than the runner (below), the git credential is unreachable
from it by file mode. So **fetch and push are runner actions, and the agent only ever touches the
files.** That is better than it sounds:

- **`git` can be absent from the task's PATH — and from the image — entirely** without costing
  the task anything it needed. The agent edits files; the runner commits and pushes what
  changed, through a library rather than a binary.
- **The push becomes a gateable effect rather than a side effect nobody declared.** The runner
  raises it as an action at a boundary, so the vector decides — and `task.RepoContext` is already
  built for exactly this, carrying `Root`, `Branch`, `Base`, `ChangedFiles` and `PushTarget`,
  with `GateMatch.OnPush` and `GateMatch.Branches` matching on it. Nothing in the tree sets it
  today outside `task/vector_test.go`; `Task.SetRepo` exists and has no production caller. This
  would be its first.
- A charter can then write `vector: ask` on the push and `auto` on everything else, which is the
  shape a coding flow actually wants: work freely, ask before anything leaves.

The alternative — the agent runs `git push` in its shell — makes the push a string in a command
line, indistinguishable from any other string, and puts the credential inside the blast radius of
every prompt injection in every issue body the agent reads.

**And the remote must be pinned, not named.** This is where the first cut was wrong, and the
defect is general enough to be worth stating rather than fixing quietly: `.git/config` lives
*inside* the workspace — the one place the agent is meant to be able to write — and go-git
resolves a remote *name* through `Repository.Config()` at push time, re-reading that file. So a
push by name goes wherever the agent last wrote, carrying the runner's credential to a host of
the agent's choosing. Verified, then fixed by building an in-memory remote from the configured
url, with a test that writes the attack.

The general form: **anything the delivery path reads out of the workspace is agent input.** The
credential split says the task cannot reach the credential; it says nothing about the task
reaching the *configuration* that decides where the credential is spent, and the second is as
good as the first if you are the attacker. Worth re-asking of every field delivery consults.

What it costs is attribution at the far end: the push lands under the runner's own identity, so
the external record names a bot and the link back to a person lives only inside verse. That is
the first time verse-caused effects leave through a credential it does not hold, and
[attribution.md](attribution.md) covers it — including the cheap channel back, which is that the
branch name and the commit trailers can carry the charter, the firing and the task id, checkable
by anyone who can read both the repository and the verse.

### The push is an edge, not an output

The deeper reason to take the push off the agent is not credential hygiene. **It is the moment
the work becomes a fact other charters react to.** A [git source](../sources.md) reflects the
branch, a charter conditions on it, and what follows — review this, here is a bug, fix it — is a
flow that continues from the push. The push is the edge into all of it.

Which makes where it happens a design question rather than a detail. The first answer this
example gave was wrong in a way worth keeping written down: **the push happened after the
terminal.** `finish` waited for a terminal status frame and only then delivered, so the branch
appeared after verse had already recorded the task as done. Three consequences, and the first was
the only one already noticed:

- **It is not gated.** The vector never sees it, `GateMatch.OnPush` never fires, and
  `RepoContext.PushTarget` — set for exactly this — is decoration.
- **It is not recorded.** The task's own read plane, and therefore its entity, never names the
  branch it produced. `verse task get` cannot answer "where did this work go".
- **It is not a cause.** A downstream flow chains from a ref appearing in a source reflection,
  not from a boundary in the task that made it — so verse reacts to an event it did not observe
  itself causing, and the connection has to be re-derived rather than handed over.

Inside the loop, before terminal, all three close at once: an action the vector gates, a boundary
the read plane records, and a cause the next flow chains from — with `$result` still in hand when
it happens.

#### The seam, built

`task.Finisher` is it, and it landed in `task` rather than in `harness` because it is about
drivers and executors, which are `task`'s — the command driver in this runner needs it as much as
the LLM one does. `harness.Config.Finish` passes one through, since `harness.Build` returns a
finished `*task.Task` and exposes neither piece; composing from outside would have meant
reimplementing the harness, which is what this example exists not to do.

**An action, not an `OnComplete` hook.** The hook is the obvious shape and it is a fourth thing to
supervise. An action is already supervised: the vector gates it because it is an action, the read
plane narrates it because it is an action, it happens before terminal because it is an action.
Nothing new had to learn about delivery.

`push` is now a verb. `write` on a `KindGitRef` would have worked and would have been caught by
every `verbs: [write]` gate anybody wrote about files; a charter that wants to stop work leaving
should be able to say that and nothing else. It is also the only verb here whose effect is not
undoable from inside the verse.

**The awkward part is that the task is in another process and the credential deliberately is
not.** So the action is proposed and gated on one side of the boundary and performed on the other:
the child's executor asks the parent over an inherited *connected* socket (fd 5), and the parent
commits, pushes, and answers with the refs — which become the action's output, on the read plane,
before terminal. Inherited rather than a path in the sandbox, unlike the egress socket, because
nothing but that process should be able to ask: a `deliver` endpoint the agent's shell could reach
is a delivery that skips its own gate.

That channel is not the task-control contract and does not pretend to be. It is a capability the
parent holds and the task may ask it to exercise — one operation, no arguments that change where
anything goes. Where the work lands is the environment's to say and the trailers are the runner's
to write, so asking cannot redirect anything.

Three things fall out. A refused delivery now means the runner does not push — previously
unsayable. A failed delivery **fails the task**, instead of being a line in a runner's log. And
`supervise` no longer delivers at all: it only decides about the workspace, from what the task
asked for, so "nothing was delivered" is a fact about the run rather than about that function's
ordering.

#### Reflecting through the mirror, which is the next question

The suggestion is to have the runner publish task branches into its own **mirror** and reflect
the mirror to origin, rather than pushing from the task's clone straight out. It is better than
what is there, for reasons that are not about tidiness:

- **The workspace stops being the only copy.** Today a delivery that fails keeps the workspace
  precisely because `os.RemoveAll` would take the object store with it. If the work were in the
  mirror, the workspace could always go and the retry would still have something to push.
- **It makes delivery retryable at all.** A failed push is currently terminal for that work.
- **It is verse's own shape.** A source reflects the world in; a runner reflecting agent branches
  out is the same relation with the arrow reversed, which is an argument for it beyond mechanics.

Two costs, and one fact discovered while checking:

- Two stages means two places a ref can be, and `verse task get` has to say which — more states,
  and a reflection loop has its own retry policy and its own failure modes.
- The deploy's mirror is an `emptyDir`, so "durable" there is only relative to the workspace.
  Worth a PVC if this lands.
- **go-git's local transport shells out.** `transport/file` runs `git-upload-pack` /
  `git-receive-pack`, so *the mirror→clone step already depends on the git binary being on the
  runner's PATH* — `workspace.go`'s headline claim that this speaks go-git and not the git binary
  is true of the network remotes, where the credential argument lives, and false of local paths.
  Verified by shimming those two binaries: the clone fails. A mirror-staged delivery would add a
  second local push and so lean on it harder, which is fine in the image and a thing to say out
  loud rather than discover.

Not built. It is independent of everything above — the gated action is the same action either
way, and only what the parent does when asked would change.

**The gap downstream is now closed.** `source/git.go` formatted recent commits with `%s`, so the
commit body never reached a `git:commit` entity and every trailer this runner writes was
invisible to any charter conditioning on the push — leaving the branch name as the only
provenance that survived into the verse.

The fix was not `%B` on the payload, which would have warehoused an entire message per commit
*and* broken the parser (it reads one line per commit, so three commits became seventy-nine
lines). It was `%B` as **input**: `git log -z` so NUL separates records, the message read and
the trailer block parsed, and only the trailers stored. `git:commit` now carries `trailers` when
a commit has any, so `Verse-Task` and `Verse-Chain` are matchable and the flow that reviews this
runner's work can find it by what it declared.

Every key is reflected and none is interpreted, which is the right division: a source cannot
tell a meaningful trailer from an accidental one better than the repository can, and the block
is a heuristic over the last paragraph — a message merely ending in something shaped like
`Status: proposal` yields `Status`, in git's own parser too. So the convention lives in the key
**namespace**, which is why this runner's trailers are `Verse-*` and not bare words.

## Two drivers

`driver:` selects, per spawn, as it already does. Both build a task whose `Root` is the workspace,
so every repository in it is reachable by a relative path.

**`llm`** is `harness.Build` with `Root` set to the workspace — the loop already in the tree,
unchanged, with `read_file` / `list_dir` / `write_file` / `run_bash` confined to it. Every action
is one the vector sees, gates and narrates.

**Everything else is a `command`**: an external program named in the runner's config under
`agents:`, invoked with the prompt, its output taken as the result. A coding agent binary is one
such program; so is a script.

That is a fold, and it replaced a driver called `claude`. From verse's side the two shapes were
identical — one opaque action, a subprocess, an exit status, some output — and keeping them
apart meant **two subprocess spawners**, so two places to apply containment, build an
environment and choose a working directory. The duplication had already cost something: the
`Confiner` was applied on the agent path and not on the harness's `run_bash`, so the one tool
that can leave a workspace was the one not covered. One path is one place to get that right, and
`claude` is now a config entry rather than a type — nothing in the runner knows what a coding
agent is.

What is deliberately **not** folded away is the ability to RE-INVOKE. A driver that runs once
and completes cannot honour a steer (nowhere to put it) and cannot repair an answer that missed
its contract (cannot ask again). Those are the same capability, and it is generic rather than
anything to do with agents — a `resume:` in the config, and a program that can continue a session
gets steering and contract repair for free. A program that declares none gets one turn and
**says so** when a steer arrives, rather than dropping it.

The rest of what the command driver does is the shape any opaque leaf needs:

- **One action per turn.** verse gates the invocation, not what the program does inside it.
  Nothing can gate inside a subprocess, and saying so is better than implying a supervision that
  is not there — the `CallAgent` verb and `AgentRes(name)` exist for exactly this.
- **Cancel is process death.** `exec.CommandContext` kills the turn, so control is applied
  synchronously, which is what the contract assumes of anything behind a url.
- **`CallAgent` on the ToolUse is left nil**, because that field is not a label: it is the
  instruction that routes an action to the loop's child-spawn path, which needs a `SpawnFunc`
  this runner does not have.

`Result()` is the program's last output, which is what a completed task's `result` carries and
what a charter's `$result` derefs — so the frame advice in `harness.defaultFrame` about the final
message being the answer applies here too, and has to be passed to the binary rather than assumed.

## Commands: a ceiling, an opt-in, and one honest enforcement

A coding image carries a lot: compilers, test runners, linters, package managers. Opting in per
task is right. The question is which layer holds the opt-in and what actually enforces it.

[expose.md](../roadmap/expose.md) already draws the layers — *possible ⊇ granted ⊇ auto* — and
commands split across the first two:

| | | |
|---|---|---|
| **the image** | what exists at all | a property of the deployment; the environment's ceiling |
| **the spawn** | what this task opted into | request-specific, so it belongs to the claim, not the definition |
| the vector | which of those must ask | already built |

The middle row is request-specific data, and `expose.md` is explicit that this matters: *"an
environment record has to stay free of request-specific data … a derived grant belongs to the
claim, not the definition, or environments stop being shareable and pre-warmable."*

### Matching on the command string is theatre

`grant:` can nearly express the opt-in already. `harness.demoDeriver` derives `run_bash` into
`Effect{Verb: Exec, Resource: {Kind: KindTask, Ref: <the command>}}`, and `ResourcePred` globs on
`Resource.Ref`. So this type-checks and runs today:

```tony
grant: { allow: [ { resources: [ { kind: task, pattern: "go test*" } ] } ] }
```

And it is worth nothing. `go test`, `x=go; $x test`, and `make test` are the same action past a
glob, and every one of them is a string the agent chose. `task.ToolUse`'s own doc names this as
the thing the model is built to avoid — it separates "what the task *asked for* (Name/Input,
self-reported and untrusted for classification)" from "what it *actually does* (Effects, derived
by the platform)" — and then `Ref` here *is* the raw self-reported string. A permission enforced
by globbing it is advice wearing a permission's name.

### So: PATH is the allowlist

The opted-in set becomes a directory of symlinks to exactly those binaries, and that directory is
the task's entire `PATH`, over a filesystem where the rest are not reachable. A command outside
the set fails as *not found*.

That puts it on the right side of `expose.md`'s line — *denials belong in the ledger; absences
belong in the definition* — and the cost is real and should be stated rather than discovered: an
absence produces no gate, no decision record, and nothing on the docket. It appears only as a
failed command inside the agent's own output. **Present-and-gated and absent-entirely are
different mechanisms with different observability, and a design that pretends one covers both
will surprise someone.** The rule of thumb: gate what a task legitimately does and a human should
weigh (the push, a write outside the workspace); make absent what it has no business reaching for
at all.

An absolute path defeats a `PATH` allowlist, so this is only a floor when the filesystem is
restricted too — which is the next section, and is why the two cannot be designed apart.

## Isolation, and the refusal that keeps it honest

Containment is **verse's vocabulary, provided by the runner** — a tree of properties whose leaves
are ordered chains, sketched in [runner-descriptor.md](runner-descriptor.md). What this runner
provides at each leaf, and what each rung actually is here:

| leaf | rung | what it is on this runner | what it costs |
|---|---|---|---|
| `filesystem.scope` | `shared` | a checkout per task in a shared rootfs | tidiness, not isolation |
| | `private` | a mount namespace: only the workspace and the toolchain | Linux, and a kernel that allows it |
| `filesystem.readOnly` | `no` | the workspace is writable, which the work requires | — |
| `principal` | `runner` | the task runs as the runner does | no separation, and the credential is reachable |
| | `distinct` | a per-task uid from a pool, workspace 0700 | the runner must be root in its own container |
| `network.ingress` | `none` | nothing reaches the task | — |
| `network.egress` | `restricted` | an allowlist that reaches the model API | a proxy to run and keep true |
| | `none` | no interfaces | `claude` cannot reach its model API |

`principal: distinct` is where the credential split above becomes true, and it is cheap: a runner
root inside its own container is ordinary, and dropping to a per-task uid needs nothing exotic.
`filesystem.scope: private` is what makes the PATH allowlist a floor rather than a convention; on
a single host that is `bwrap` — in the image, which this design controls — rather than
hand-rolled `CLONE_*` flags and mount setup in Go.

**There is no one implementation, and on this machine there were none.** The mechanism lives
behind `Confiner`, and what can build a point depends entirely on where the runner is deployed:

| deployment | mechanism | reaches |
|---|---|---|
| a pod on Linux | **rootless runc** | `filesystem.scope: private`, `filesystem.readOnly`, pid/ipc/uts isolation |
| a Linux host, or this example's image | **`bwrap`** — built, `-confine bwrap` | `filesystem.scope: private`, `filesystem.readOnly`, network shared or unshared, pid/ipc/uts |
| **macOS, locally** | **`sandbox-exec`** | `filesystem.scope` only — no namespaces, no principal |
| none of the above | — | refuse, which is what `hostConfiner` does |

The bwrap one is now the built path rather than a plan, and writing it produced the sharpest
argument yet for a **catalogue** of points beside the product of chains. `--unshare-net` takes
ingress and egress *together*, so bwrap builds `(full, full)` and `(none, none)` and nothing
else — a per-leaf ceiling accepts `ingress: none, egress: full` because each leaf on its own is
within reach, and the mechanism cannot do it. `Provides` refuses that combination by name.

`restricted` was refused outright here too, on the reasoning that an allowlist is a proxy's job
rather than a stronger flag. The first half was right and the conclusion was not: it *is* a
proxy's job, and the runner runs the proxy. `--unshare-net` plus one bound socket builds it — see
[Network](#network-the-egress-proxy) — which is the clearest case yet for the catalogue, since the
buildable point `(ingress: none, egress: restricted)` is not a per-leaf fact about either leaf.

It also confirms the independence the runc reading predicted: bwrap reaches
`filesystem.scope: private` and **not** `principal: distinct`, since `--unshare-user` maps the
caller to itself. Two mechanisms, same split, which is what makes them separate leaves rather
than rungs of one ladder.

The rootless-runc row is not hypothetical: signadot's own
`sandboxes/internal/managedrunners/actionbox/runimage` builds an OCI spec and executes it that
way, and reading it corrected something this note had wrong.

**Its UID mapping is size ONE** — `{ContainerID: 0, HostID: <the outer process's uid>, Size: 1}`
— so the container's root is the *same host principal* the runner already was. That is namespace
isolation without a distinct host identity, and it is the combination the flat ladder could not
express: `filesystem.scope: private` **without** `principal: distinct`.

Which dissolves the collision predicted in the drive's finding 4. The agent's credential is keyed
on the OS user, so `principal: distinct` breaks its authentication — but a private filesystem does
not require a distinct principal, and rootless runc is the proof. The two axes are independent,
the tree says so, and a real implementation takes one and not the other. A single ladder would
have forced them to move together and made agent auth the price of filesystem isolation.

Two more things worth taking from that code, both of which are choices rather than defaults: it
**omits the network namespace deliberately**, so the container shares the pod's network
(`network.egress: full` in this vocabulary, with restriction left to a proxy — the same
conclusion this note reached from the other side); and it sets `Root.Readonly` with explicit
bind mounts for what must be writable, which is `filesystem.readOnly` enforced by construction
rather than by asking.

**And macOS has an answer, which matters because it is where this runner first escaped.**
Neither runc nor `bwrap` exists there. `sandbox-exec` does — deprecated, undocumented, and
present on every Mac — and an SBPL profile denying `file-write*` outside the workspace is enough
for `filesystem.scope`. Verified: a write to `~/Dev` under such a profile fails with *Operation
not permitted* while `/tmp` stays writable. It reaches nothing else — no namespaces, no principal,
no network — which is exactly what a class vocabulary is for: it can claim the one leaf it
provides and refuse the rest, instead of being called "a sandbox" and believed.

Factoring into classes also dissolved something the first draft had wrong.Factoring into classes also dissolved something the first draft had wrong. That draft's ladder
made `pod` — one container per task — its top rung, and a container is not a *degree* of
containment. It is a mechanism that reaches strong values on several independent axes at once.
Under classes it stops being a rung and becomes a `Confiner`, which is the better model and one
the prior art does not have, since there the sandbox *is* the unit.

**The tree is what lets the two drivers differ honestly.** The harness makes its model call from
the *runner's* process and only `run_bash` runs inside the containment, so an `llm` task can sit
at `network.egress: none` and still do real work. `claude` **is** the contained process and needs
egress to reach its API, so it cannot. That is one leaf's difference between two profiles, visible
to whoever reads the descriptor — where a single ordered ladder could only have recorded it as a
suspicion that the ladder belonged to the (runner, driver) pair.

**The obligation this puts on the implementation**: the point formed by every leaf's default must
be constructible, since it is where a spawn naming no profile lands. That is the acceptance
criterion for this example — a task spawned with no profile must edit files in its workspace and
produce a result under the `llm` driver, and `claude` must fail *loudly* on egress rather than
quietly running less contained than it said. A runner that cannot build its own default is
advertising coverage it does not implement.

**Both upper rungs are Linux, and depend on distro and kernel switches that must be verified
rather than assumed** (unprivileged user namespaces are enabled on some distributions and
restricted on others; Kubernetes has its own switch). Neither exists on macOS, where this is
being written.

Which forces the sharpest claim in this note: **an environment declares its isolation, and a
runner that cannot provide it refuses the launch.** Not "runs it with less". The argument is the
one `runners-residual.md` already makes one layer up — *a real runner knows what it is and what
it can host; verse does not, and cannot be made to* — and the alternative is a runner that runs
the work with a weaker floor than the charter named, which is undetectable from the outside,
which is the direction that costs. A refused spawn is a mechanism: it comes back as a failed
handle carrying the runner's own words, onto the flow.

### The task is a process, not a call

Before the hop, the containment above wrapped whatever the *command* driver spawned and reached
nothing the `llm` driver did — `harness.Config` has no exec hook and `fsExec.runBash` calls
`exec.CommandContext` inside the harness package, in the runner's own process. So **the more
supervisable driver was the less contained one** — per-action gates and narration, and no kernel
floor — which is why the drive's environment withholds `run_bash` rather than because a docs task
happens not to need it. You could have verification or containment, not both, and for a coding
runner that is eventually fatal, since edit-and-verify is the loop.

Wrapping each tool the harness might spawn is the obvious fix and the wrong one. The complete
version is to stop running the loop in-process:

```
verse ──HTTP──▶ coderunner (parent)        credential, network, delivery, mirrors
                     │
                     │ spawns one per task, under bwrap
                     ▼
                coderunner -task ──▶ taskd + harness
                                     workspace only
```

The parent stops being a runtime and becomes a proxy: it spawns a contained child and forwards
control and the read plane to it over the same task-control contract it already speaks to verse.
`taskd.New(build).Handler()` is one line on the child's side, and `runner.HTTP` is already the
client.

**This is the seam's own claim, cashed at process granularity.** `task/runner.go` says *"there is
no privileged in-memory path, so the contract stays complete and local ≡ remote"* — and a
contained child on the same machine is the smallest possible test of it. It passed: gating,
steering, narration, the answer contract and the result all cross the hop with nothing added to
the contract. Everything that broke on the way (below) was lifecycle or a client's error path,
never a missing verb.

What it buys is not incremental. `run_bash` is inside the sandbox, contained by construction,
because the whole loop is — so containment stops being "wrap each subprocess a task might start",
which can never be complete, and becomes "the task IS a subprocess", which is. Per-action gating
and narration survive untouched, because they travel over a **protocol** rather than depending on
shared process memory. And it downgrades `safePath`'s symlink hole from an escape to a nuisance:
a write through a checked-out symlink reaches the sandbox's view — the workspace and a read-only
system — rather than the host.

It also retires two asks. The harness does **not** need a `Confiner` seam, and it does not need
its model client exported: a child that is a process needs neither.

**Secrets go in, and that is not a regression.** The child makes the model call, so the model key
is inside the sandbox. Proxying that call through the parent to avoid it would solve one secret
by special case, while every other secret a real coding task needs — registry tokens, credentials
for the service under test — has to be plumbed anyway. That plumbing is deployment machinery and
belongs at the verse or deployment level ([expose.md](../roadmap/expose.md) already carries the
open question of whether an environment holds values or references a secret store), and
`Environment.Env` is the mechanism today.

Which sharpens a distinction this note had blurred. Two kinds of secret, and only one of them was
ever the point:

| | | |
|---|---|---|
| what the **task** needs to do its work | model key, registry tokens | into the sandbox, declared by the environment |
| what the **runner** needs to act on its behalf | the git push token | stays in the parent, never in the sandbox |

The credential split was always the second. Claiming the first as well was over-reach.

And it moves the real question somewhere more useful: once a secret is inside, what bounds it is
**what the sandbox can reach**, not whether the value is present. That is
`network.egress: restricted` — an allowlist to the model API and nothing else — which is one
mechanism covering every secret rather than one proxy per secret. So the egress proxy stops being
a nicety and becomes the piece the whole secret story rests on.

It is built ([Network](#network-the-egress-proxy)). What it does not do is bound what *leaves*
through an allowed destination: a list says where, never what, and `api.anthropic.com` will accept
anything you care to send it. So this bounds a secret against a task that wanders, and not against
one that is trying.

Structurally the parent then looks like a **broker with the polarity reversed**, spawn-out
instead of dial-in, and the broker already exists for the dial-in half.

#### Built, and what it cost

It works, and `local ≡ remote` held: `hop.go` is the parent, `taskproc.go` the child, and the
child is `taskd.New(build).Handler()` over an inherited listener. The parent's whole verse-facing
surface is now `POST /tasks` plus a reverse proxy; nothing translates, so nothing can drift.
`runner.HTTPOver(baseURL, transport)` is the injection point the client was missing.

Five things this cost, all of them findings rather than chores.

**The control plane is a descriptor, not a path.** The obvious hop is a unix socket per task, by
name — and a socket the child can dial by name is one the *agent* can dial by name, which is a
task that can answer its own gates. So the parent listens, hands the child the listening fd, and
keeps the only path outside every mount namespace. It is also the only design that survives
`network.egress: none`: a loopback port inside an unshared netns is a different loopback, and a
bind-mounted socket directory is the thing we just refused. The parent's own `/tmp` also keeps
the path under the sun-path limit, which a socket beside `-data` would not.

**`net.UnixListener.Close()` unlinks.** Taking `File()` and closing the listener removes the
socket the child is about to serve on. `SetUnlinkOnClose(false)` first. Half an hour, and what it
looks like is a child that starts and never answers.

**Subscribing before the task exists declares it dead.** The parent registered the child, started
supervising, *then* forwarded the launch — so the supervisor's `GET /events` hit a task that did
not exist yet, and `remoteHandle.SubscribeSince` reports a refused subscribe as a **failed status
frame** on the plane the caller is reading. Every task was killed at birth by the one thing
watching it. That error path is right for a mirror and wrong for a supervisor, and the fix is
ordering: forward first, supervise after.

**The snapshot lags the terminal frame.** The loop emits its last status during teardown; taskd
records status and result when `Run` *returns*. Asking once, at the instant of the frame, catches
`running` with no result — and the parent then answers that forever, which is worse than
remembering nothing. It asks until the snapshot agrees, and falls back to the frame for the
status. Worth knowing beyond this runner: **the read plane is ahead of the snapshot**, and
anything that reconciles the two needs to know which one it is reading.

**Refusals got better for free.** Environment, driver and containment checks are now HTTP errors
rather than a task that fails immediately, so `runner.HTTP` turns them into
`spawn refused by <url>: <reason>`. A launch this runner will not host used to look downstream
like work that was attempted.

**Untested**: whether `bwrap` passes fds 3 and 4 through to the program it execs. It is expected
to — bwrap's own `--args FD` proves fds reach it — but this machine has no bwrap, so it is an
assumption the first Linux deploy checks. The diagnosis if it is wrong is already in place: the
child fails with *"fd 3 is not the listener this process serves on"* and the parent reports the
child's output. The fallback is a bind-mounted socket directory, at the cost named above.

### The environment leak this fixes by construction

`fsExec.runBash` builds `exec.CommandContext(ctx, "bash", "-lc", command)` and never sets
`c.Env`, so the agent's shell inherits the *entire* server environment — including the
`PORTKEY_API_KEY` and `PORTKEY_VIRTUAL_KEY` the harness itself runs on. (`-l` also sources login
profiles, so it inherits whatever those set as well.) `expose.md` flags this and prescribes the
fix: *"an environment record fixes that by construction rather than by policy: the environment
defines the variables, so the default becomes 'what this sandbox declares' and inheritance
becomes an explicit passthrough list. A floor that has to be enforced by a gate is not a floor."*

The `env:` allowlist above is that list, and a coding runner is where the leak actually bites,
because it is the runner that holds a git credential and hands out a shell.

## Network: the egress proxy

Default-deny is right for the task, and the task needs a model API from inside the same namespace
— since the hop it is the process being contained, so the asymmetry that used to let the `llm`
driver call out from the runner's process is gone. The containment cannot be "no network" without
disabling the work.

`egress.go` is the answer this note guessed at, built: an allowlisting proxy per task, on the
runner's side, and the sandbox's only route to it.

**Declaring the list IS declaring the rung.** An environment writes `egress: [api.anthropic.com,
proxy.golang.org]` and `pointFor` derives `network.egress: restricted` from it. Two reasons: the
list and the claim cannot then disagree, and "restricted" with no list is a claim with no content.
`*` is refused by name — that is `egress: full`, which says so where a descriptor can read it.

**The mechanism is a namespace with no route plus one socket bound back in.** `--unshare-net`, so
there is nothing to reach; `--bind <proxy socket> /run/verse-egress`, so there is exactly one
thing. That is why `restricted` arrives with `ingress: none` attached — there is still no way in —
and it is the answer to the combination rule that previously made `restricted` unbuildable. bwrap's
network flag is all-or-nothing; the rung is not built by a better flag but by a different
mechanism the runner supplies.

**Two hops, both forced by the same fact.** Nothing speaks "HTTP proxy over a unix socket" — not
Go's transport, not curl, not `go`, not npm — so the task process listens on its own loopback and
pipes each connection down the socket, which turns it into an ordinary `http://127.0.0.1:<port>`
proxy everything already knows. Loopback is safe to expose there for the same reason the socket is:
inside an unshared netns it is a network of one process tree. Without the unshare it would be the
host's loopback and an open proxy — one more reason a confiner that cannot unshare **refuses**
rather than approximating.

Which is the cost, and it is real: **`restricted` cannot run on a machine without bwrap.** The
proxy works under `hostConfiner` — it is a socket and a port — and it would be advice, because
nothing stops the task opening its own connection. A floor that can be stepped around is not one,
so an environment with an allowlist is refused on macOS. The proxy, the allowlist and the forwarder
are all tested for real; the combination that makes them a floor is not, and cannot be here.

**A finding about the medium.** A refused `CONNECT` is answered rather than dropped, so an agent can
report "not on the allowlist" instead of a network problem. But `http.Error(w, why, 403)` loses the
message entirely: Go's transport reads a failed CONNECT and returns `errors.New(reasonPhrase)`,
discarding the status code *and* the body. The agent saw the single word `Forbidden`. So the
explanation goes in the reason phrase, sanitized to one ASCII line — the destination comes from the
task, and a host with a CRLF in it would otherwise write headers of its own choosing.

Still open, and unchanged: an image with dependencies pre-warmed goes stale, and a proxy that can
be reached is a proxy a prompt-injected agent can try to reach. What the allowlist bounds is
*where*, never *what* — a destination list says nothing about what leaves through an allowed
destination, and `api.anthropic.com` is a place you can send anything you like.

The proxy is still what an environment declares, and `network: proxied` is still the rung with
the most left to design.

## The image

Separate from the runtime `Dockerfile`, which is deliberately thin: `debian:bookworm-slim` plus
ca-certificates, git, curl and three binaries, and its header explains that one image with
several roles is what keeps the deploy to one ref to pin. A coding image is the opposite kind of
thing — a toolchain, sized by what the repos it serves need to build — and pinning it to the
engine's release cadence would be wrong in both directions.

So: its own Dockerfile beside the example, carrying the coderunner binary, the agent binary, and
the toolchain, with the `commands:` ceiling of an environment being a claim about *that* image.
Which makes the environment name and the image a pair that must be kept true together, and
nothing in this design checks that — an environment naming `go` in an image without it produces a
task whose commands are not found, with no better diagnosis than the agent's confusion.

## What this does not do

**No scheduling, no queue, no capacity.** Unchanged from [runners.md](../runners.md). A workspace
per task means concurrent tasks means concurrent checkouts — and with a clone per repository
that is disk, not just inodes — and the bound on how many is a flag,
not a scheduler. Over it, the spawn is refused.

**No merge, no PR, no review.** The runner pushes a branch. What happens to that branch is a
charter's business, and the [github source](../reference/sourced.md) is what would see it come
back. A runner that opened pull requests would be a runner with opinions about a workflow.

That is the division this design is betting on, and it is worth saying which way the bet runs:
**the runner's job ends at the edge, and everything interesting is downstream of it.** Reviewing
the work, finding a bug in it, spawning the fix — those are charters conditioning on a branch,
not features of a runner. Which is exactly why the push has to be disciplined: it is the one
event the whole downstream depends on, so it happens once, at a defined moment, to a target the
environment named, caused by a party verse knows. An agent pushing when it felt like it would
give the same flows a cause verse never saw, at a moment nobody chose, possibly several times
per task.

**No conflict handling.** Two tasks on the same base produce two branches, and if both push, both
exist. Reconciling them is the flow's problem.

**No incremental workspaces.** Each task starts from `base`, not from another task's output.
Chaining is a charter writing a second spawn whose environment names a different base — not
something the runner arranges.

## Open questions

- **Where the per-spawn command opt-in is written.** It is request-specific, so not the
  environment; `grant:` can carry it and is the wrong mechanism for it (above). A field beside
  `environment:` on the spawn is the obvious third answer and adds charter grammar this note has
  otherwise avoided.
- **Whether `$task` in a push target is a template the runner expands.** `launch.Spec` already
  has a `$var` expansion pass and it runs on the *charter* side against the matched event. A
  second expansion, on the runner side, against a different vocabulary, is two template languages
  that look alike.
- **What a task's result should be.** The agent's final message, or the branch it pushed, or
  both. `$result.branch` is what a charter wants to deref, which argues for an answer contract
  the runner *imposes* — and a runner imposing a contract on the work is a new kind of thing.
- ~~Whether the LLM harness can reach the upper `filesystem` rungs at all.~~ **Answered by
  making the task a process.** It runs `bash -lc` in-process today, so no amount of per-tool
  wrapping can be complete — but a bwrapped child running the loop contains `run_bash` by
  construction. The question was the wrong shape; what is left of it is building the
  parent↔child hop.
- ~~`safePath` does not resolve symlinks.~~ **Fixed** in `task/harness`. It rejected `../..` and
  allowed a write *through* a symlink inside the root — `write_file` to `link/pwned` wrote
  outside it, which mattered because a workspace is a git checkout and git checks symlinks out
  as symlinks, so a repository carrying one was an escape needing no shell. It now resolves the
  deepest existing ancestor (a write names a file that does not exist yet) and the root as well
  (`/var` is `/private/var`, so resolving one side only would refuse every legitimate write).
  The race is not closed and says so: a symlink planted between the check and the open would
  still be followed, which needs an `openat2`/`O_NOFOLLOW` walk rather than check-then-open, and
  is unreachable through the tools this package offers because none of them creates a symlink.
- **Credential form.** A token in the runner's environment, a git credential helper, or an SSH
  agent socket — differing in what a compromised runner process leaks, and all of them equal from
  the *task's* side, which cannot reach any of them.
- ~~Whether the mirror is shared across environments.~~ **Built**: mirrors are keyed by the
  remote, so two environments on one repository share objects and a lock, and two on different
  repositories share neither.
- **The harness seam that moves the push inside the loop.** The sharpest one, because it is
  three defects held open by one missing export: `harness.Build` returns a finished `*task.Task`
  and hands back neither driver nor executor, so nothing can compose a final action onto the
  loop. `harness.BuildParts` (or an equivalent) is the change, and it belongs to the harness
  package, made against this want rather than guessed at. It is also the case that decides
  whether `Task.SetRepo` and `GateMatch.OnPush` ever mean anything: both exist, and neither has a
  production consumer until a push is an action.
- ~~A `git:commit` entity carries no trailers.~~ **Built**: `source/git.go` reads `%B` over
  NUL-separated records and stores the parsed trailer block, so `Verse-Task` is conditionable.
  What is left is a naming question rather than a formatting one — whether anything should
  reserve the `Verse-*` prefix, given that a repository is free to write those trailers itself.
- **Whether workspaces should be pooled and reused**, and what that costs the floor — the
  reuse-versus-containment tension above. The measured case for it is the build cache rather
  than the checkout, which is also what makes it a cross-task channel rather than a cache.
- **When the clone should become alternates.** The trigger is repository size, and the switch is
  one function (write `objects/info/alternates`, set the ref off the mirror, skip the fetch,
  construct the storage with `AlternatesFS`). Worth doing against a real monorepo rather than
  against an argument — and worth a note that the mirror can then never be garbage-collected
  under a running task without breaking it.
- **Whether a repository can be added to a running workspace.** A task that discovers halfway
  through that it needs a third repository has no way to ask for one, and the environment is
  fixed at spawn. That is probably right — it is the floor argument again, a set declared rather
  than accumulated — but it is untested against real work.
- **What a `paths:` predicate means across repositories.** Changed files are workspace-relative
  (`verse/task/loop.go`), so a charter's existing path globs need a repository segment they did
  not need before. Nothing warns about a predicate written for the single-repo shape.

## What it would touch

New: `docs/reference/examples/coderunner` (the program, its config, its Dockerfile) and an entry
in [reference/examples/README.md](../reference/examples/README.md) and
[docs/README.md](../README.md)'s table. Existing: the `environment` field through `task.Launch`,
`runner.HTTP.Spawn`, `taskd`'s decode and `versed.launchOf` — which removes one of the two names
in that function's "still unread" comment, and that comment must be edited to say so rather than
left describing a state that no longer holds. [expose.md](../roadmap/expose.md) then has a
referent for the axis it says is waiting for one, and its section on the `run_bash` environment
leak has a place where the fix is built; both want editing on landing rather than leaving as
prophecy.

Nothing here changes the charter grammar, the entity model, or the meaning of an existing field,
so it is `change`. The `environment:` plumbing is a field with a safe absence at every hop.
