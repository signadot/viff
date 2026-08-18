# Federating a working tree, and scopes

`verse connect` federates your working tree into the verse under a private, per-session
*scope*, and wires a validator to every file — the local-dev loop. It is its own process, so
give it a terminal and leave it running.

The mechanics, through docd's two faces, are in [connect](../working/connect.md); how a tree
is split into an index and served bytes is [content federation](../content-federation.md).

## What one command does

Three things.

It writes each file's rich metadata — full fstat plus a content hash — to the store as
`local:dir-status-<scope>:<path>` entities. That is an index, not a warehouse: the stat and
the hash, not the bytes.

It serves file contents on demand through docd, from a content controller at
`local:dir-content-<scope>:*`, so a validator reads them through the verse with no shared
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
verse --scope <scope> state local                   # your files, as dir-status entities
verse state get local dir-content-<scope> README.md # a file's bytes, served on demand
```

Touch or add a file and its validator fires, reading the file through the verse. Ctrl-c
`connect`, or run `verse disconnect --scope <scope>`, to tear the scope down: trigger,
overlay and content mount all go.

For a credential-free smoke test, drop the creds and `--driver llm` — a validator still fires
per file, on the scripted driver; it just doesn't read.

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
verse state put demo x 1 '{k: v}' --scope me      # in the scope only
verse state get demo x 1                          # baseline: not there
verse disconnect --scope me                        # drop the whole overlay
```

`disconnect --scope me` tears the overlay down in one shot — every scope write, not just
`local:*` files — via logd's `DeleteScope`.

Scopes are the store's, not the surface's: a copy-on-write overlay is something logd/docd
does, and `disconnect --scope` asks it to drop one. A store without real scopes would answer
501 and the client would fall back to removing `local:*` entities one at a time.

The scope's engine observes via per-trigger subtree watches, each watching exactly the
`system.kind` it matches rather than a root.

## The charter a scope runs

A scope runs **the rules installed into it**, and only those. `verse c -a --scope me`
writes the rule into the scope's own layer, where it is durable and addressable (`verse state
get verse:trigger:rule:x --scope me`); the record carries `scope: me`, which is what says whose
it is.

Baseline's rules are **not** run in a scope, even though a scoped read sees them: the store
layers baseline underneath, so `verse state get verse:trigger:rule:x --scope me` answers for a
rule the scope never installed, and the `scope` field on the record is what tells the two
apart. Whether a federation *should* inherit baseline's program is open, and the reason it is
open is cost rather than taste — a scoped subtree watch is seeded
with the entities the scope inherits, so installing baseline's charter into a scope would
replay all of it over everything the scope can see, privately, per connected dev, and then
fire again in every scope on every baseline change.

The LISTING is the same set: `verse charter --scope me` answers from the scope's engine rather
than from the layered store, so it shows what runs here and nothing else — the rules you
installed, without baseline's underneath them.

To run a baseline rule's behaviour in your scope, install it there. To **replace** one, install
yours under baseline's name: a scoped install lands in your layer, where it is the only rule of
that name the scope's engine holds, and baseline is untouched. Nothing has to be removed first —
`verse c -r rules <a baseline rule> --scope me` answers "no such rule", correctly, since
the scope holds none of them, and a rule that never ran here needs no hiding.

A scope's charter comes back after a verse restart when the scope's engine is next built —
that is, when you reconnect. Until something asks for the scope, its engine does not exist and
its rules are not running, because a restart cannot enumerate the scopes that have a charter:
a scope's layer is invisible from baseline and nothing registers one.

*Left to do: a scope is not yet a first-class, listable, durable thing — see
[scope lifecycle](../roadmap/scope-lifecycle.md). That is the missing piece above: a registry
would let a restart bring every scope's charter back without waiting for a reconnect.*
