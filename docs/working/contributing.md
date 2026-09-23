# Contributing: the remote as the place we meet

Status: proposal. A process note — nothing here describes code, and nothing is in force until
we say it is.

It is subject to itself: disagree by editing it and pushing to `main`, and mark what turned
out wrong rather than deleting it ([working/README.md](README.md)). It leaves `working/` like
any other note — when this is what we actually do, it lands as root `CONTRIBUTING.md` and this
file goes away.

It serves [the charter](charter.md): the charter says what verse is for and how work under it
is owned, and this note is how the repository carries that out. Where the two disagree the
charter governs, and this one gets edited.

## Where we are

The core is stablish; the trigger model got there by iterating against itself, and ten `!`
commits on this history are what that cost. Two hundred–odd commits, effectively one hand,
straight onto `main`. No pull requests, no CI, no tags.

The goal now is to get verse *driven*, by the handful of us who will engage, and let what the
driving finds set the agenda. The remote stops being a backup and starts being where we meet.

The charter names its owners and leaves it to each of them how to organise building, review
and fixes, with `main` kept fast-forward only. So what follows is a recommendation carrying its
reasons, not a rule: take it, adapt it, or say what you do instead. The exception is what a
commit *declares*, which is not yours alone because it is what everybody else reads.

The order below is the priority order: dogfooding, then how thinking gets shared, then the
push mechanics that serve both.

## What changes, and what does not

**Push to `main` stays.** At this size the review queue *is* the whole cost.

What changes is the cost of being wrong — no longer "I'll notice tomorrow" but "somebody's
charter stopped installing and they can't tell whether it's their fault." So every obligation
below is *announcement, not permission*. Nobody has to say yes; somebody has to be able to
find out what happened.

**A revert needs no ceremony.** It is how a trunk stays usable across time zones, not an
accusation.

## Dogfooding: a drive has a name

The unit is a **drive**: somebody using verse for something real, long enough to hit walls. It
gets a short name (`verse-dev`, `release-flow`) that ties three things together:

| | |
|---|---|
| `use/<name>` | a branch on `origin` — charters, seeds, scratch config, hacks |
| `dogfood/<name>.N` | tags: the points on `main` this drive has stood on |
| `docs/working/<name>-*.md` | what the drive found — the actual deliverable |

### Tags: ground that does not move

`main` may move breakingly; a drive needs stable ground. **So nobody dogfooding stands on
`main`.** `dogfood/<name>.N` marks the commit a drive is standing on; `N` advances when it
deliberately moves. Not a release — a record of where one drive's ground is.

Per-drive rather than per-date, because then nobody owns cutting tags: you cut your own. Two
drives on different points of `main` is a normal, visible state.

Cut only with `make check && make test && ./smoke.sh` green on that commit, and the drive
actually run against **that build**. The message says what moved; the breaking half is
generated, never recalled (`Breaks:` is defined [below](#say-what-you-held-yourself-to)):

```sh
fmt='%h %s ::%(trailers:key=Breaks,valueonly,separator=%x20)'         # :: ends the subject
prev=$(git tag -l --sort=v:refname 'dogfood/verse-dev.*' | tail -1)   # v: sorts .10 after .2
git log --format="$fmt" "$prev"..HEAD | grep -v '::$'                 # drop the trailerless
git tag -a dogfood/verse-dev.4        # paste that in, add a sentence of what moved
git push origin dogfood/verse-dev.4   # tags are NOT pushed by a bare git push
```

Upgrading is: read the `Breaks:` lines between your tag and the new one. That is the entire
coupling between somebody filing an edge off on Tuesday and somebody driving a flow on Friday.

**"Driven" is about the build, not about the commit**, and the difference stops being pedantic
the moment a drive's subject is this repository. `verse-dev` runs a charter over verse's own
commits: driving it PRODUCES commits — a fix from a review, a defect found and landed — so a
tag that required its own sha to have been the subject of a run would be chasing something it
moves by looking at it. Two hours of that is how this paragraph got written.

So the tag asserts the ground is usable: these checks passed here, and the drive ran on a build
carrying this code. It does not assert that this commit was reviewed, deployed first, or
reached quiescence — none of which a self-referential drive can ever quite reach.

**`verse-dev` stood on `main`, deliberately, and it is the exception this rule is worth
stating against.** `make ship` deploys the checkout's HEAD, so staging ran main's tip rather
than any tag — read literally, the drive was doing the one thing the paragraph above says nobody
dogfooding does.

It was right anyway, and the reason names what the rule is FOR: a tag protects a drive from
OTHER PEOPLE's churn, from `main` moving breakingly under a flow somebody is halfway through.
`verse-dev`'s churn is entirely its own — one hand, and the changes are the drive — so pinning
would only mean waiting for a tag to see your own fix, which is the loop above in a different
hat. What the tag means here is what it means to everybody else: a point to upgrade from, a
name an announcement can carry, the ground a SECOND drive stands on so this one's churn does
not reach it.

Deploying a tag is one command when that is what you want — `git checkout dogfood/<name>.N &&
make ship`, which the refuse-if-dirty guard and the immutable image ref both survive.

**A shared instance is named separately.** `deploy/<I>.N` says what instance `I` is running,
and its annotation lists the components ([charter](charter.md)). A `dogfood/<name>.N` tag is
one drive's ground; a `deploy/<I>.N` tag is the ground of everybody dialling into `I`, which
is why deploying one is owned work rather than a thing anyone does in passing.

**And a tag is cut when somebody needs ground**, not when main moves. `N` advances when a drive
deliberately moves, which is a decision somebody makes for a reason: a new drive starting, an
upgrade being coordinated, an announcement that wants a name. Cutting one per improvement turns
"ground that does not move" into a changelog with worse ergonomics, and it is the same instinct
that makes people tag releases nobody asked for.

Not semver — a version number implies a compatibility promise, and the `contract` tier says we
are not making one. Revisit when there is a consumer outside this handful.

### `use/<name>`: what `origin` is for

`origin` carries `main`, `dogfood/*` tags, and `use/*` branches — nothing else. Twenty topic
branches sit in this checkout unpushed and none of them needed pushing; a remote branch list
that is mostly abandoned is one nobody reads.

`use/<name>` is where you get to be at "I made it work" without owing anybody "I made it
right" — including hacks to verse itself. **It is read, not merged.** Two things come off it,
both rewrites: fixes landed on `main` under a tier, and a working note. The note is the
deliverable even when no code changes (`931fd51` — five tutorial sections, four defects the
driving turned up).

Force-push your own `use/` branch freely. Never force-push `main`.

### The first drive, and what came after it

[`verse-dev`](residuals.md) (`rd9c5f8r`) — verse coordinating verse — is done. Contributing to verse
*is* an SDLC flow, which is the shape verse claims to run, so it was the fastest honest feedback
available: the tree as entities, and commits landing as facts. Both walked into walls, which was
the point, and what it bootstrapped is now carried by targeted efforts instead — the comment
drift charters that run, and the pieces named in
[owners.tony](owners.tony). Its residual has what it left.

## From a note, to a plan, to a doc

The shape a contribution takes — and what makes work reviewable *without* review. Already the
practice; just unwritten.

**1. `docs/working/$X.md` — the note.** What is wrong, what you propose, why. Opens
`Status: proposal, nothing here runs`. Push it early: cheap to disagree with in week one,
expensive to discover in week three.

A claim over the note names the note — `work: task-budget` claims `work/task-budget.md` — and
lives in [owners.tony](owners.tony).

**A note whose subject is a document outside this repo carries `Source:`.** The repo holds the
relation and not a copy: what the document is, who wrote it, where it lives, and what the team
decides about it. A reading can be claimed and a review can be asked over it, which is how the
charter's *review may happen out of band, and lands back here* reads when the document itself is
out of band too. It says what it was read at, because the source moves under it the way the tree
moves under a residual. The note may carry one `Claimed: <owner> until <date>` line
if it wants to say so, which `make check` holds to what is written there; one that says nothing
is free to be free, and `make claims` lists it as such. A note nobody holds is an invitation
rather than a plan.

**2. `docs/working/$X-plan.md` — the plan.** The order of work, and per step *what it proves*
([llm-memory-plan.md](work/llm-memory-plan.md) is the reference). Each step leaves the tree green.
Writing the plan forces decisions back into the note — `181b179` is that commit — which is a
feature. The plan is also what a second person claims a step of.

**3. Landing** deletes both working files and rewrites the docs to say what *is*:

| | |
|---|---|
| `docs/$X.md` | a subject with an argument ([strata.md](../strata.md), [cycle-throttle.md](../cycle-throttle.md)) |
| `docs/reference/$X.md` | surface: flags, fields |
| folded into an existing doc | most of the time, honestly |
| `docs/roadmap/$X.md` | only what did *not* get built, once reconciled with the roadmap's argument |

Three things make that a change to the repo's shape rather than a `git mv`, and all belong in
the landing commit:

- **the index edits** — `docs/README.md`'s entry and `mkdocs.yml`'s nav, `docs/reference/README.md`'s table and
  command block, root `README.md` if the top-level story moved (`b78459f`);
- **the neighbours** — a doc describing the old behaviour, or calling this gap a looseness, is
  now wrong (`319843c`);
- **the residual** — what the plan named and the landing skipped becomes an issue, and a row in
  [residuals.md](residuals.md) saying what it is missing from (`git issue create`, labelled
  `residual`; the genre is in [README.md](README.md#residuals)). Dropping a plan without its
  residual turns a known gap into an unknown one.

Design proposals go straight to `main` in `docs/working/`: a working note owes coherence to
nothing, so a branch protects nothing. `lifting-flows-design-proposal` on `origin` predates
this and should just land.

`working/` is what is coming, a plan is what is claimed, `docs/` is what is true.

## Pushing to `main`

Three tiers, graded by what a reader needs from you. All go to `main`.

**These are recommendations.** How you organise your own building and review is yours under the
charter. What a commit declares is not, because everybody else reads it — so the recommendation
is about the declaration, and `Breaks:` is the last line to drop.

### `fix` — no trailers, no ceremony

**The default.** Innocuous cleanup, an obvious fix, getting the checks back to green, a
revert. Write a normal commit message and push. You never write `Tier: fix` — silence *is* the
declaration, and it says "anyone reading the diff would agree."

Trailers are recommended for the two tiers below, not for everyone.

### `change` — significant, but the contract holds

New capability, an internal rewrite, better behaviour — where nothing anybody else wrote stops
working. The body is the record: what changed, why, what a reader would notice
(`3acac29` and neighbours are the standard). Recommended: `Tier:` and `Checked:`.

The trap is the change that looks contract-preserving because the types did not move. A rule
that now fires where it did not is a contract change to whoever's charter it is. When in
doubt, next tier.

### `contract` — not backward compatible

Filing off a rough edge will sometimes break things. That is allowed; silence is not.

**The contract is whatever somebody else's checkout depends on:** the charter grammar
([trigger-model.md](../trigger-model.md), the largest surface, because a charter is something
a dogfooder *wrote*); the entity model ([store.md](../reference/store.md),
[change-events.md](../change-events.md)); the `verse` CLI and `scripts/`; the HTTP surface and
wire types (`api/`, `versed/`) and anything dialled in against them; the config layers and
what a `--data` directory holds; and every doc under `docs/` **except those under
[`docs/working/`](README.md) and [`docs/roadmap/`](../roadmap/README.md)**, because we promised
that directory describes what is true and those two are where we promised the opposite. A
working note is retired, contradicted or rewritten as the thinking moves — that is the genre,
not a break — and the roadmap is resynthesized whole. Nobody's checkout depends on either.

Recommended: the `!` marker and all the trailers below. `Breaks:` is the one another owner's
checkout depends on.

### Say what you held yourself to

There is no CI. The only signal a commit carries about its own confidence is the one its
author writes — so write it honestly. "Did not run the tests, was on a plane" beats a green
checkmark nobody produced. Under-declaring is what the `fix` default is for; over-claiming is
the only thing that breaks this.

```
Work: <a claim in owners.tony>                             # what this commit is under
Tier: change | contract                                  # omit entirely for a fix
Checked: make check && make test && ./smoke.sh
Breaks: <surface> — <what a reader must do>              # contract only
```

`Checked:` is free text — `by hand: drove §4`, `smoke only`, `nothing — see body` are all
valid, and the last earns the mechanism its keep. Note load-bearing skips: `make test-long` is
not part of the ritual, but a timeout change that skipped it should say so.

`Work:` names the claim in [owners.tony](owners.tony) this commit is under, and it is the trailer
the charter gives a use: a claim stays alive by being engaged with, and the log is where that
shows. Omit it on a commit no claim carries.

### Declare the contract where the work lands, not on the way there

`Tier: contract`, the `!` marker and `Breaks:` are about the branch's NET effect on main — and
mid-branch that is not yet known. Written per commit they end up describing states that never
leave the branch: one commit breaks what the commit before it introduced, and the reader on main,
who saw neither, is told about a break that never reached them.

So on a branch commit, say what changed in the body and keep `Checked:`, which is an honest
record of what actually ran at that point. Add `Tier:` and `Breaks:` **when the work lands** — at
the merge or the fast-forward — and compute them by asking what somebody *on main* has to do
differently, not what changed since your previous commit.

The tell, which is worth checking before writing one: **if a `Breaks:` line describes something
you introduced earlier in the same branch, it is wrong.** It is a note to your past self, and it
will be obsolete before anybody reads it.

Driven: a credentials branch declared a contract break three times in an afternoon — a field that
was a name, then an address, then a node holding a value — and squashing it to what main would
actually receive left one `Tier: change` and no `Breaks:` at all, because nothing a reader had
ever seen was breaking.

```sh
fmt='%h %ad %s ::%(trailers:key=Work,valueonly)'
git log --date=short --format="$fmt" | grep '::auth'   # what has moved on the auth claim
```

Trailers rather than prose, because the log becomes queryable — which is what makes the tag
message above generatable:

```sh
fmt='%h %s ::%(trailers:key=Breaks,valueonly,separator=%x20)'
git log --format="$fmt" | grep -v '::$'            # every break, ever
git log --format="$fmt" A..B | grep -v '::$'       # the breaks between two points
```

Match on the *parsed* trailer, not `--grep='^Breaks:'`: `--grep` searches the whole message,
so it also matches a body that merely talks about the convention — as this note's own landing
commit does.

**Which means a trailer that does not parse is a declaration nobody made.** Git reads exactly one
place — the LAST paragraph — and takes it only if every line in it is a trailer:

| | |
|---|---|
| body, blank line, `Tier:` … `Co-Authored-By:` | parses, all of them |
| body, blank line, `Tier:` …, blank line, `Co-Authored-By:` | only `Co-Authored-By:` — the rest is now prose |
| body, `Co-Authored-By:` with no blank line | nothing: the paragraph holds prose |
| a wrapped value whose continuation is not indented | nothing, the whole block |

So: **one final paragraph, blank line above it, nothing in it but trailers, and indent anything
that wraps.** `Co-Authored-By:` lives inside that paragraph rather than below it, which is the
one that catches people, because putting it last with a gap looks tidier and silently demotes
everything above.

All four fail silently and leave a message that reads exactly right. So check, on the commit
rather than on the intention — it costs nothing:

```sh
git log -1 --format='%(trailers:key=Tier,valueonly)'    # empty = it did not parse
```

This is written down because it happened: a session's worth of `Tier:` lines and one
`Breaks:` on a `contract` change were all invisible to the query above, and the commits looked
correct in `git show`. A tag cut over that range would have reported no breaks and been
believed.

These describe the *diff*, not the author. Most commits here are co-authored by an agent and
that changes nothing — except that an agent will happily write a confident body about work it
did not run.

## What we are deliberately not doing yet

Each has a trigger that would change the answer; naming it is what stops "not yet" becoming
"never."

- **CI** — *trigger:* a wrong `Checked:` line survives a week, or a `dogfood/*` tag is re-cut.
- **Pull requests, repo-wide** — the charter leaves reviewing to each owner, so use them on
  your own work if you want them. What stays off the table is requiring them of everybody, and
  `main` stays fast-forward only.
- **Versioning and releases** — *trigger:* a consumer outside the team.
- **Protected `main`, required trailers, hooks** — no trigger. Discipline is cheaper than
  policy here, and revert is one command.

## Open questions

- **How does this relate to tony-format?** verse pins `go-tony`, and the charter
  grammar *is* tony — so a bump can break charters with no verse commit declaring it, and a
  defect found driving verse is often a tony defect (filed today on that repo's living issue).
  Does a bump get a `Breaks:` here? Does a `dogfood/<name>` tag pin the go-tony it was driven
  against? Do the two repos share these conventions or merely interoperate? Unanswered, and
  the most likely source of a break nobody sees coming.
- **Does a `--data` directory survive a binary upgrade?** Untried. Until somebody tries, the
  honest advice on advancing a tag is "start fresh" — bad for anyone driving something real.
- **How do you ask someone to read something?** *Answered by [the charter](charter.md),* and it
  went the way the last sentence of this bullet used to guess at: asking for a design review is
  a gate over the doc, whose default the owner of the design work sets. The reading may happen
  out of band — chat, a call, someone's afternoon — but it lands back here as a note beside the
  doc or edits to it, which keeps the repo the home of what applying it produced. What remains
  open is the mechanism rather than the convention: a gate reaches nobody until verse's gates
  can be answered from Slack, which the charter names as the prerequisite.
- **Where does friction go?** Not a defect, but "this took an hour and I'm still unsure" is the
  most valuable thing dogfooding produces and fits none of the buckets above.
- **Do per-drive tags proliferate?** No proposal for retiring a drive or its tags, and
  `git tag -l` is flat.
