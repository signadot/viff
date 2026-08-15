# `comment-drift`: main moves, and its comments get read against the code

Status: drive scratch, on `use/verse-dev`. Installed on staging and working; its first fix is on
`main` as `aaebbc04`. Now reworked onto a **ledger** — findings are entities, the prompt carries
the outstanding ones, and the answer becomes facts — which is what
[derive-each-plan.md](derive-each-plan.md) was written to build. It has reviewed a
commit that changes Go code — two files, a sub-agent each, `{files: 2, stale: 0}` with a report
saying which comments it checked and why they still hold — landed as a fact on the subject.

Getting there cost eleven findings, three defects fixed on `main`, one upstream fix
(`xppke7xhh`, released as go-tony v0.0.126), and one thing the model cannot currently express
that every spawning charter depends on (finding 10).

It is the first charter of the [`verse-dev`](verse-dev.md) drive to act on this repository's own
commits — [step 3](verse-dev.md#3-commits-landing-as-facts), the one the note expected to hit a
wall somewhere else. It hit twenty — a crash, three defects, one thing the grammar documents
that the store cannot hold, one that is not a wall at all (what a developer sees when a commit
they did not write lands on main), and one the ledger shape produced on its first run. Three were defects in verse, all now fixed on `main`, and
**none of them could have been found by reading**: two of the three make every charter-spawned
task fail on the deployed runner, and the tests missed them because a test launches by hand.

## What it does

`main` moves. The git source reflects that as `git:head:verse`, one entity carrying the tip.
For every Go file that commit changed, an agent reads the file and the commit's change to it,
finds every comment that no longer agrees with the code it is attached to — a doc comment naming
a parameter that is gone, a rationale for a branch that was deleted, a `returns X` that returns
Y — and **fixes it**, leaving the edits on `refs/heads/verse/auto/<task>` for somebody to diff.

The deliverable is a branch rather than prose, because a stale comment is cheap to read as a
diff and expensive to read as a paragraph about a diff, and a branch is droppable in a way a
report is not. Delivery is the runner's act: the token lives in its process, outside the
sandbox, and the task only proposes the push at the end, carrying an effect the vector sees. A
run that changed nothing proposes nothing, so most commits leave no branch — which is the
outcome to expect, and the first two real reviews had it.

Not "the comments are thin here". A comment that is *absent* is a different complaint and a
much cheaper one to make; the value is in the ones that are confidently wrong, because those are
what a reader believes.

| | |
|---|---|
| `verse-dev/comment-drift.tony` (on `use/verse-dev`) | the charter — two strata, two rules |
| `verse-dev/comment-drift-install.sh` (on `use/verse-dev`) | (re)installs it |
| `deploy/coderunner/coderunner.yaml` | a `verse-fix` environment: a push target (`verse/auto/<task>`), a shell, `write_file` |

Running it needs `make deploy PROFILE=staging` (for the environment) and then the install
script. The result of a review is a fact:

```sh
verse -addr verse:9797 entity get 'drift:head:head-moved@git:head:verse'
```

## Finding 1: a reflected entity cannot hold a latch

A spawning rule must take its subject out of its own selection, or every commit that touches
what it reads spawns again; `trigger/latch.go` refuses one that cannot. The ordinary way is to
write a marker onto the subject and select on its absence.

A subject in a reflected slice cannot do that. **A source is the single writer of what it
reflects, and converge REPLACES** — [sources.md](../sources.md), and `source/reflector.go`. A
marker a charter writes onto `git:*` is gone on the next pass that mentions the entity, and the
rule fires again, forever, paced by the resync interval. The cycle throttle does not engage and
should not: [a paced cycle is a control loop](../cycle-throttle.md).

Watching it happen took one command. Writing a fake sha onto `git:head:verse` by hand held for
under a minute before converge put the real one back.

So: a spawning rule needs a latch on its subject; a reflected entity cannot hold one; **no
spawning rule may take a reflected entity as its subject.** Every charter over a source must
copy the fact into a slice it owns and latch there — which means `derive`, which means declaring
[strata](../strata.md). That is a large first step for what reads like a one-rule charter, and
nothing says it: sources.md and trigger-model.md are each correct, and the consequence lives
between them.

The charter already in the tree is the evidence. [`verse-dev/docs-drift.tony`](verse-dev/docs-drift.tony)
is a spawn on `git:commit` with no latch, marked `NOT installed yet on purpose`. It could not
have been installed:

```
trigger "docs-drift": this action spawns but writes nothing its subject reads (repo), so every
commit that touches it spawns again
```

The check is right and, here, its advice is wrong: `patch one of those paths` is what produces
the loop above. Worth fixing on `main` — when a subject's system belongs to a registered source,
the refusal should say so and point at `derive`.

## Finding 2: width is the thing to design against, and 131 was fatal

The first version subjected the commits: `{system: git, kind: commit, value: {repo: verse}}`,
one derived fact per commit, one review per commit. It is the shape the problem asks for.

A subject is a **selection**, and git commits are reflected under no cover so history
accumulates — 131 of them on staging, none of which age out. So that rule is 131 firings, at
every commit, forever. That is the cost everyone expects to argue about. What actually happened
is not a cost argument:

```
WARN patch did not land; the firing's state transition was lost chain=commit-seen
  ref=drift:commit:commit-seen@git:commit:verse_c2c3e69… err="… context deadline exceeded"
```

**All 131 firings timed out at 15s inside the store, and every derive was lost.** `verse status`
reported `fired: 131` next to an empty `drift:*`. A single derive, installed alone against one
named commit, lands instantly — so it is the width, not the write.

That moved the charter to `git:head:verse`: one entity, carrying main's tip, changing exactly
when main moves. Width one, same event. The cost is real and named in the charter — a push of
three commits is reviewed as its tip, so the two underneath it are not read.

Two things follow that are bigger than this charter.

**Width is not visible in a rule.** `{system: git, kind: commit}` and `git:head:verse` look
equally innocent. One is a selection over an unbounded, accumulating slice and the other is one
entity, and nothing at install says which — no count, no warning, no "this rule will fire 131
times when you press enter". The engine knows: it is a `List` prefilter away.

**There is no "from here on".** A match asks what is there, never when it arrived, and a commit
payload carries no timestamp — `{repo, sha, msg, author, parents, trailers}` — so "the ones from
now on" is not sayable even by hand. The first version of the install script therefore had a
seeding step: mint the facts, mark everything standing as already reviewed, and only then
install the rule that spawns. Moving to `git:head` deleted that step. It will be back for the
next charter over a source with history in it.

## Finding 3: a changed file is not an entity — and the answer's findings now are

The unit of this review is a FILE and the firing is about a COMMIT. verse fans out per entity,
one firing per selected member, and there is no entity for "a file this commit changed" — so
that fan-out cannot be in the charter. It lands inside the task, as `call_agent` per path, and
still does.

What changed is the other end. This was the fifth arrival at
[derive-residual.md](derive-residual.md)'s missing primitive — *a task can find N things, and
verse cannot make them N facts* — and it is the one that built it: `!each` walks a task's answer
and derives one entity per element ([strata.md](../strata.md#each--when-one-answer-holds-many-facts)).
So the FILE is still not an entity and the FINDING is, which turns out to be the grain that
matters: the charter now remembers what it found, a person can retire a finding by writing to
it, and a review reads its own memory instead of re-reading the tree.

The unit of a finding is a **symbol**, not a comment — `entity.nodeToPayload`,
`source/github.Reflect`. An identity that survives the comment being rewritten, which is what
resolving a finding does to it, and which a line number does not survive at all.

## Finding 4: the commit carries no paths, so the task needs a shell

`git:commit` has the sha, the subject line, the author, the parents and the trailers. Not the
files. `git:head` has less. So the task has to run `git show --name-only` to find out what it is
reviewing, which is why `verse-fix` is the only environment on that runner with `run_bash` and
`git` — a thing the image's own Dockerfile withholds on purpose.

It is not the weakening it looks like, and the reason is worth keeping: the push credential is
not in that environment's `env:`, it lives in the runner's process outside the sandbox, and
`egress:` has no route to github.com. `git` in there can read the clone and reach nothing. But
the honest fix is upstream — **reflect the file list** — and it is one `git diff-tree` per
commit. Then the subject predicate could select the commits worth reviewing at all, the prompt
could name the paths, and this environment would go back to the file tools alone.

## Finding 5: no grant means "read, and delegate"

The task fans out, so it needs `CallAgent`. The vocabulary in `task/authority.go` is four names:
`read-only` is reads alone, `no-delegate` is *defined* by excluding CallAgent, `none` grants
nothing, `all` is unbounded. So a fan-out task can only be spelled `grant: all` — which also
hands it `Resolve`, the verb for answering gate questions.

Inert today: `vector: auto`, no resolver attached, nothing to resolve. It stops being inert the
moment somebody attaches a resolver to this rule, and the charter would not have to change for
that to happen. What actually bounds this task is the environment's `tools:` list and bwrap. The
grant is a word naming something wider than the deployment allows.

## Finding 6: no charter-spawned task could run on the deployed coderunner

Every spawn this charter made came back `Post "http://coderunner:9802/tasks": EOF`, and the
runner's log had a panic in it. The id is the reason. A trigger's task id is `<chain>@<subject>`
— here `comment-drift@drift:head:head-moved@git:head:verse` — and the hop builds each child's
control url as `"http://task."+id`. The `@` makes the rest of it userinfo, the subject's colons
then read as a port, and the url does not parse. `http.NewRequest` answers that with a **nil
request**, all three call sites in `runner/http.go` discarded the error, and `Client.Do`
dereferenced it.

`review@demo:pr:1` — the tutorial's own charter — is enough to reproduce it. So no charter had
ever successfully spawned onto this runner, and the tests did not catch it because they launch
by hand, where ids are `run-<hex>`. Fixed on `main` (`62703518`), with both regression tests
using a trigger-shaped id.

Two things this drive should keep saying: the wall was found by *deploying* rather than by
reading, and it was found in a minute rather than an hour because the charter's `onFailed`
branch wrote the error onto the entity. The store carried the diagnosis to the same place the
result would have gone.

## Finding 7: a finished task id could not be run again

With the subject narrowed to one entity, the task id is constant forever —
`comment-drift@drift:head:head-moved@git:head:verse`, every time. The second review came back
`502: the process running task … did not answer: dial unix …: no such file or directory`, and
so did every one after it until the runner was restarted.

The hop keeps a child's record after its process is reaped, so `task get` can still answer
about a task that has ended, and its spawn path short-circuits a known id into that child's
proxy — which was written for retry-safety while a task is *running*. After the reap the record
is there and the socket is not.

A repeat is not an anomaly: the engine mints `<chain>@<subject>`, deterministic on purpose, and
its dedupe holds only while the task is live — `trigger/latch.go` says so in as many words, and
the whole latch discipline is built on it. So a rule firing twice about one subject IS one id
asked for twice. Fixed on `main` (`04eaeb8b`): the short-circuit applies only while the process
is alive.

Worth noticing which shape found it. With a subject per commit the ids all differ and this
never shows — so finding 2's forced narrowing is what surfaced it.

## Finding 8: staging's tags are mutable and its pull policy was not

The fix above then failed identically, twice. `make coderunner-push` and `rollout restart`
both reported success and the pod kept running the old binary: kubernetes defaults
`imagePullPolicy` to `IfNotPresent` for every tag that is not literally `:latest`, the
coderunner's is `:staging`, and the node already had it. Comparing the running digest against
the pushed one is what ended it.

Fixed on `main` (`1387a922`): the staging profile sets `Always`, which is what a mutable tag
actually requires. The knob already existed for the opposite case.

## Finding 9: a long-narrating task crashes the engine, from inside go-tony

The first review of a commit that actually changed Go code took versed down. It restarted;
the panic is upstream, and verse is the caller:

```
panic: runtime error: index out of range [3] with length 3
  go-tony/libdiff.PatchStringMultiLine    libdiff/patch_string.go:117
  go-tony/mergeop.strDiffOp.Patch         mergeop/strdiff.go:63
  go-tony.Patch
  verse/entity.(*LogdStore).WatchSubtree.func1    entity/logdstore.go:768
```

A watcher applying a store delta that carried a **multi-line string diff**. Filed on
tony-format as `xppke7xhh`, fixed upstream, and verse now pins go-tony v0.0.126 (`1b154855`).
Confirmed on the same workload that killed it: the review that took the engine down ran again,
for eleven minutes and thirteen boundaries, and the engine did not move.

What makes it this charter's finding rather than a random crash is which run hit it. The two
earlier reviews answered in three boundaries and a sentence; this one narrated across a dozen,
with file listings in it — so it is the first task here to rewrite a long multi-line value
often enough for the store to encode a delta as a string diff rather than a whole value. Every
charter that spawns a talkative agent is on this path, and nothing about the charter can avoid
it.

The blast radius is the interesting part. The task itself was fine — it kept running in the
coderunner, reading files, unaware — but the ENGINE watching it died, so its `onDone` had
nobody to fire it. The result was computed and had nowhere to land, and `drift:head:…` still
carries the new sha with no `reviewed`. A task outliving its engine is designed for
(reconcile re-attaches); this one was not re-attached, and it is not mirrored as a
`task:instance` either, so from verse's side the work simply is not there.

## Finding 10: verse cannot store a bare null, and the documented latch is spelled with one

The biggest one, and it was found last because it only shows after a restart.

A spawning rule must latch, and [trigger-model.md](../trigger-model.md) names the shape for a
latch whose value the charter cannot predict: **ask for the ABSENCE of what the action writes**
— `value: !not {reviewed: null}` with a hook that writes `reviewed`. This charter did exactly
that, and it worked, until versed restarted. Then it selected nothing, silently, and the head
moved past two commits with no review and no error anywhere. The durable rule reads back as
`value: !not {}` — and as a match, `{}` means "any object", so `!not` of it selects nothing.

**The layer is verse's own**, which is worth stating precisely because the first diagnosis
written here blamed the substrate and was wrong on every point that could be tested.

Not tony: a null in a patch SETS null (`doPatchWith` falls through to `Clone`), and the diff
layer names the three cases apart — `!delete null`, `!insert null`, `!replace {from: 2, to:
null}`. Not logd: `b: null` reads back as `b: null` through write, restart, `SwitchDLog` and a
compaction that dropped 303 of 304 entries, and inside a scope layered over a baseline holding a
different value. Not `!raw`, which is the opposite of useless — it is precisely what carries an
operator-tagged subtree through, and the thing it protects against fails LOUDLY on the first
read (`not patching "null" gave cannot patch with not operation`) rather than silently after a
restart.

It is `entity/logdstore.go`, and it is deliberate, documented, and a matched pair:

```go
// A TAGGED null is not a tombstone but a value, and is kept. Absence is spelled by the store
// and by nothing else: every null verse writes to remove something … is written bare
func nodeToPayload(node *ir.Node) Payload {
	if node == nil || (node.Type == ir.NullType && node.Tag == "") { return nil }
```

The write half is `addRemovedNulls`, which appends a **bare null** for every field a `Put`
dropped — that is how a patch-shaped write gets replace semantics — and `Write` with a nil value
writes one to remove an entity or a field. So verse spells absence by writing a null and hiding
every null it reads, at any depth, arrays excepted. The pair is self-consistent and it works.
What it costs is that **no bare null can be stored as data anywhere in the verse**, including
inside a charter's own `!raw` spec, where by read time the escape has been consumed and what is
left is an ordinary object for the stripper to walk into.

So the grammar has a spelling that install accepts, that the engine honours in memory, and that
the store cannot hold. Every rule using the documented absence latch is one restart away from
being dead, and nothing says a word.

A TAGGED null survives — this charter's `result: !delete null` round-trips intact — which is
both the shape of the fix and the shape of the workaround. The charter now latches on a state
(`reviewed: pending`, written by the derive, written past by the hooks) and touches no bare null
anywhere. That is strictly better than what it replaced: re-firing rule 1 with an unchanged head
now writes the same bytes and dies as a no-op, where the absence version re-armed the latch and
re-reviewed the tip on every restart.

Three places it could be answered, and the first is now the interesting one:

- **Verse's absence convention moves onto `!delete`.** The write sites emit `!delete null`
  instead of a bare one, and `nodeToPayload` stops stripping. That is what the substrate
  actually supports, and it gives the grammar its null back at every depth. It is a `contract`
  change and the tier is not a formality: what a `--data` directory holds changes meaning, and
  entities written under the old convention carry bare nulls that would stop being hidden.
- **Install refuses it.** A subject predicate carrying a bare null cannot survive being stored,
  which is a static property of the rule — the same class of check as the latch check that
  already refuses a spawn with nothing to advance. Cheap, and it turns a silent death into a
  refusal at the moment somebody types it.
- **The docs stop teaching it.** trigger-model.md names the absence latch twice and it is
  currently unimplementable as written; a state latch is what works.

## Finding 11: the .tony charters in this tree are not checked

`TestDocCharartersInstall` installs every charter fenced in a `docs/**/*.md`, which is why the
tutorial's rules cannot rot. It globs markdown only — so `verse-dev/docs-drift.tony`, the
coderunner config beside it, and this charter are unchecked. Finding 1 was confirmed by running
those files through that test's own machinery by hand. Globbing `docs/*/*.tony` too is a small
change to a test, and it would have caught `docs-drift.tony` on the day it landed.

## Finding 12: a delivery has no commit subject

The branch the first fix landed on carried this as its subject line:

```
Network-restricted, unrelated to our change (comment-only edit can't cau
```

That is the model's last sentence — an aside about a build it could not run, because egress is
restricted — truncated to fit. The delivery has no notion of a subject: whatever the task said
last becomes the commit message, and the trailers (`Verse-Task:`, `Verse-Environment:`) are the
only part of it composed by anything that knows what happened.

It is the difference between a branch somebody skims and one they have to open, which for a
deliverable that is *meant* to be skimmed is most of its value. Two places it could be
answered, and they are not the same answer: the charter could put a `subject:` in its answer
contract and the runner could use it — which makes the message the charter's to shape, like the
prompt — or the runner could compose one from the task name, which is always available and
never wrong, and never says what changed either.

## Finding 13: "a commit I didn't make has landed"

The charter's second real review found something, pushed a branch, and the branch was merged.
The next person to run `git log` on main saw this:

```
69861bf0 files: 2
```

Authored by `verse coderunner <coderunner@invalid>`, subject `files: 2` — the first field of the
answer document. It cost four commands to establish that no agent had pushed to `main`: read the
commit, read its parent, read the reflog, read the remote heads. That was **me**, with every
piece of context about what had been running. A developer pulling next week has none of it.

This is the finding the whole drive was walking toward without noticing, and it is social before
it is technical: **the moment this is useful, it produces commits nobody wrote.** Three things
decide whether that reads as a system working or as an incident.

**The subject line, which is finding 12 and is no longer cosmetic.** It is the only part a
reader sees before deciding whether to be alarmed, and `files: 2` answers nothing.

**The trailers, which already work.** What actually resolved it was the four lines the runner
composes:

```
Verse-Task: comment-drift@drift:head:head-moved@git:head:verse
Verse-Subject: drift:head:head-moved@git:head:verse
Verse-Chain: comment-drift
Verse-Environment: verse-fix
```

That is a complete account of what produced the commit — which rule, about which subject, in
which environment — and `git log --author='verse coderunner'` is the audit of everything the
mechanism has ever landed. Keep them; they are the part that paid.

**Who accepted it, which nothing records.** The surprise was not the agent — it branched exactly
as designed and pushed nowhere near `main`. It was the FAST-FORWARD: a merge that leaves no trace
of the person who took it, so the history reads as though the branch arrived on its own. Merging
an agent's branch is a human act and should look like one: `--no-ff`, so `Merge branch
'verse/auto/…'` carries a name and a moment. That is the cheapest available answer to "who
decided this was alright", and it costs one flag.

And the branches are now `verse/auto/<task>` rather than `agent/<task>` (`da59af01`), for the
same reason: the first segments of a branch name are where somebody asks what made this, and
`agent/` said neither which system nor that nothing human proposed it.

## Finding 14: its own fix re-triggers it, and that is how it converges

A fix changes a file; the commit that lands it is a commit that changed a file; the charter
reviews it. So the charter always gets a second look at its own work, unprompted.

That could be a loop — a paced one, through the world, which the cycle throttle deliberately does
not catch — and instead it was the best thing that happened. Run one fixed the two comments it
was pointed at. Run two, reviewing THAT commit, found three more instances of the same false
claim in the same files, in places the first pass never touched: `addRemovedNulls` ("it appends
an explicit null so *logd* tombstones it"), `Get`'s doc ("that is how *the log* spells a
tombstone"), and the claim-replay comment. Same error, same class, swept up on the second pass.

It converges when a pass finds nothing, and nothing else bounds it. Which is fine here and is
worth stating as a property rather than discovering as a surprise: **the charter's own output is
its next input**, so a rule that is wrong about what counts as stale does not settle, it
oscillates. The bound during the drive is the same one it started with — a person reading the
branches.

## Finding 15: the tip moves while a review runs, and the result lands on the newer fact

The first run of the ledger shape produced this, and it is worth reading twice:

```
sha:    feedeef6…
report: "Commit 068cc5bb… modifies only Makefile and deploy/profiles/staging.tony …"
```

The review was of `068cc5bb`. While it ran, main moved, the git source reflected the new tip,
`head-moved` re-derived, and the subject's `sha` became `feedeef6`. The result then landed on
that fact — so the entity says one commit and its own report says another, and every reader
after that inherits the wrong one.

It is width-one's bill, arriving. One entity per repository is what made the charter affordable
(finding 2), and the cost is that the fact is a MOVING one: it names the current tip, not the
tip the running task was told about. Nothing is racing in the store — every write is ordered and
correct — the mismatch is between what a task was asked and where its answer is filed.

The fix is small and is the general lesson: **an answer should say what it is about.** The
contract gains `at`, the prompt tells the agent to report the sha it was given, and `onDone`
patches `reviewedSha: $data.at` — read from the ANSWER, never from the subject, which in a
completion hook is not even available. A fact that names its own subject matter survives its
neighbour moving underneath it.

What it does not fix, and is left as the honest limit: a review of an older commit can still
overwrite a newer review's result, because both land on one entity. Per-sha facts would settle
it and cost the width this shape exists to avoid. Nobody has been bitten by that ordering yet.

## Finding 16: the contract is checked but never shown

The first review to fail on this charter failed twice in a row, on the answer rather than on the
work:

```
answer contract not satisfied after 2 repair attempt(s): the final message carries no answer
document: imbalanced document: key not followed by : got TLiteral "touches" … (line=4, col=20)
```

Attempt one was a sentence — *"No Go files were touched by this commit …"*. Attempt two was a
document whose `report:` held that same sentence unquoted, which tony reads as more keys and
refuses at the first word that is not one.

Both are reasonable readings of what the agent was told, and that is the finding. The contract
is a MATCH PATTERN — `report: !irtype "text"` — and the repair hands the model that pattern,
which is the right thing to send and is not sufficient: a pattern specifies a shape and says
nothing about how to write one. Nothing in the prompt or the repair showed a DOCUMENT.

Two fixes, and they are at different levels. The charter now shows the exact answer in the
prompt, quoted, including the empty case — a form to fill in rather than a shape to satisfy.
And `answer`'s repair for an unparseable message states the one mechanical fact the contract
cannot carry, since it is about tony rather than about this contract: write a document, quote
every string, unquoted text after a key is read as more keys.

Worth noting what it cost, because it is the cost of an answer contract in general: the review
had already done the work. The reading, the judgement and the diff were all finished, and the
run failed on the shape of the sentence describing them. A contract is still worth having — the
alternative is `stale: "No Go files were touched"` landing in a field a charter reads as a
count — but the tax falls on the last two turns of a task that has otherwise succeeded, and
that is where it should be spent carefully.

## Finding 17: a ledger's answer is a DIFF, and two statuses do not cover three cases

Caught by reading the charter rather than by driving it, which is worth saying because the two
mistakes would have been slow and quiet respectively.

**Three cases, two statuses.** The prompt asked for "one entry per symbol you judged, whether or
not you changed it" and offered `status: open` or `status: resolved`. A symbol whose comments are
simply FINE is neither, so the model had to pick one, and the cheap pick is `open` — which mints
a finding against a healthy symbol. Append-only means it then stays forever, is handed to the
next review as outstanding, and is re-judged fine, forever. A ledger that fills with things that
are not wrong is worse than no ledger, because the second review has to read them before it can
find the first real one.

So an entry is a CLAIM, not a verdict: `open` when the comments disagree and still do,
`resolved` when they disagreed and no longer do. A symbol judged accurate is not an entry.

**An answer is what changed.** The same paragraph told the agent that an outstanding finding it
did not resolve "must appear again — one you leave out is forgotten". That is false, and it is
false because of a decision made deliberately one layer down: the fan-out is APPEND-ONLY, so a
finding nobody mentions keeps its entity and is gathered again next time. Re-listing it costs
tokens on every run and grows the answer with the ledger rather than with the work — which for a
long-lived ledger is the difference between an answer and a transcript.

The instruction was written before `!each` existed, when "the answer is the whole state" was
true. It stopped being true and nothing pointed at it, which is the ordinary way a prompt goes
stale: prose about a mechanism, sitting next to the mechanism, agreeing with it only on the day
it was written. This charter exists to catch exactly that in Go comments; it did not catch it in
itself.

## Finding 18, and a note on where a format is taught

Two things about the answer, one structural and one that was simply inconsistent.

**The prompt and the repair disagreed about the format.** The charter asked for a tony document
and showed one; `answer`'s repair — after three failures across two charters' worth of runs —
now shows a JSON instance rendered from the contract. So a model heard "write tony" first and
"json is easiest, here is one" only once it had already failed, which is the worst possible
order: the expensive turns are spent on the format it is least reliable at, and the reliable one
is offered as a consolation. The charter now asks for JSON up front, in the same words the
repair uses. Nothing about the contract changed — it is a match, and a match does not care which
dialect satisfied it, because valid JSON is valid tony.

That is worth generalising: **a format is taught in the prompt and enforced in the contract, and
those are two different places that can disagree without anything noticing.** The contract cannot
state a dialect (it is a pattern over values), so the prompt is the only place a charter says
how to write, and it is prose — which is exactly the material this whole drive is about going
stale.

**And a fix arrives as a branch off a moving base.** The workspace is cloned at `base: main`
while the review is ABOUT a commit, so a fix lands as a branch off whatever the clone held. The
first real fix demonstrated it harmlessly: the review of `bbc94ee7` produced
`verse/auto/comment-drift_…` fixing both planted comments, by which time main already carried
the same two fixes carried out of the runner by hand — so the branch is a rewording of work
already done rather than a fix. Benign once. It would not be if two reviews overlapped on one
file, or if a person and a review fixed the same symbol differently.

The ledger was right either way, which is the part worth noticing: both findings read
`status: resolved`, which they are, whoever resolved them.

## Finding 19: a task id is a NAME, and nothing names an occurrence

`<chain>@<subject>` is deterministic on purpose, and the purpose is load-bearing three times
over: it is what makes a re-fire idempotent (the engine dedups a spawn against a live task of
the same id), what scopes a completion hook to the run that raised it, and what `verse flow
demo:pr:1` addresses. It answers *the review of this subject* — a relation, not an occurrence.

Nothing answers *this run*, and three consumers wanted one before anybody noticed they were the
same want:

- **The branch a fix lands on.** `refs/heads/verse/auto/$task` is one ref for every review this
  charter will ever do, because its subject never changes. Two fixes overwrote their
  predecessors before it cost anything; the third would have been unmerged work. Fixed by
  putting the base sha in the target (`$sha/$task`).
- **The task's own record.** `task:instance:<id>` is ONE entity, so a second run overwrites the
  first: the `started`/`ended` stamps added the same morning describe only the latest
  occurrence, and a failed run followed by a successful one leaves no trace of the failure.
- **The `at:` on every finding.** The charter asks the ANSWER to carry the sha, which is the
  same gap being routed around a third time — by hand, in a prompt, because the id could not
  carry it.

The fix is not to make ids unique: that would break the dedupe the determinism buys, and the
name is right. It is that an OCCURRENCE should have a discriminator the record can carry — the
causing commit is in hand at spawn time, and `verse.meta.derivedFrom` already shows the engine
is comfortable stamping provenance it knows. Then "which run" is answerable without changing
what "which work" means.

Worth noticing how it presented: as three unrelated bugs, one per consumer, each with a local
fix. It only reads as one thing when the third arrives.

## Finding 20: a derive dereferences every element, and one omission costs a whole review

`!each` writes `value: {symbol: $item.symbol, status: $item.status, says: $item.says, at:
$item.at}`, and an absent reference fails the firing — deliberately, since the store distinguishes
an absent key from a null one and dropping it would launder "the task never said" into "the task
said nothing".

So an answer whose twelve findings include one without `says` derives NONE of them. That happened
here on a review that had already read its files, made its edits and written its report:
`$item.says is not present in the payload that fired this action`, and the work was gone.

It is the answer-contract failure again, one layer further in, and it has the same fix: state the
element shape where there is still a repair loop. `findings: !all {symbol: …, status: …, says: …,
at: …}` constrains every element, so the omission is caught when the model can still be asked for
it rather than after the tokens are spent. The contract is now written that way, and the
exemplars inside it are real values rather than `"text"` — `!irtype` reads only their type, so
making them meaningful costs nothing and makes the example the repair renders something a reader
can copy.

## What worked, and is worth saying

**The answer contract earned its keep on the very first real run.** Asked about a docs-only
commit, the agent ran the git command, read the empty output, and answered `No Go files were
changed by this commit.` — in prose. The contract rejected it and the run failed rather than
patching a sentence into a field the charter reads as a count. The retry answered in shape.
Without it, `stale: "No Go files were changed by this commit."` would be sitting on that entity
looking like data.

**The fix-and-push path works, and its first output is on `main`.** `aaebbc04` — two comments
that claimed logd spells a tombstone as a bare null, corrected to say that the convention is
verse's own. The agent found the matched pair in `entity/logdstore.go` by itself, named both
halves, kept the voice of the comments around it, changed no code, and left the tree gofmt
clean. It is cherry-picked with the coderunner as its author, because that is what happened.

The commit it fixes is worth the irony: the wrong comment is what this drive's own finding 10
first believed, and the note said so in a paragraph that had to be rewritten. A charter that
reads comments against code caught, on its second real job, the class of error that had already
cost its author an evening.

**`onFailed` is how the wall got diagnosed.** Every failure above arrived as a fact on the
subject — the 502, the dial error, the refusal — in the same place the result would have gone.
Three defects were found by reading one entity.

**The whole loop runs.** A commit lands on `main`, the source reflects it inside a minute, the
derive re-arms the latch, one task fans out to a sub-agent per changed Go file, the answer
contract holds the shape, and the verdict lands as a fact on the subject that a `verse entity
get` prints. That is [verse-dev](verse-dev.md) step 3 working, on this repository's own history,
including a review of the commit that fixed the runner it ran on.

**A patch merges, so two endings must clear each other.** `onDone` writing `result` and
`onFailed` writing `err` left the failure's `err` standing beside the next run's `reviewed:
done`: a fact that reads as a successful failure. Each hook now deletes the other's key. This is
the ordinary shape of every charter that has more than one kind of ending, and nothing warns
about it.

## What is still untried

- **A commit with a comment that IS stale.** Both real reviews so far were of commits whose
  comments were written with the code, and both correctly reported nothing. That is the harder
  half of the question answered — a model asked to find fault in something that is fine did not
  invent any, twice, and its reasoning is legible in the runner's log ("`r.running()` naming
  coincidentally matches new `c.running()` but no conflict — different scope/type"). What is
  untested is whether it catches one that is really there.
- What a fan-out of ten `call_agent` children costs against one task reading ten files itself.
- Whether the answer contract survives a real report — the empty one satisfied it, and a
  multi-line `report:` is a different question.
- One loose end, recorded rather than chased: the run that failed its answer contract wrote
  `err: placeholder` onto the entity. The runner's log had the real story; the fact had a word
  that says nothing. Where that string comes from is not in this tree.
