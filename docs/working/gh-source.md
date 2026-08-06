# A GitHub source: issues, discussions, pull requests, Actions

Status: proposal, nothing here runs. It argues from code that does — [sources.md](../sources.md)
is the design it obeys, and `source/git.go`, `source/slack` and `source/linear` are the three
precedents it is measured against.

`github:pr:org/repo#42` has been the example entity in this tree since the beginning —
[overview.md](../overview.md), [reference/entities.md](../reference/entities.md), and a case in
`trigger/match_test.go` — and nothing writes it. This note is about filling in that slice, and
about the two things building it would settle that the other three connectors did not.

## Why a fourth, and what it is not

The git source reflects **the code**: refs, heads, commits, content-addressed and cursorless.
A GitHub source reflects **the collaboration around the code**: what somebody proposed, what
was argued about, what CI said. Those are different systems in the vocabulary's sense, so both
can be installed against the same repository and the disjointness check has nothing to say
about it — `git:*` and `github:*` are disjoint on the first segment.

They are also not joined by verse. A PR carries its head sha; a `git:commit:<repo>/<sha>` is
addressed by the same sha; a charter that wants "the commit this PR is proposing" reads both
and joins on the field. That is what [`scripts/lineage.sh`](../../scripts/lineage.sh) already
demonstrates for a commit, a PR and a release: **no fourth id is minted**, a correlation is a
derived entity whose identity is (rule, subject). Nothing in this source is allowed to invent
a cross-system key.

That script is also this connector's first real consumer, and its readiest acceptance test.
It seeds `lingh:pr:42 {stage: merged, head: a1b2c3, target: main}` by hand — deliberately fake
systems, because nothing writes the real ones — and correlates on exactly the three fields the
table below carries under different names (`merged`, `headSha`, `base`). So the source is
finished when `lineage.sh` runs against a real repository with its `lingh` seeding deleted, and
the first thing that exercise will argue about is vocabulary rather than mechanism.

Two things make this connector worth building rather than merely possible:

- **It is what [`verse-dev`](verse-dev.md) actually needs.** Step 3 of that drive is commits
  landing as facts, and [contributing.md](contributing.md) is a process with pull requests,
  contract notices and CI in it. A drive against the repo hits this slice on day one.
- **It is the first connector where the four things reflected are not four of the same thing.**
  git has one shape (a ref), Slack has two (a channel, a message), Linear has three that behave
  identically. GitHub has four surfaces with genuinely different lifetimes, and the covers
  grammar has to carry all four. That is the interesting part, and it is [below](#the-four-surfaces-are-not-four-of-the-same-thing).

## The vocabulary

Five kinds, all repo-scoped, spelled the way GitHub spells them:

| entity | payload |
|---|---|
| `github:repo:<org>/<name>` | `repo, org, name, private, archived, fork, defaultBranch`, and `description, topics, homepage` |
| `github:issue:<org>/<name>#<n>` | `repo, number, title, state, author, url, createdAt, updatedAt`, and `stateReason, assignees, labels, milestone, comments, closedAt, body` |
| `github:pr:<org>/<name>#<n>` | `repo, number, title, state, draft, author, base, head, headSha, url, createdAt, updatedAt`, and `merged, mergedAt, mergeable, reviewDecision, checks, labels, assignees, comments, closedAt, body` |
| `github:discussion:<org>/<name>#<n>` | `repo, number, title, category, state, answered, author, url, createdAt, updatedAt`, and `comments, body` |
| `github:run:<org>/<name>/<id>` | `repo, id, workflow, event, branch, headSha, status, conclusion, url, startedAt, updatedAt`, and `attempt, actor, runNumber` |

`repo` is `<org>/<name>` and is on every payload. It is not decoration: a cover is a payload
predicate and can only speak of fields the payload has, so **a source must carry in its payloads
every field its covers are spelled with**. git carries `repo` and Linear carries `team` for the
same reason; here it has teeth, because [targeted passes](#the-delivery-half) need `number` in
the payload too and the id alone would have looked sufficient.

Three notes on the spelling.

**A PR is not an issue, and GitHub thinks it is.** `GET /repos/{o}/{r}/issues` returns pull
requests as well, distinguished only by a `pull_request` key on the item. Reflecting them as
`github:issue:*` would collide with nothing — the number space is shared, so `#42` would be the
same entity from two reads — and it would make "every open issue" a charter's question that
quietly includes review traffic. So the issue read filters them out and the PR read is separate.
The number space stays shared and the kinds keep them apart.

**Discussions have their own number space**, so `github:issue:org/repo#1` and
`github:discussion:org/repo#1` are different things with the same number. That is fine — a ref
is (system, kind, id) — and it is worth saying out loud because `#1` alone reads like an
identifier and is not one.

**Match `state` and `conclusion`, never a display string.** This is Linear's `stateType`
lesson, and GitHub gives it for free: `state` is `open`/`closed`, a run's `conclusion` is
`success`/`failure`/`cancelled`/`timed_out`/`skipped`/`neutral`/`action_required`. Labels and
milestone names are what somebody renamed on a Tuesday.

## Ownership: five claims per repo, and the reason it is not one

```tony
owns:
- github:repo:signadot/verse
- github:issue:signadot/verse#*
- github:pr:signadot/verse#*
- github:discussion:signadot/verse#*
- github:run:signadot/verse/*
```

The `#` is load-bearing in exactly the way Linear's hyphen is. A single glob
`github:*:signadot/verse*` covers all five and also claims `signadot/verse2#1` and
`signadot/verse-extras/9` — not a false alarm but a real overclaim, and one that would make two
perfectly disjoint repositories refuse to install alongside each other. With the separator,
`signadot/verse#*` and `signadot/verse2#*` are two id prefixes neither of which prefixes the
other, which is the disjointness rule verbatim.

**The kinds are named rather than globbed, and that is the load-bearing choice here.** A
`github:*:signadot/verse#*` claim would be shorter and would work today. It would also make one
particular future install impossible, and it is the future this design is most likely to want:
a source that reflects **people**, claiming `github:user:*` and nothing else. Against a
kind-globbed claim that is refused — a `*` kind is disjoint from nothing, and the user claim's
id is a bare `*`, disjoint from nothing either — while against the five above it is disjoint on
the kind segment and installs clean. Naming the kinds costs four lines and keeps a door open
that a glob would weld shut. See [what is not reflected](#what-is-not-reflected-and-what-that-is-evidence-of).

An **org-wide** source is the same five with `<org>/*` in place of `<org>/<name>`. It is
correctly refused beside a per-repo source in the same org, since one id prefix prefixes the
other, and that refusal is right: they would each tombstone what the other reflected.

## The four surfaces are not four of the same thing

This is the design decision the connector exists to make, and the two precedents disagree:

- **git keeps history.** Commits are carried and covered by nothing, so one that falls out of
  the tip window is simply not mentioned and stands forever. A repository's history is finite
  and immutable.
- **Slack keeps a window.** Messages are covered, so one that falls out of `-depth` is
  tombstoned. A channel is an unbounded event stream, and keeping all of it is the warehouse a
  source is not supposed to be.

GitHub's four surfaces split across that line, and not where the API boundaries are:

| | shape | reflected as |
|---|---|---|
| issues, PRs, discussions | an unbounded set of which the **live** part is bounded and the **settled** part only grows | live set complete; settled set a window |
| Actions runs | an unbounded stream where nothing settles into being interesting | a window |
| the repo itself | one entity | one entity |

So the rule, and it is a third position rather than a compromise:

> **Cover the live set completely. Cover the settled set as a window.**

```tony
covers:
- {system: github, kind: issue, value: {repo: signadot/verse, state: open}}
- {system: github, kind: issue, value: {repo: signadot/verse, state: closed}}
```

The first cover says the answer holds **every** open issue in this repo, so one that has gone
absent is gone. The second says the answer holds the closed issues it read — the most recent
`-settled` of them — so a closed issue that has aged out is tombstoned, exactly as a Slack
message is. An issue that closes appears in the settled half with `state: closed` (an update, a
rule about closure fires), ages, and leaves.

**Both halves must be covered or neither works.** Covering only `{repo: X}` and answering with
the live set plus a recent tail would tombstone every issue closed before the tail — the whole
back catalogue, on the first pass. Covering only `state: open` and never the settled set leaves
a husk per issue ever closed while this source watched, which is the warehouse arriving slowly
instead of at once. Two covers, one for a complete set and one for a window, is what says both
things at the same time.

### The constraint that forces this shape

The obvious alternative — cover what was **read**, which is "everything updated since T" — is
not expressible, and the reason is worth writing down because it will be reached for again. A
cover is an `entity.Pattern`, whose predicate is a tony match, and tony's match tags are
`or, and, not, type, glob, field, tag, subtree, has-path, at, all, let, if, pass, raw, quote,
unquote`. **There is no ordering operator.** `{state: open}` is sayable; `{updatedAt: after T}`
is not. (`updatedAt: !glob "2026-08-*"` says "updated in August", which is a real answer to the
wrong question — a window that snaps to calendar boundaries is not a window.)

So the split has to be on a **state** the payload carries, and GitHub obliges: `open`/`closed`
is the axis, on all three of the set-shaped kinds. Discussions close too, since GitHub added
closing them; an unanswered question in an open discussion is the live thing a charter wants.

### The read that makes it safe

Two reads per repo per pass, and the second is what makes the first honest:

1. **The live set, to exhaustion.** `state=open`, paged. Bounded by `-max-open`, and exceeding
   it is an **error, not a truncation** — Linear's rule, for Linear's reason: a partial list
   under a complete cover tombstones what it merely failed to read. This is the one place a
   GitHub source can destroy a slice, so the bound refuses rather than approximates.
2. **Everything updated since the high-water mark.** `state=all&sort=updated&direction=asc&
   since=<mark>`, which is what carries an issue that closed between two passes into the answer
   with its new state, instead of letting it go absent from the live half and be tombstoned
   without ever having been seen closed.

The mark is a cursor, and the design says a source has no cursor. It is allowed here by a rule
that is worth stating generally:

> **A source may keep state whose loss costs work, never state whose loss costs correctness.**

Losing this mark means reading `since=` nothing, which is the exhaustive read, which is the
correct answer. That is categorically different from git's absent since-token, whose loss would
be a missed-event window. The same rule is what licenses [the ETag cache](#the-budget-and-the-one-thing-github-gives-that-slack-and-linear-do-not) below, and it is the test to apply to
anything else a connector wants to remember.

**The raggedness this leaves**, named rather than hidden, the way Slack's aged-out thread parent
is: a source down longer than its settled window comes back to find an issue that closed while
it was away in neither half — absent from the live set, older than the window — and tombstones
it without ever reflecting `state: closed`. A charter waiting on closure sees a delete instead
of an update. Bounding it is what `-settled` is for, and the honest advice is that the window
should be comfortably longer than the longest outage anybody expects to shrug at.

### Actions gets the window and nothing else

Runs are Slack messages with better structured payloads: the last `-runs` per repo, covered by
`{repo: X}`, tombstoned as they slide. There is no live/settled split because a queued run
becomes a completed run in minutes and a completed run is never interesting again — and because
runs are ordered by creation, a run in flight is always inside the window.

**Actions is also where level-triggering shows.** A run goes `queued → in_progress → completed`
inside a minute, and a pass every minute will frequently collapse all three into one write. A
charter that matches `status: in_progress` may never fire for a run that fired every trigger it
was supposed to. This is not a defect to fix: verse promises the **last observed state**, not
every state, and the trigger model already says the corresponding thing about creates and
updates ("express 'only the first time' as a state latch, not an edge"). Written down here
because a CI charter is the first place somebody will expect otherwise.

Webhooks narrow the gap and do not close it — a delivery storm is coalesced by the throttle,
which is the trade `source.Throttle` documents.

### What is on a PR, not beside it

**Check state lives on the PR as a field**, not as `github:check:*` entities. "Is this PR green"
is the question an SDLC flow asks, and it is a property of the proposal rather than of the
seventeen check runs that produced it. Reflecting each check run would multiply the busiest
slice in the verse by an order of magnitude to answer a question nobody asks in that form. A
charter that wants the failing job follows `url` into GitHub — the index-not-warehouse rule,
applied where it bites hardest.

`github:run:*` exists anyway because a workflow run is a thing in its own right (a scheduled
job, a release pipeline) with no PR to hang off.

## The delivery half

A GitHub webhook, received by the source, verified by the source, turned into which refs went
stale. `X-Hub-Signature-256` is HMAC-SHA256 of the raw body, hex, `sha256=`-prefixed;
`X-GitHub-Event` names the event. Same object as the Slack events endpoint, the Linear webhook
and the four-line git hook, and verse parses none of them.

| delivery | hint |
|---|---|
| `issues`, `issue_comment` | `github:issue:<repo>#<n>` |
| `pull_request`, `pull_request_review`, `pull_request_review_comment` | `github:pr:<repo>#<n>` |
| `discussion`, `discussion_comment` | `github:discussion:<repo>#<n>` |
| `workflow_run`, `check_suite` | `github:repo:<repo>` (the run window) |
| `repository`, `push` | `github:repo:<repo>` |
| anything else, or unplaceable | no ref — a full resync |

**This is the first source where a hint names the thing rather than its container**, and the
reason is arithmetic. git's targeted pass is a whole-repo `for-each-ref` because git is
content-addressed and a finer target costs the same; Linear's is a whole team because that is
what one GraphQL query reads. GitHub will answer `GET /repos/{o}/{r}/issues/42` in one request,
so a targeted pass over a busy repository is genuinely one call instead of forty.

Which means a targeted pass answers for exactly one entity, and its cover has to say so:

```tony
covers:
- {system: github, kind: issue, value: {repo: signadot/verse, number: 42}}
```

An issue deleted or transferred away is then tombstoned by the hint that reported it, and its
neighbours are untouched. This is the mechanism `entity.Pattern`'s missing id dimension is
usually a nuisance about, working properly: the pattern cannot name an id, so it names the
`number` field instead, which is why that field is in the payload.

**Where no webhook is available** — no admin on the repository, or a fork somebody else owns —
`GET /repos/{o}/{r}/events` polled at the `X-Poll-Interval` GitHub asks for is a hint stream
with about five minutes of delay. It stays inside the source, which is where the decision "what
went stale" belongs; verse is not told it happened.

## The budget, and the one thing GitHub gives that Slack and Linear do not

The primary limits: **5,000 requests/hour** for a personal access token; the same as a floor for
a GitHub App installation, scaling with org size to a cap of **12,500** (15,000 on Enterprise
Cloud); a separate GraphQL budget; and secondary limits of 100 concurrent requests and 900
REST points/minute.

Against a one-minute resync, four surfaces per repo is 240 requests/hour/repo, so a naive
source on a PAT runs out at about twenty repositories. That is the arithmetic that makes
`-min-interval` matter — and GitHub, alone among the three connectors, offers something better
than a clock:

> "Making a conditional request does not count against your primary rate limit if a `304`
> response is returned and the request was made while correctly authorized with an
> `Authorization` header." — [GitHub REST best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api)

So the source keeps an **ETag per endpoint**, sends `If-None-Match`, and a 304 means the listing
is byte-identical to the one it already holds. A quiet repository then costs four free requests
a minute, and `source.Throttle`'s clock — which guesses that nothing changed — is replaced by a
fact. It is git's content-addressing arriving by another route: the same "compare what you have
against what the system says it has", with GitHub doing the comparison.

Three things it must not become:

- **Not a cache of failures.** Errors pass through and leave the cache alone. A cached failure
  is `every` minutes of a source claiming a repository is empty, which is the law the whole
  seam turns on.
- **Not correctness-bearing.** Losing the ETags costs one full read, per the rule above. A
  source restarting into a cold cache is slow for one pass and right on all of them.
- **Not a substitute for the resync.** A 304 says the *listing* is unchanged; the settled window
  slides with the clock regardless, and the periodic full pass is still the net.

`source.Throttle` stays as the backstop for the busy case, where a delivery storm would
otherwise spend an hour's budget in a minute.

**The credential is `GITHUB_TOKEN` from the environment and never a flag**, like the other two.
A fine-grained PAT with read access to issues, pull requests, discussions and actions is what a
dogfood deployment wants — it needs no app registration and carries its owner's visibility. A
GitHub App is what an org-scale install needs, for the higher ceiling and for not being tied to
a person, and it costs real machinery: a JWT signed with a private key, an installation token
minted and refreshed hourly. Deferred, with the trigger named: **the first install that spans
more repositories than a PAT's budget covers, or the first time a person leaving would break a
deployment.**

## What is not reflected, and what that is evidence of

Every connector so far has produced one of these, and this one produces two — which matters,
because the [residual](sources-residual.md) is currently arguing from a single data point about
whether ownership-as-an-address holds.

**Projects (v2) are not reflected**, and it is Linear's finding a second time, in a system that
shares no code with it. A GitHub project belongs to an org or a user and spans repositories, so
no repository can claim it as an address, and two per-repo sources over two repos in one project
would each tombstone what the other wrote. An issue or PR carries its project's name as a field.
That the same shape arrived twice, independently, is the evidence the residual asked for:
**a many-to-many relation has no owner to spell**, and it is not a quirk of Linear's data model.
[shared-ownership.md](shared-ownership.md) proposes the way through, and it turns out to be
small: a claim that names a path inside the payload rather than only a ref, which makes two
sources of one project single writers of two slots rather than two writers of one entity.

**People are not reflected, and this one is new.** `github:user:octocat` is a global fact: it
belongs to GitHub, not to any repository, and two per-repo sources would both write it. Slack's
users looked the same and were not — a bot token is issued for one workspace, so
`slack:user:<ws>/<U…>` has a positional owner and one claim covers it. GitHub's have no such
prefix that is not a lie.

The shape is **not** many-to-many, so it is a distinct limit and worth naming as one: the entity
has exactly one owner, and that owner is *the system*, which no per-repo source is
([shared-ownership.md](shared-ownership.md) treats it as the third case of one problem). The way
out needs nothing new — a single people-source claiming `github:user:*`, installed once beside
any number of repo sources — and it is available **only because the repo claims name their kinds**
rather than globbing them. That is the whole argument for the five-line `owns` above, and it is
the cheapest kind of future-proofing: a decision that costs four lines now and cannot be taken
later.

Until then, an author is a `login` string on the payload, which is what a charter matches on
anyway.

**Comments are not entities**, and this one is honestly unsettled rather than decided — see the
open questions. The first cut carries a count and reflects nothing of the bodies.

**Releases and tags** are absent from the table above because the ask was the other four, not
because they are hard: `github:release:<org>/<name>/<tag>` is repo-owned, spells its owner
positionally, and is a sixth claim. `scripts/lineage.sh` correlates a release already, so
something will want it.

## What it would cost, at the scale that has been measured

The [residual](sources-residual.md) measured verse converging at ~290 entities/second against a
local logd, with a pass that Lists the whole system slice whether or not anything changed. So:

| repository | entities in the live set | a pass |
|---|---|---|
| this one, today | tens | nothing |
| a busy internal repo | ~500 | ~2s |
| kubernetes/kubernetes | ~2,500 open issues + ~1,500 open PRs | ~14s, before the List |

The last row is the same order as the 3,377-entity git pass that never completed on go-tony
v0.0.113, and it is inside a one-minute resync only barely. Two conclusions, both about design
rather than tuning:

- **The bounds are not optional and must refuse rather than truncate.** `-max-open` is the
  only thing standing between a large repository and a partial list under a complete cover.
- **This connector is what makes the residual's parked lever urgent.** "A pass that changes
  nothing still Lists everything" is a footnote against ten git refs and is the dominant cost
  against four thousand issues. A `current()` that could ask the store for a slice's revision
  rather than its contents is worth more here than anywhere else — and note the symmetry: the
  ETag makes the *external* read free for a quiet repository, and nothing yet makes the
  *internal* one free.

## If it gets built

Where it would land, so a plan can be short: `source/github/` (the source, the API client, the
webhook), `cmd/sourced/github.go` (a fourth subcommand), `deploy/github-source` (a fourth
manifest, off by default), and the table rows in
[reference/sourced.md](../reference/sourced.md). Nothing in `source/`, `versed/` or the CLI
changes — which is the seam's claim, and building a connector that needs no verse-side change
is what would demonstrate it a third time.

```
sourced github -repo   [<org>/<name>]     a repository to reflect (repeatable, comma-separated)
               -org    <org>              …or every repository in an org the token can see
               -settled <n>               closed issues/PRs/discussions kept per repo (default 50)
               -runs    <n>               workflow runs kept per repo (default 50)
               -max-open <n>              refuse a repo with more open items than this (default 2000)
               -base   <url>              GitHub Enterprise Server API root
```

`GITHUB_TOKEN` and `GITHUB_WEBHOOK_SECRET` from the environment; `POST /github/webhook` is the
delivery route.

The order that would prove things fastest is the one the other connectors used — the read and
its covers first, against a fake API, since that is where every wrong answer tombstones
something; the webhook second; the ETag cache last, because it is an optimisation and the pass
must be correct without it.

## Open questions

- **Do comments want to be entities?** The first cut says no, on the Slack argument: a comment
  stream is an unbounded event stream and would want a window, and the SDLC facts are on the
  issue. But [`verse-dev` step 2](verse-dev.md#2-a-contract-notice-as-a-motion) is a contract
  notice that is *visible to all, objectable by any* — and an objection, in GitHub's world, is
  a comment. If that drive concludes a notice is a gate, the objection has to be readable, and
  the answer is either comment entities or a `/objections`-shaped predicate on the issue. This
  is the question most likely to be settled by somebody using it rather than by argument.
- **Is a repo rename a `--keep` that nobody is asked about?** Removal has a fork — `--forget` or
  `--keep` — because moving a repository between hosts is a bookkeeping change that would
  otherwise destroy and recreate live facts. A rename inside GitHub is the same event with no
  removal in it: the ids all change, and a running source would tombstone `old/repo#*` and write
  `new/repo#*`, firing every trigger in the verse twice. GitHub redirects the old name, so the
  source would not even notice. The cheap defence is to resolve the repository's numeric id at
  startup and **fail the pass** when it stops matching the configured name, making the operator
  re-install deliberately. Unconfirmed that this is the best answer, but a source that follows
  a rename silently is definitely the wrong one.
- **Does a `state`-covered set stay honest under GitHub's other transitions?** An issue can be
  *transferred* to another repository and *converted* to a discussion. Both look like a delete
  from this repo's live set, which is arguably right; whether a charter can tell them apart from
  a deletion, and whether it should, is untested.
- **Is one source per repo the right unit at all?** `-org` reflects a whole org from one
  credential and one budget, which is what a real deployment probably wants; per-repo is what
  makes the ownership claims legible and lets two teams install independently. The disjointness
  check forbids mixing them in one org, which is correct and may be inconvenient in exactly the
  case somebody hits first.
- **Does the settled window's age-out fire rules it should not?** A tombstone is a tombstone: a
  charter matching `op: delete` on `github:issue:*` cannot distinguish "somebody deleted this
  issue" from "this issue closed six weeks ago and left the window". Slack has the same
  ambiguity and nobody has written a rule against it yet.
- **Does an ETag cache survive a source that answers for several repositories?** The cache is
  per-endpoint and the throttle is per-ref; a pass that is 304 on three repos and live on the
  fourth answers a mixture, which is fine for covers and has not been thought through for
  `lastReflect`.
