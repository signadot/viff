# A GitHub source: what is left

Status: residual of a landed connector. `sourced github` runs — a repository, its issues, pull
requests, discussions, Actions runs and comments — and the surface is
[reference/sourced.md §github](../reference/sourced.md#github), with the parts of the argument
that are about sources in general folded into [sources.md](../sources.md). The note and the plan
went away with the landing, as [contributing.md](contributing.md) says they should; this is what
they named and the landing did not build.

Nothing here is a design owed to anything else here.

## What the landing decided differently, and why it is worth knowing

Three of the note's positions did not survive contact, and in each case the thing that moved it
was a measurement or a use, not an argument.

**Comments are entities.** The note deferred them on the Slack argument — a comment stream is
unbounded, and the SDLC facts are on the issue — and called it "the deferral most likely to be
wrong". It was. Two repository-wide listings answer for every comment in a repository
(conversation comments, which an issue has and which a pull request's discussion *is*, and
review comments on a diff), so the cost is two conditional requests a pass rather than one per
item, which is what made the Slack argument stop applying. They are a window, like Slack's
messages, and they hang off the item they are on so that "the comments on #7" is a prefix of an
address rather than a predicate.

**Discussions are off by default, and that is the budget lever.** The note deferred GitHub App
auth with the trigger "the first install that spans more repositories than a PAT's budget
covers", and the measurement moved the decision somewhere cheaper. Every REST read is
conditional and a 304 does not count against the primary limit, so a warm pass over a quiet
repository issues nine requests and is charged for **two** — both of them the discussions
queries, which are GraphQL and carry no ETag. A flag that skips them takes a quiet repository to
nothing chargeable.

| per repository per pass | chargeable | issued |
|---|---|---|
| cold (first pass, or after a restart) | 9 | 11 |
| warm (nothing changed) | **2** | 9 |
| warm, `-discussions=false` | **0** | 7 |

So App auth is not next, and the trigger for it is sharper than the note's: **a deployment that
wants discussions on more than about forty repositories**, or the first time a person leaving
would break one. Everything else one token reflects is effectively unbounded.

**The ETag cache moved to the front.** The note put it last, as an optimisation the pass must be
correct without. It was built with the client instead, because at four surfaces and a one-minute
resync a PAT covers about a dozen repositories with no caching at all — so conditional requests
are what make more than a handful possible, and they are cheaper to build in than to retrofit
around a client that never had a cache. The note's ordering rule still holds for its own reason:
losing the cache costs one full read and never correctness, which is the same rule that licenses
the `since=` high-water mark.

## Not built, each with the trigger that would revive it

**`-org`: one credential over a whole org.** The note offered `-repo` and `-org`; only `-repo`
landed. A per-repo source is what makes the ownership claims legible and lets two teams install
independently, and it is what a dogfood deployment needs. The disjointness check correctly
refuses an org-wide source beside a per-repo one in the same org — one id prefix prefixes the
other, and they would each tombstone what the other reflected — so this is not an addition, it
is a second unit of deployment with a migration in it. **Trigger:** an install where enumerating
the repositories is the operational cost.

**`github:project:*`, which now has its mechanism.** A GitHub project is org- or user-owned and
spans repositories, so no repo source can claim it as an address. That was one of the three
cases that argued for [shared ownership](shared-ownership.md), and phase 1 of that work landed —
a claim may name a path inside a payload, and converge writes, compares and tombstones there.
So a project reflected by slot, one per repository, is a **second pass over this connector**
rather than blocked work; Linear's projects are the worked precedent
([reference/sourced.md §linear](../reference/sourced.md#linear)). What it would cost is the same
thing it costs Linear: a project with no top, because every field GitHub has about one is global.

**`github:user:*`, which needs a source rather than a mechanism.** A user belongs to GitHub, not
to any repository, so every per-repo source would write it — one owner, and that owner is the
system. The way out needs nothing new: a separate source claiming that kind alone, installed
beside any number of repo sources. It is available **only because the repo claims name their
kinds** rather than globbing them, which is what those five lines of `owns` bought. Until then an
author is a `login` string on the payload, which is what a charter matches on anyway.

**Releases and tags.** `github:release:<org>/<name>/<tag>` is repo-owned and spells its owner
positionally, so it is a sixth claim and nothing else. It is absent because the ask was the other
four. **Trigger:** [`scripts/lineage.sh`](../../scripts/lineage.sh) already correlates a release,
so something will want it.

**Check-run entities.** Deliberately not, and this one is a decision rather than a deferral: "is
this PR green" is a property of the proposal, and reflecting seventeen check runs per pull
request would multiply the busiest slice in the verse to answer a question nobody asks in that
form. CI state is a field on the pull request, derived from the runs already read by head sha,
so it costs no request. A charter that wants the failing job follows `url` into GitHub.

**The events poller.** Where no webhook can be installed — no admin on the repository, a fork
somebody else owns — `GET /repos/{o}/{r}/events` at the `X-Poll-Interval` GitHub asks for is a
hint stream with about five minutes of delay, and it would stay inside the source. It buys
latency and nothing else: the resync net already converges a repository with no hook. It costs a
background loop, a cursor over deliveries already seen, and a request per repository per poll,
against a budget this connector is short of. **Trigger:** a repository somebody drives where they
cannot install a hook and a minute is too much.

**A defence against a repository rename.** Removal has a fork (`--forget` or `--keep`) because
moving a repository between hosts should not destroy and recreate live facts. A rename inside
GitHub is the same event with no removal in it: every id changes, so a running source tombstones
`old/repo#*` and writes `new/repo#*`, firing every trigger over the slice twice — and GitHub
redirects the old name, so the source does not notice. The cheap defence is to resolve the
repository's numeric id at startup and **fail the pass** when it stops matching the configured
name, making the operator re-install deliberately. Unbuilt, and unconfirmed as the best answer;
a source that follows a rename silently is definitely the wrong one.

## Raggedness inside what runs

Each of these is a corner of something that works, named here rather than hidden, in the way
Slack's aged-out thread parent is.

**A source down longer than its settled window loses a state transition.** An issue that closed
while the source was away is in neither half when it comes back — absent from the live set, older
than the window — so it is tombstoned without ever having been reflected `state: closed`, and a
charter waiting on closure sees a delete instead of an update. `-settled` is the lever, and the
honest advice is that the window should be comfortably longer than the longest outage anybody
expects to shrug at.

**A long run can fall out of the runs window while it is still running.** The note claimed runs
are ordered by creation so a run in flight is always inside the window. That is wrong: more than
`-runs` runs can be created while one long job goes, and that one is then tombstoned, so a
charter waiting on its conclusion never sees one. It cannot be designed away with the API on
offer — the runs listing has no ordering of its own and its `status` filter takes a single value,
so there is no read that returns "everything not finished", and an enumeration of statuses would
leave whatever GitHub adds next uncovered and standing forever. The lever is a `-runs`
comfortably larger than what a repository creates while its longest job runs.

**A tombstone from the window cannot be told from a deletion.** A charter matching `op: delete`
on `github:issue:*` cannot distinguish "somebody deleted this issue" from "this closed six weeks
ago and left the window". Slack has the same ambiguity and nobody has written a rule against it
yet.

**A run's transitions collapse.** `queued → in_progress → completed` happens inside a minute, so
a pass will frequently write all three as one, and a rule matching `status: in_progress` may
never fire for a run that did everything it was supposed to. This is not a defect: verse promises
the last observed state, not every state, and the trigger model already says the corresponding
thing about creates and updates. It is written down where a charter author meets it
([reference/sourced.md §github](../reference/sourced.md#github)) because a CI charter is the
first place somebody expects otherwise. Match `conclusion`.

**`lastReflect` under a mixed pass is unexamined.** The ETag cache is per-endpoint and the
throttle is per-ref, so a pass that is 304 on three repositories and live on the fourth answers a
mixture. That is fine for covers, which say what the answer is complete for either way; what it
does to the "wrote something" bookkeeping has not been thought through.

## What the drive turned up, which is the part worth keeping

[`scripts/lineage.sh`](../../scripts/lineage.sh) was this connector's acceptance test, and it has
been run for real: pointed at `github:pr:*` and `git:commit:*` against this repository, with two
live connectors and a real pull request. It passes.

It did not stay that way, and the reason is worth keeping: a demonstration that needs a token,
two deployed connectors and a repository that happens to contain a merged pull request is not a
demonstration, it is an integration test with a narrative. The script seeds its own fixtures
again — but under the shipped ids and field names, `git:commit:<repo>/<sha>` and
`github:pr:<org>/<repo>#<n>` with `merged`/`headSha`/`base`. Running it the other way is
git-issue `wynxghrxh12ksyv2esn0`, which records that it needs both sources installed first.

The prediction was that the exercise would argue about **vocabulary rather than mechanism**. That
was wrong in both halves. The vocabulary cost nothing — the script moved to GitHub's own spellings
and the invented ones had no constituency. What real ids surfaced was three things about
mechanism that inventing both sides had hidden, and none of them is a defect:

- **The commit join is a list read, not an identity one.** A git source ids a commit
  `<repo>/<sha>`, because a sha is not a sound cross-repo key, while a pull request carries a bare
  `headSha`. There is no id to name, and `!get-path` refuses a predicate, so the join is
  `!list-paths` over `{sha: …}` returning a one-element list. Two fake systems hid this by
  agreeing on a spelling no two real systems would.
- **A rule does not backfill.** The pull requests were reflected before the charter existed, and a
  trigger is level-triggered on writes, so nothing fired. There is no replay verb, by position
  rather than omission. What works is the source's own idempotence: drop what it wrote and re-add
  it, and the rewrite is a real write the rule sees. Seeding hides this completely, because seeding
  always happens after the rules are installed.
- **A derived address is not the subject's id concatenated.** `#` and `/` are sanitised to `_` and
  a short hash is appended so the sanitising cannot collide two subjects, so nothing finds a trace
  by string-matching a subject into an address. It asks `verse.meta.derivedFrom` instead. The fake
  ids were punctuation-free and never showed it.

All three are things a charter author would otherwise meet for the first time in their own
charter, which is the argument for driving a connector against something real rather than a
fixture.

## The cost that is verse's, not GitHub's

The ETag makes the *external* read free for a quiet repository, and nothing makes the *internal*
one free: a pass that changes nothing still Lists the whole system slice to answer "what changed".
That is a footnote against ten git refs and the dominant cost against four thousand issues, which
makes this connector the reason
[sources-residual.md](sources-residual.md#gaps-inside-what-runs) parks that lever rather than
drops it. A `current()` that could ask the store for a slice's revision rather than its contents
is worth more here than anywhere else.

The shape of the bill, at the rate the residual measured (~290 entities/second converged against a
local logd):

| repository | entities in the live set | a pass |
|---|---|---|
| this one | tens | nothing |
| a busy internal repo | ~500 | ~2s |
| kubernetes/kubernetes | ~2,500 open issues + ~1,500 open PRs | ~14s, before the List |

The last row is the same order as the 3,377-entity git pass that never completed on go-tony
v0.0.113, and it is inside a one-minute resync only barely. Which is also why `-max-open` refuses
rather than truncates: a partial list under a complete cover tombstones what it merely failed to
read, and that is the one mistake this connector can make that destroys a slice.

## Still open

- **When does a charter need to glob a system?** Not yet, and by design: a separated slice
  (`-system github-internal`) is opt-in, so the rules that name one are written by whoever
  separated it. It becomes a thing the first time somebody wants one rule over several GitHub
  deployments — an org-wide policy charter across slices split for operational reasons, which is
  the shape a big install grows into. It is not a match-grammar change: `system` names the subtree
  a watch is established on (`WatchSubtree(system, kind)`), so globbing it means a rule watching
  several subtrees. Cheap to want, not cheap to build.
- **Does a `state`-covered set stay honest under GitHub's other transitions?** An issue can be
  *transferred* to another repository and *converted* to a discussion. Both look like a delete from
  this repository's live set, which is arguably right; whether a charter can tell them apart from a
  deletion, and whether it should, is untested.
- **Is one source per repository the right unit?** See `-org` above. The disjointness check forbids
  mixing the two in one org, which is correct and may be inconvenient in exactly the case somebody
  hits first.
- **Is the settled window the right shape at all?** It was the note's central bet, and the thing
  that would falsify it is a real charter that cares about closed issues older than the window —
  at which point the window becomes a floor somebody raises, and the two-cover split is worth
  re-opening rather than tuning.
