# One path into the charter: a plan for `verse c`

Status: **proposal, nothing here is built.** The charter command as it stands is
[cmd/verse/charter.go](../../cmd/verse/charter.go); the tree it prints is the server's
([versed/charter.go](../../versed/charter.go)).

The charter became one thing when `verse charter` and `verse stratum` merged: rules and levels
are installed from one file, confined by one address, and they come and go together. The
command did not finish the journey. It still has four verbs, two of which take a NAME and two
an ADDRESS, and the two coordinates are told apart by which verb you typed:

    verse charter [<address>]              a tree
    verse charter show <name> [--at <a>]   a rule or a level, by name, searched for
    verse charter remove <name> [--at <a>] …and how it goes
    verse charter uninstall <address> [--recurse]

So `show` searches every charter for a name while `uninstall` takes an address and refuses to
guess; `remove` takes a rule out and `uninstall` takes a package out, and the difference between
them is not in the word. A reader who has learned one of these has learned nothing about the
next.

**The proposal in one line: there is one path grammar, it is the tree's own, and every verb
takes it.**

## The surface

    verse c                                  the whole charter, every address
    verse c <path>                           …what stands at <path>
    verse c add -f <file|dir> [--at <a>]     install; a directory is walked
    verse c rm <path> [-r]                   remove what <path> names

and `<path>` walks ONE tree — the address, then into the node document that address holds:

    verse c verse-dev                        a place: what is under it
    verse c verse-dev.drift                  the charter installed there
    verse c verse-dev.drift rules            its rules
    verse c verse-dev.drift rules review     one rule
    verse c verse-dev.drift strata base      one level
    verse c verse-dev.drift writes           what it may write
    verse c verse-dev.drift reads            what it declares it reads
    verse c rules review                     baseline's `review`

That is goals 2 and 3 at once, and neither is a feature: `rules`/`strata` reach the same way at
every address because they are keys in the document every node already prints, and `show` stops
existing because `verse c <path>` was always going to be able to answer it.

It is the grammar `verse state` has. `verse state demo.pr.1.stage` is an address followed by a
reach into the payload at it, and nobody had to be told that `.stage` was a different kind of
step. A charter node is a document; a path indexes it.

### Where the address ends

The two halves are resolved SERVER-SIDE, because the server is the end that knows the tree —
which is the same reason the tree is built there. The client sends the whole positional list;
the server consumes segments while they name children, and the remainder must name a key of the
node it stopped at.

    verse c verse-dev drift rules review  →  path=verse-dev.drift.rules.review

Greedy: the longest address that exists wins. A package installed at an address whose last
segment is literally `rules` shadows the key, and `--` ends the address for that case, as it
already ends the verbs today:

    verse c verse-dev -- rules             the `rules` KEY of verse-dev
    verse c verse-dev.rules                the package at that address

A path that resolves nowhere is where the old `show`'s search earns its keep — not as grammar,
but as the error. `verse c rules review` when baseline holds no `review` should answer

    no rule or level `review` in the baseline charter — `verse-dev.drift` has one:
    verse c verse-dev.drift rules review

which is what the search was FOR, said at the moment it helps, without a second way to address
things.

### `rules`, not `triggers`

The tree prints `triggers:` and `strata:` today. The path above says `rules`, and one of the two
has to move, because a path that does not index the document it is a path into is a second
vocabulary wearing the first's clothes. The prose has said "a rule and a level" since the
charter merged; `triggers:` is the older word. **Rename the key to `rules:`** — a contract
change to `/charter`, in the same family as `imports:` → `reads:` and for the same reason.

## Goal 1: recursion, both ways

`uninstall --recurse` exists. What is missing is the other half and the reason to trust either.

**`rm -r`** is today's `--recurse` under the verb that now takes paths. Removing a place with
charters under it needs it; removing one package does not. Unchanged in meaning, and the
refusal it prints already names what is under the address.

**`add -f <dir>`** walks a directory for charter files and installs what it finds. Each file
says its own `at:` (or takes `--at`), so a tree of files is a tree of packages, and a program
that spans four addresses is four files in a directory rather than one file with four `---`
declarations — which is what a charter of any size wants to be.

**And it must be one step.** Today a multi-package FILE is already several requests: the CLI
walks the groups and POSTs each. A refusal in the third leaves the first two installed, which is
exactly the half-applied state the per-package guarantee was written to prevent ("a charter file
is applied as one step, so a refusal anywhere puts back what stood before it"). A directory
makes that failure ordinary rather than rare, so recursion is the change that has to fix it:
**`POST /charter` accepts several packages and applies them all or none.**

That is the one genuinely new mechanism here, and it is the one to be honest about: the existing
atomicity is per package, inside one engine ([versed/charteratomic_test.go](../../versed/charteratomic_test.go)),
and a transaction across N engines is not free. If it turns out to want more machinery than the
goal deserves, the fallback is to apply in dependency order and report precisely what stood when
it stopped — worse, but not silently worse.

## Order of work

Each step leaves a working tree, a runnable verse and a green smoke.

### 1. The server resolves a path (no CLI change)

`GET /charter?path=a.b.rules.review` alongside today's `at=`/`depth=`. Walk the tree; answer the
node, the list, or the item. 404 says which half failed and, when a name exists elsewhere, where
it is.

Tests: a path to a node, to `rules`, to one rule, to `writes`; the greedy split against a
package named `rules` and the `--` escape; the not-found message naming the other charter.

### 2. `triggers:` → `rules:` in the tree

Contract, with a notice. Touches the tree builder, the CLI's own tree walking
(`charterHolding`), the docs, and the smoke. Do it before the CLI moves, so the CLI is written
once against the final shape.

### 3. `verse c <path>` subsumes `show`

The positional list goes to `path=`. `show` is deleted rather than aliased — no compatibility
path, per [contributing.md](contributing.md)'s standing rule while the users are the people
writing it. `--at` survives on `add` alone.

Tests: the CLI end of each path above; `--depth` still composes; a scope still answers.

### 4. `verse c rm <path>` subsumes `remove` and `uninstall`

One verb over the same grammar: a path ending in a rule or level removes it, a path naming an
address takes the package down, `-r` for what is under it. The removal keeps what the package
WROTE, as it does now, and keeps saying so.

Tests: each shape; the refusal when children exist and no `-r`; a scope's tombstone wording;
and the case the last defect turned up — a record the engine never installed still goes.

### 5. `add -f <dir>`, applied as one step

The walk, then the atomic apply. Order within it is the file order for reporting and the
dependency order for installing (a package that another reads goes first).

Tests: a directory of two packages, one of which is refused → nothing installed; a package
reading another in the same directory installs regardless of file order.

## What this does not do

- **It does not touch the charter FILE grammar.** `at:`, `reads:`, rules and levels are
  unchanged; this is the command, not the language.
- **It does not add a search command.** The old search survives as an error message, which is
  where it was useful.
- **It does not rename `add`.** `install` reads better against `uninstall`, but `uninstall` is
  the word going away, and `verse c add` is in every charter doc in the tree.

## Open

- Does `verse c <path>` want `--paths` (one address per line, as `verse state` has) for
  scripting? Probably, and it is cheap; left out above to keep the first cut small.
- `rm` of a rule that a stratum still holds is refused today by the engine. The message should
  name the path to the stratum now that there is one.
