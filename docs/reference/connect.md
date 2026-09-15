# Federating a working tree, and scopes

`verse connect` federates your working tree into the verse under a private, per-session
*scope*, and wires a validator to every file — the local-dev loop. It is its own process, so
give it a terminal and leave it running.

How a tree is split into an index and served bytes is
[content federation](../content-federation.md).

## What one command does

Three things.

It writes each file's rich metadata — full fstat plus a content hash — to the store as
`local.dir-status-<scope>.<path>` entities. That is an index, not a warehouse: the stat and
the hash, not the bytes.

It serves file contents on demand through docd, from a content controller at
`local.dir-content-<scope>.*`, so a validator reads them through the verse with no shared
filesystem required.

And it installs a scoped trigger that spawns a validator per file.

It all lives in a nonce scope, isolated from baseline and from other devs, and on exit
`connect` drops it.

## Driving it

Configure LLM creds first ([up.md](up.md#the-driver)) so the validators run as real agents.
In one terminal:

```sh
verse up
```

In another, connect your tree and give the validator a prompt — `$path` binds to each file's
relative path. `connect` prints the scope it chose (e.g. `scope "a1b2c3"`):

```sh
verse connect . --driver llm \
  --prompt 'Read the file at path "$path" and report any problems in one sentence.'
```

In a third (using the scope `connect` printed):

```sh
verse task                                           # a validator per file
verse task get <id>                                  # the agent's read-and-validated report
verse --scope <scope> data local                   # your files, as dir-status entities
verse data local dir-content-<scope> README.md # a file's bytes, served on demand
```

Touch or add a file and its validator fires, reading the file through the verse. Ctrl-c
`connect`, or run `verse disconnect --scope <scope>`, to tear the scope down: trigger,
overlay and content mount all go.

For a credential-free smoke test, drop the creds and `--driver llm` — a validator still fires
per file, on the scripted driver; it simply does not read.

Because contents flow through docd rather than a server-side `--root`, the runtime need not
share a filesystem with your tree, so the validator can run elsewhere. *File reads are still
truncated today, so an agent validates a large file from a partial view.*

<a id="scopes"></a>
## Scopes

Every client command takes `--scope <name>`, with baseline as the default. A scope is a
copy-on-write overlay: reads see the scope layered on the shared baseline, and writes land in
the scope without touching baseline, so your local changes and flows stay isolated.

```sh
verse connect . --scope me
verse watch --scope me
verse data --scope demo x 1 '{k: v}' --put me # in the scope only
verse data demo x 1                          # baseline: not there
verse disconnect --scope me                        # drop the whole overlay
```

`disconnect --scope me` tears the overlay down in one shot — every scope write, not just
`local.*` files — via logd's `DeleteScope`. What the scope started goes first: its sources stop,
its engine stops, and every live task it owns ([below](#what-a-scope-owns)) is cancelled, those
its rules started without their `onCancelled`, which would act inside the scope being dropped.

Scopes are the store's, not the surface's: a copy-on-write overlay is something logd/docd
does, and `disconnect --scope` asks it to drop one. A store without real scopes would answer
501 and the client would fall back to removing `local.*` entities one at a time.

The scope's engine observes via per-trigger subtree watches, each watching exactly the
`system.kind` it matches rather than a root.

## The charter a scope runs

A scope runs **the rules installed into it**, and only those. `verse c -a --scope me`
writes the rule into the scope's own layer, where it is durable and addressable (`verse data
get verse.trigger.rule.x --scope me`); the record carries `scope: me`, which is what says whose
it is.

Baseline's rules are **not** run in a scope, even though a scoped read sees them: the store
layers baseline underneath, so `verse data verse trigger rule x --scope me` answers for a
rule the scope never installed, and the `scope` field on the record is what tells the two
apart. Whether a federation *should* inherit baseline's program is open, and the reason it is
open is cost rather than taste — a scoped subtree watch is seeded
with the entities the scope inherits, so installing baseline's charter into a scope would
replay all of it over everything the scope can see, privately, per connected dev, and then
fire again in every scope on every baseline change.

The listing is the same set: `verse charter --scope me` answers from the scope's engine rather
than from the layered store, so it shows what runs here and nothing else — the rules you
installed, without baseline's underneath them.

To run a baseline rule's behaviour in your scope, install it there. To **replace** one, install
yours under baseline's name: a scoped install lands in your layer, where it is the only rule of
that name the scope's engine holds, and baseline is untouched. Nothing has to be removed first,
and nothing can be: `verse c -r rules <a baseline rule> --scope me` is a write to baseline's
charter made from a scope, and is refused as one (see [what a scope owns](#what-a-scope-owns)).
A rule that never ran here needs no hiding.

A scope's charter comes back after a verse restart when the scope's engine is next built —
that is, when you reconnect. Until something asks for the scope, its engine does not exist and
its rules are not running, because a restart cannot enumerate the scopes that have a charter:
a scope's layer is invisible from baseline and nothing registers one.

*Left to do: a scope is not yet a first-class, listable, durable thing. That is the missing piece above: a registry
would let a restart bring every scope's charter back without waiting for a reconnect.*

<a id="what-a-scope-owns"></a>
## What a scope owns

A scope has two halves, and they layer differently.

**The world is copied.** Entities under `demo`, `git`, `notes` — whatever the charter is
about — are logd's copy-on-write: a scoped write lands in the overlay, a scoped read sees the
overlay on baseline, and baseline never sees the overlay.

**The program is referenced.** Rules, tasks, gates and flows are records in the store, but each
is also a live thing: a rule is a watch, a task is a handle, a gate is an awaiter, a flow is the
set of handles about a subject. A live thing runs in exactly one engine, and every scope's engine
shares one task runtime and one gate registry with baseline. The scope's engine holds what the
scope created — the rules installed into it, the tasks those rules and `task run --scope` spawned,
the gates those raised. Everything else it can see is baseline's, read through the overlay: the
record is there, the live thing is not.

So a control-plane write in a scope — `flow suspend|resume|say|cancel`, `task cancel|say|vector`,
`gate answer|escalate`, `charter -r` — reaches only what the scope created. A
suspension made with `--scope me` covers the rules the scope installed, the tasks and questions
they started and the tasks run in the scope, and nothing baseline's; one made without covers
baseline's and every package's, and nothing any scope's. Addressed at an
inherited object it is **refused**, and the refusal names the scope that holds it, baseline being
the unnamed one:

```
task review@demo.pr.1 is baseline's; scope me did not create it — act on it without --scope
```

Refused rather than passed through or copied, because the two halves would part ways: the act
can only land on the live thing, which is baseline's, while the record it leaves would land in
the overlay. A gate answered that way would be delivered in baseline and recorded as answered in
the scope alone, with baseline's record still pending. The rule reads the same from baseline: a
task a scope spawned is that scope's, and baseline is told which scope to say rather than allowed
to cancel it unaware.

Creating is what "the scope's own" means, so creation is never refused: `charter -a --scope me`,
`task run --scope me`, and everything a scoped rule fires. To replace a baseline rule in a scope,
install yours under its name (above); removing baseline's from the scope is a write to baseline's
program, and is refused as one.

**Grants are copied, like the world.** A grant is live too — the check reads a mirror rather
than the store — and a scope is governed by baseline's grants: opening one is not a way around
them. But a grant has no live half that runs anywhere; the mirror is a cache of records, and it
layers as records do. A grant made with `--scope me` lands in the overlay and governs the scope's
writes on top of baseline's, and baseline's checks never see it. [Permissions](../perms.md#scopes)
has the detail.
