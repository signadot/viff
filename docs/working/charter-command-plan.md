# One path into the charter: a plan for `verse c`

Status: **all of it is built** (branch `charter-path`). What is written under each step is the reasoning it was built from, with what the building found noted under it. The charter command as it stands is
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
    verse c -a [<address>] -f <file|dir>     install; a directory is walked
    verse c -r <path>                        remove what <path> names
    verse c -R <address>                     …and every charter under it

**WHAT TO DO IS A FLAG, and a positional is a path in every case.** The verbs were the last
place two coordinate systems met: `add`, `rm` and `uninstall` are ordinary words and a package's
address is a system name, so `verse c rm` had to be told apart from the package installed at
`rm` — by a `--` the operator had to know existed. Nothing has to be told apart now.

It also retires two meanings of `--at`, which is how the collision was going to reappear:
installing took `--at <address>`, reading took `--at` to say where an address ended. Both are
the positional path now.

`-R` is not `rm -r`'s recursion, and the letters are chosen knowing that: `-r` is the removal,
`-R` is the removal that reaches under an address. A hand that types `-r` out of habit gets the
narrower thing and, when charters are under the address, a refusal naming them — which is the
direction that mistake should fail in.

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

~~**`add -f <dir>`** walks a directory for charter files~~ — **dropped.** A charter FILE is
already multi-package: `---` separates documents and an `at:` declaration opens each package, so
a file encodes its own tree of addresses. A directory would add organisation and nothing else,
and `for f in *.tony; do echo ---; cat $f; done | verse c -a -f -` is that organisation without
a walk, a suffix filter, an ordering rule and a subdirectory question.

What is left when the directory goes is the part that was always the point, and it is a defect
rather than a feature: **a multi-package file is not applied as one step today.**

**And the message says otherwise, which is the sharp end.** The CLI walks the groups and POSTs
each, so a two-package file whose second package is refused prints:

    package one: installed 1 trigger(s)
    charter -a: 400 … trigger "bad-rule": subject elsewhere:y is outside two:*
    nothing was installed: a charter file is applied as one step, so a refusal anywhere puts
    back what stood before it

…with `one` installed. The guarantee is real per PACKAGE and the sentence claims it for the
FILE. So: **`POST /charter` accepts a whole file — several packages — and applies them all or
none**, and the grouping moves to the server with it, which is where the rest of the
classification already lives ("a client posting a charter file does not have to know which of
three things each document is").

### It is not a new mechanism, and the fallback is dropped

An earlier draft called this the one genuinely new thing here and kept a fallback in reserve —
apply in dependency order, report precisely where it stopped — on the grounds that a transaction
across N engines is not free. That reasoning was wrong about what exists, and `charterTx` says
so in its own words:

> It is not a transaction over the store — the writes have committed — but an UNDO of them in
> reverse, which is as much as an engine holding live watches can offer.

There is no transaction to generalize. What exists is a compensating undo list, built one entry
at a time as each declaration, level and rule goes in, replayed backwards on a refusal, plus one
`fresh` bool that drops an engine this request brought into being. Every one of those entries is
already a closure that knows what it is putting back. **Widening it is making `at`/`eng` a
property of each step instead of of the transaction, and `fresh` a set instead of a bool.** That
is smaller than the fallback, which would need a dependency sort the install does not otherwise
have.

So: **atomic across packages, no fallback.** And the dependency sort is not needed for the
install either — a rule's reads are checked against its own package's DECLARATION, and declaring
a read of an address no package is installed at adds no edge, so file order installs correctly
whatever the reading between them.

What the wider scope does not change is the kind of guarantee, and this is the honest limit to
write down rather than discover: the undo compensates, it does not isolate. A rule installed
early in a request begins observing immediately and can fire before a later refusal arrives, and
what it wrote is the world's and stays — which is already true of a single file today, and is
the same sentence `uninstall` prints about what a package wrote. "All or none" is a promise
about the CHARTER, never about the writes its rules made while it stood.

## One charter operation at a time

Nothing serializes charter mutation today. `scopeMu` guards the map of engines, not what is
done to one: two `charter add`s at the same address both take the engine and mutate it, and
their undo lists interleave — so a refusal in one puts back state the other installed, and the
"nothing was installed" line is then false in both directions. `uninstall` is in the same
position, and it now sweeps the store as well as the engine.

That is a hole in what is there, not one recursion opens. Recursion widens it: an operation that
was one package and one engine becomes a directory and several, so the window goes from
milliseconds to as long as the biggest charter takes.

**A single server-side lock over charter mutation — install and uninstall, every address, every
scope.** Not per address: a recursive install spans addresses and so does `rm -r`, so a
per-address lock would need a canonical acquisition order to stay deadlock-free, and it would
buy concurrency between operations that are operator actions taken seconds apart.

**Acquired against a context, not blocked on.** A `sync.Mutex` cannot be waited for with a
deadline, so the lock is a `chan struct{}` of capacity one and the acquisition is a `select`
over it and `ctx.Done()`. An HTTP request carries no deadline of its own, so the handler makes
one:

    ctx, cancel := context.WithTimeout(r.Context(), charterLockWait)

and a request that does not get in answers **503 with `Retry-After`**, saying that another
charter operation is in progress — a temporary condition about the server, which is what 503
means, rather than a conflict in what was asked. A caller that waits forever behind an install
is how a CLI hangs with nothing to report, and the wait is exactly the kind of thing that turns
into "verse charter times out" with no way to tell waiting from wedged.

`charterLockWait` is 30s, the same as the store's write budget
([entity/logdstore.go](../../entity/logdstore.go)) and for the same reason: a charter install is
a sequence of writes, each of which may already be waiting behind other writes, so the wait has
to exceed a whole install rather than a single write.

Two rules that come with it, both worth stating because getting either wrong is silent:

- **The charter lock is the OUTER lock.** `packageEngine` takes `scopeMu`; the charter lock is
  taken before it and never from inside it. Nothing may acquire it while holding an engine lock.
- **Reads do not take it.** `GET /charter` sees whatever is installed at the moment it asks,
  half-applied included — which is what it would see of any concurrent write, and no worse.
  Putting reads behind the mutation lock is how a listing wedges behind an install, which is the
  same shape as the readiness probe that read `/status` and made health a function of load.

The restore path takes it too. `restorePackages` runs after the listener is up (versed serves
while it restores, deliberately — that is what `/healthz` distinguishes), so an operator's
install can land mid-restore today. It already releases `scopeMu` before loading charters, for
the reason above; taking the charter lock around the load makes an install during a restore wait
or be told to come back, rather than race the charter it is being installed alongside.

### Is that a contradiction? No, but the guarantee is narrower than "one step" sounds

"Applies as one step" and "a reader can see it half-applied" only look contradictory if the
promise is isolation. It is not, and it cannot be here, for a reason that has nothing to do with
readers: **a rule begins observing when it installs.** By the time a later definition in the
same request is refused, the earlier ones have been live for milliseconds — matching, firing,
writing. That is already true of one file today, and it is why the undo compensates rather than
isolates.

Which settles what locking reads would buy: almost nothing. A reader held behind the lock would
get a tidy before-and-after view of a verse that had *already run* half a program. The exposure
is not on the read path; it is in the engine, which is inside the mutation rather than outside
it. So locking reads buys a neater display and costs a listing that wedges behind an install —
the worse half of both.

The only thing that would buy real isolation is **build-then-swap**: install into an engine that
is not observing, validate the whole request, then swap it in. Rejected, and worth saying why so
nobody re-derives it as an obvious improvement. An engine's identity is its watches and its
running state — the per-rule watches whose establishment is what made package restore take
seconds, plus the seen watermarks, the in-flight hooks and the motions being awaited. A swap
would pay that cost on every install and drop that state, to close a window measured in
milliseconds, on an operator action. The trade is not close.

So the honest statement, and the one the tests should pin: **all-or-none is a promise about what
is INSTALLED when the request answers.** Not about what was visible while it ran, and not about
what the rules did while they stood — which is the same sentence `uninstall` already prints
about what a package wrote.

What the plan does close, and it is the part that matters:

- **Two mutations can no longer interleave.** That is the corruption risk: undo lists crossing,
  and a refusal putting back state another request installed while both claim nothing happened.
- **The read-then-act gap goes away.** `whichCharter` GETs the whole tree to resolve a name, then
  sends a DELETE for it — two requests, with the charter free to move between them. Resolving
  the path server-side inside the mutating request (step 1, used by step 4) makes resolution and
  removal see one charter. That is a gain in precision rather than a corruption fix, but it is
  the last read-then-write chain outside the lock: after this, every read that feeds a write
  happens inside it.

## Order of work

Each step leaves a working tree, a runnable verse and a green smoke.

### 0. The lock (independent of everything below)

The section above, against today's code: install and uninstall both take it, the acquisition is
context-bounded, 503 on the timeout. It lands on its own — it fixes a hole that exists now, and
every step below inherits it rather than needing it.

Tests: two concurrent installs at one address, both of which must report truthfully; a held lock
answering 503 rather than hanging; an install arriving during a restore; and a canary that the
charter lock is never taken while `scopeMu` is held. Plus the limit, pinned rather than assumed:
a rule that fired between installing and a later refusal keeps what it wrote.

**Built** ([versed/charterlock.go](../../versed/charterlock.go)). Three things the building
found:

- The ordering rule needed no enforcing anywhere: `restorePackages` already released `scopeMu`
  before loading charters, for its own stated reason. The canary stays, since what is habit
  today is a bug tomorrow.
- The refusal could not be observed in a test without sitting out the 30s wait, so the wait is a
  `Server` field defaulting to the constant. That is the only thing in the design that exists
  for a test, and it is one field rather than a global a parallel test could race.
- `release` has to be idempotent. Every handler defers it, and the lock is taken before the
  paths that return early, so a second call is reachable by ordinary editing rather than by
  mistake.

Not asserted, deliberately: that two RACED installs interleave. A test that wins a race by
timing proves nothing on a loaded machine, so the assertion is on the lock — the second caller
does not get in while the first holds it, and gets in the moment it lets go.

### 1. The server resolves a path (no CLI change)

`GET /charter?path=a.b.rules.review` alongside today's `at=`/`depth=`. Walk the tree; answer the
node, the list, or the item. 404 says which half failed and, when a name exists elsewhere, where
it is.

Tests: a path to a node, to `rules`, to one rule, to `writes`; the greedy split against a
package named `rules` and the `--` escape; the not-found message naming the other charter.

**Built** ([versed/charterpath.go](../../versed/charterpath.go)). Three things worth recording:

- The reach did not stop at a rule. `rules.watcher.action` walks INTO the rule for free, because
  once a list is indexed by its elements' `name` the rest is ordinary object indexing — the same
  reason `verse state demo.pr.1.stage` never needed a rule for `.stage`.
- `--` is `?at=` on the wire. The client sends the address it means and the reach separately,
  which is the only thing the greedy split cannot express, rather than a second grammar.
- The 404 does two different things and had to be told apart: a first segment that is not a
  document key means no address resolved, and answering "baseline holds no nowhere.at.all"
  describes a failure nobody asked about.

Not built here: a reach into a SCOPE's charter (`?scope=`), which answers whole and has no tree
to walk. It falls out of step 3 if the CLI wants it.

### 2. `triggers:` → `rules:` in the tree

Contract, with a notice. Touches the tree builder, the CLI's own tree walking
(`charterHolding`), the docs, and the smoke. Do it before the CLI moves, so the CLI is written
once against the final shape.

**Built, and taken BEFORE step 1** — the same argument one step earlier: a path grammar whose
tests are written against a key that is about to move is tests written twice. Status's `triggers`
count came with it, in the same commit and the same notice: it is the same word for the same
thing, and renaming one surface is the drift this removes.

### 3. `verse c <path>` subsumes `show`, and the verbs become flags

The positional list goes to `path=`. `show` is deleted rather than aliased — no compatibility
path, per [contributing.md](contributing.md)'s standing rule while the users are the people
writing it. `--at` survives on `add` alone.

Tests: the CLI end of each path above; `--depth` still composes; a scope still answers.

**Built.** Two things it found:

- `address()` is the wrong joiner. It refuses to MIX the two spellings — `demo.pr a.b` is an
  error there, because an entity id may hold a dot and `a.b` could be one step or two. A charter
  path has no such ambiguity (no name may hold a delimiter, verified at install), so the steps
  are dot-joined plainly and `verse c verse-dev.drift rules watcher` — the address as one word,
  then the steps — is what somebody actually types.
- The ambiguity `show` had to ask about is GONE rather than moved. `TestARuleInAPackageIsReachable`
  asserted that two charters holding one name is a question settled with `--at`; under a path the
  name arrives with its charter, so the test now asserts each path answers its own and the
  refusal names both.

### 4. `verse c -r <path>` subsumes `remove` and `uninstall`

One verb over the same grammar: a path ending in a rule or level removes it, a path naming an
address takes the package down, `-r` for what is under it. The removal keeps what the package
WROTE, as it does now, and keeps saying so.

Tests: each shape; the refusal when children exist and no `-r`; a scope's tombstone wording;
and the case the last defect turned up — a record the engine never installed still goes.

**Built**, first as an `rm` verb and then as `-r`/`-R`: the verb kept the collision it was meant
to remove, and a flag has nothing to collide with. Three things:

- A path that reaches INTO a rule (`rules watcher action`) is refused rather than removing the
  rule it is inside. Editing a rule through a URL is what re-installing the file is for, and the
  refusal says what a removal may name.
- `-R` reaches a PLACE, and usually that is all it reaches: nothing is installed at `demo` while
  `demo.pr` and `demo.build` are under it. "uninstalled the charter at demo" is a sentence
  somebody re-checks, so the recursive case says "at and under".
- The install's file stops being a positional. `verse c add f.tony` read one; a positional is a
  path everywhere now, so the file is `-f` or stdin. A command whose argument changes meaning
  with a flag is what this grammar exists to remove.

### 5. A file is one request, applied as one step

The grouping moves to the server, and one `charterTx` spans the request: `at`/`eng` per step,
`fresh` as a set. File order throughout — for installing as well as for reporting, since no edge
exists to sort by (above).

No directory walk. A file already carries its packages, and the shell composes files better than
a flag would.

Tests: a two-package file, the second refused → nothing installed, and no engine left standing
at either address; the same file whole → both installed and both engines running; a package
reading another in the same file installs regardless of order; a rule that fired in the window
is not undone, which is the limit stated rather than a bug; and the whole install holds the lock
from step 0 for its duration, so a second operator's install waits or is told to come back.

**Built.** Three things:

- The undo closures had to CAPTURE their `at`/`eng`. They read `tx.at` and `tx.eng` at unwind
  time, which was right for a transaction that never moved and would have put a later package's
  declaration back over an earlier package's the first time one spanned two.
- The REPORT moved with the grouping. The CLI split the file to know what to print, so the
  grader that printed was not the grader that installed; the install answers with what it did,
  per address, and the CLI renders that.
- `codec.AsInt64` takes a Go value, not an `*ir.Node` — so reading the counts off the answer
  silently printed `installed 0 trigger(s)` for every package. Caught by driving it.

## What this does not do

- **It does not touch the charter FILE grammar.** `at:`, `reads:`, rules and levels are
  unchanged; this is the command, not the language.
- **It does not add a search command.** The old search survives as an error message, which is
  where it was useful.
- **It does not rename `add`.** `install` reads better against `uninstall`, but `uninstall` is
  the word going away. (Moot: the verbs became flags, so `add` is `-a`.)

## Open

- Does `verse c <path>` want `--paths` (one address per line, as `verse state` has) for
  scripting? Probably, and it is cheap; left out above to keep the first cut small.
- `rm` of a rule that a stratum still holds is refused today by the engine. The message should
  name the path to the stratum now that there is one.
