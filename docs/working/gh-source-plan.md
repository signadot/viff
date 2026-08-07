# A GitHub source: implementation plan

Status: plan, nothing built. The design is [gh-source.md](gh-source.md); this is how it lands.
Read that first — this file carries only the order of work and what each step must prove.

The shape being built: a fourth `sourced` subcommand reflecting one system's four surfaces —
issues, pull requests, discussions and Actions runs — into `github:*`, with the set-shaped kinds
covered twice (the live set complete, the settled set a window) and the streams covered once.

**The first cut needs none of the slot machinery.** `repo`, `issue`, `pr`, `discussion` and `run`
are repo-owned and spell their owner positionally, so every cover here is a whole-entity cover
and every claim is an address. `github:project:*` is the one kind that needs a slot and
`github:user:*` needs a source of its own; both are [deferred](#what-this-does-not-do). The
shared-ownership work was argued *from* GitHub and got used by Linear — that is not a
prerequisite relationship, and treating it as one would hold this behind work it does not need.

## Before anything: the note was stale in one place — done

`gh-source.md` spelled `owns` as a list of ref globs, which stopped parsing when a claim became a
document. Fixed in place, so nobody builds from the old spelling.

## Two lessons from the last six commits that this connector will meet

Both were learned by a test failing rather than by argument, and both apply here before any code
is written.

**A cover must select exactly the set a tombstone can be right about.** Linear's project cover
began with no predicate on the reasoning that its slot was the narrowing; it was not, and the
pass wrote a tombstone into every project in the workspace. The covers in the design note carry
predicates (`{repo: X, state: open}`) and are right, but the same trap has a sharper form here:
**a targeted pass must not answer with the live cover.** A hint about issue #42 that returned
`{repo: X, state: open}` as its cover, carrying one entity, tombstones every other open issue in
the repository. That is the one mistake in this connector that destroys a slice, and step 1's
first test is about it.

**Anything that differs between two reads of unchanged data is a rewrite storm.** converge
compares the answer against the store, so a payload carrying an unsorted list, or anything
derived from the time of the read, differs on every pass: every entity is rewritten every
minute, every trigger over the slice fires, and `lastReflect` is stamped forever because `Wrote`
counts writes issued. Linear sorts an issue's labels for exactly this reason. GitHub payloads
carry more lists than Linear's — `assignees`, `labels`, `topics`, a PR's `checks` — and the rule
is per-payload, not per-list: **every list sorted, nothing clock-derived, and a test that runs
two passes over one fixture and asserts the second wrote nothing.** That test belongs in step 1
and gets a new case per kind after it.

## Order of work

Six steps. Each leaves the tree green; 1–3 are a package with a fake and no deployment, 4–5 make
it deployable, 6 is the drive. Nothing in `source/`, `versed/` or the CLI changes at any point —
that is the seam's claim, and this is the third connector that gets to test it.

### 1. The repo, and the two kinds that share a number space

`source/github/` — the package, its API client, and `Reflect` over the repository, its issues
and its pull requests.

**Three kinds, not two.** `github:repo:<org>/<name>` moved here from step 2 because it is what
makes a github-only deployment useful standing alone: it carries `defaultBranch` and its head
sha, which for a repository nobody watches at ref level is the whole of the git-shaped fact
anyone wants, and it costs one un-paginated call plus one for the sha. It is also cheap in every
other sense — one entity, a whole-entity cover, no window, no cursor, no paging — and it is the
natural byproduct of the credential check this step needs anyway. Its `cloneUrl` is the field a
charter joins to a git source on; see the note.

- **`-system`, defaulting to `github`.** The connector's `System()` is a flag rather than a
  constant, so a deployer who wants a slice of their own has one and owes nobody a disjointness
  proof, while the default keeps one vocabulary and one watched subtree. It costs a few lines
  here and is easiest to carry from the start, because every claim prefix and every cover is
  spelled from it. See [one slice or several](gh-source.md#one-slice-or-several).
- **The client carries ETags from the beginning**, rather than waiting for step 5. That is a
  reorder from the note's own advice and the reason is scale: at four surfaces and a one-minute
  resync the note computes 240 requests/hour/repo, and this step's read is nearer six requests a
  pass, so a PAT's 5,000/hour is spent somewhere around a dozen repositories. With 304s not
  counting against the primary limit a quiet repo costs nothing, which is what makes more than a
  handful possible at all. Conditional requests are a transport concern and cheaper to build in
  than to retrofit around a client that never had a cache; what stays in step 5 is the
  measurement, the throttle, and the decision the numbers inform.
- **Two covers per kind**: `{repo, state: open}` complete, `{repo, state: closed}` a window of
  the most recent `-settled`.
- **Two reads per pass**: the live set to exhaustion, and everything updated since a high-water
  mark. The second is what carries an issue that closed between passes into the answer with its
  new state instead of letting it go absent from the live half.
- **The mark is a cursor, and is allowed** because losing it degrades to the exhaustive read.
  That is the rule the design note states generally; this is its first user.
- **`-max-open` errors rather than truncates.** A partial list under a complete cover is how a
  reflection tombstones what it merely failed to read.
- **A PR is not an issue.** `GET /repos/{o}/{r}/issues` returns both; the issue read filters on
  the `pull_request` key. Getting this wrong makes `github:issue:…#42` and `github:pr:…#42` the
  same number reflected twice, and makes "every open issue" quietly include review traffic.
- **Startup checks the credential**, as Slack's `auth.test` and Linear's `teams` do: a bad token
  is a task that does not start, not a source that installs clean and fails once a minute.

**`Owns()` grows with the steps, and must not run ahead of them.** A source claims what it
writes and no more: three claims per repo here, four after step 2, five after step 3. Claiming a
kind this build does not reflect refuses a neighbouring install over entities nobody is writing,
and the git source has a test for exactly that invariant (`TestGitOwnsCoversWhatItReflects`). A
registration installed mid-way needs re-installing to pick up the later claims.

**Proves:** a targeted pass covers `{repo, number}` and tombstones nothing else — the
mass-deletion test, written first and watched fail; two passes over one fixture write nothing on
the second; an issue that closes appears in the settled half with `state: closed` and is
tombstoned when it ages out; `-max-open` exceeded is an error and writes nothing; a PR never
appears as an issue.

### 2. Actions runs

The window-only kind, alone now that the repo entity has moved to step 1.

- `github:run:<org>/<name>/<id>` — the last `-runs`, covered by `{repo}`, tombstoned as they
  slide. No live/settled split: a queued run becomes a completed run in minutes and a completed
  one is never interesting again.

  ~~and since runs are ordered by creation a run in flight is always inside the window.~~
  **Wrong, and it is a raggedness rather than a bug to fix.** More runs than `-runs` can be
  created while one long job is going, and that one then falls out and is tombstoned, so a
  charter waiting on its conclusion never sees one. It cannot be designed away with the API on
  offer: the runs listing has no ordering of its own, and its `status` filter takes a single
  value, so there is no read that returns "everything not finished" — and an enumeration of
  statuses would leave whatever GitHub adds next uncovered and standing forever. This is Slack's
  aged-out thread parent, and the lever is the same: `-runs` wants to be comfortably more than a
  repository creates while its longest job runs.
- **Check state lands on the PR as a field**, not as `github:check:*` entities.

**Proves:** the window slides and tombstones; the raggedness above is pinned rather than
believed, along with the larger window that mitigates it; the PR's check field moves with the run
and costs no new kind, and costs no request either — it is derived from the runs already read, by
head sha, where asking GitHub per pull request would be one call each.

**And the level-triggering limit gets written down where a charter author meets it**, not only in
the note: a run goes `queued → in_progress → completed` inside a minute, so a pass will
frequently collapse all three into one write, and a rule matching `status: in_progress` may never
fire. Match `conclusion`. This is the trigger model's own rule about states rather than edges,
and it is the first connector where somebody will expect otherwise.

### 3. Discussions

GraphQL, and the one surface with a second failure mode.

- Repository Discussions has no REST API, so this is a GraphQL client beside a REST one. That is
  the cost, and it is why discussions are their own step rather than folded into step 1.
- **A repo with discussions disabled is not a repo with no discussions.** A failed or refused
  discussions read drops the discussion cover *and* the discussion entities, leaving what could
  not be read standing — Slack's users asymmetry exactly, and the same reasoning: a pass that
  lost its issues has nothing true left to say and fails, one that lost its discussions is still
  complete for everything else.

**Proves:** discussions disabled leaves any existing discussion entities standing and reflects
the rest of the repo; a discussion's own number space does not collide with an issue's; the
no-op-pass test gets a discussion case.

### 4. The webhook

The delivery half, owned by the source, as the git hook and the Slack and Linear endpoints are.

- `X-Hub-Signature-256`, HMAC-SHA256 of the raw body, compared with `hmac.Equal`.
- The event→ref table from the note. A delivery that cannot be placed is a **full resync**, not a
  silence.
- **The hint names the thing, not its container** — this is the first source where that pays,
  because GitHub answers a single issue in one request. Which is what makes the targeted cover of
  step 1 load-bearing rather than theoretical.
- ~~Where no webhook is available, `GET /repos/{o}/{r}/events` at the `X-Poll-Interval` GitHub
  asks for is a hint stream with about five minutes of delay, and it stays inside the source.~~
  **Deferred, and named rather than dropped.** verse's resync net already converges a repository
  with no hook; what the events poller buys is latency, and it costs a background loop, a cursor
  over deliveries already seen, and a request per repository per poll — against a budget this
  connector is already short of. The trigger that would revive it: a repository somebody drives
  where they cannot install a hook and a minute of latency is too much.

**Proves:** an unsigned or wrongly-signed delivery is refused, and so is every delivery when no
secret is configured; a valid one hints exactly one ref, including the `issue_comment` on a pull
request that arrives looking like an issue; a delivery naming no repository hints nothing, which
is a full pass, while one naming another source's repository hints not at all; no delivery ever
writes an entity — asserted by sending a forged title and reading GitHub's own back; and a
targeted run pass covers that run alone and lands its state on the pull request it ran for.

### 5. Conditional requests, and the budget

The measurement, and the throttle. The cache itself arrives with the client in step 1 — see
there for why the note's ordering did not survive contact with more than a handful of repos —
so what is left here is proving it behaves and finding out what it bought.

- An ETag per endpoint, `If-None-Match`, and a 304 answered from the last successful reflection.
  GitHub documents a 304 as not counting against the primary rate limit, which is what makes a
  quiet repository nearly free.
- **A failure is never cached** — the law the whole seam turns on.
- **Losing the cache costs one full read**, never correctness.
- `source.Throttle` stays as the backstop for the burst case.

**Proves:** a 304 produces the same reflection as the 200 before it; an error passes through and
leaves the cache alone; a cold source answers what a warm one does, which is what makes losing
the cache cost work rather than correctness.

### Measured, against thresholds named first

The thresholds were written down before the numbers were read: a warm pass at most **3**
chargeable requests per repository, a cold one at most **12**. Chargeable is issued less those
answered 304, because that is what the rate limit counts.

| per repository per pass | chargeable | issued |
|---|---|---|
| cold (first pass, or after a restart) | 9 | 11 |
| warm (nothing changed) | **2** | 9 |
| warm, `-discussions=false` | **0** | 7 |

**The entire steady-state budget is discussions**, and nothing else moves it. Every REST read
here is conditional and a 304 does not count against the limit, so seven of the nine requests a
warm pass issues are free; the two that are not are the discussions queries, which are GraphQL,
and a POST carries no ETag. At a one-minute resync that is 120 chargeable an hour per repository,
so a personal token covers about **40 repositories** — against the note's estimate of twenty,
which assumed no caching at all.

So the decision the numbers inform is not GitHub App auth. It is a flag: `SkipDiscussions` takes
a quiet repository to nothing chargeable, which makes the number of repositories one token
reflects effectively unbounded for a deployment that does not want them. App auth moves from
deferred to next only for a deployment that wants discussions on more than about forty
repositories — which is a sharper trigger than the one the note left, and a smaller piece of work
than the one it was going to justify.

### 6. `sourced github`, and the drive

The subcommand, the manifest, the docs, and the only step anybody can see.

- `cmd/sourced/github.go`, `deploy/github-source` (off by default, its own knob), the rows in
  [reference/sourced.md](../reference/sourced.md), and the index edits.
- `GITHUB_TOKEN` and `GITHUB_WEBHOOK_SECRET` from the environment, never a flag.

**The join needs a git source from somewhere**, and that is a decision rather than a detail.
`lineage.sh` correlates a commit, a PR and a release; this connector supplies the PR and the
release, and `git:commit:<repo>/<sha>` is the other half. Runners clone for themselves, so
nothing else in a deployment requires a git source — which means the drive has to deploy one for
the repository it correlates, or demonstrate the two-system half and say so. Worth settling
before the step rather than discovering at it.

**Proves:** the whole thing, against a real repository — and the acceptance test is the one the
note names: [`scripts/lineage.sh`](../../scripts/lineage.sh) runs with its `lingh:` seeding
deleted and its rule pointed at `github:pr:*`. That is a correlation over a real PR, a real
commit and a real release, which is what the script was written to demonstrate and has never
had real data for.

**Done.** The script now runs that way and passes. It did not argue about vocabulary; it turned
up three mechanism facts the invented systems had hidden — the join is a list read, a rule does
not backfill, and a derived address is sanitised and hashed. [gh-source.md](gh-source.md)
records all three where the prediction was made.

Expect the argument that step to start to be about **vocabulary, not mechanism**: the script
correlates on `stage: merged`, `head` and `target`, and this source writes `merged`, `headSha`
and `base`. One of the two spellings has to move, and the interesting question is which — the
script is a demonstration and the source is the contract, so probably the script.

## What this does not do

- **No projects.** `github:project:*` is org-owned and spans repositories, so it wants a slot per
  repo — which now exists, and is the second pass rather than the first. Nothing else in this
  plan touches slots.
- **No people.** `github:user:*` belongs to GitHub rather than to any repository. The answer is a
  separate source claiming that kind alone, installable beside any number of repo sources
  *because* the repo claims name their kinds; it needs no new mechanism and no change here.
- **No comments**, which is the deferral most likely to be wrong. The note's argument is that a
  comment stream is Slack-shaped and the SDLC facts are on the issue — but `verse-dev` step 2
  wants a contract notice that is objectable by any, and an objection is a comment. If that drive
  concludes a notice is a gate, this comes back.
- **No releases**, no check-run entities, and no GitHub App auth. Each is named in the note with
  the trigger that would revive it.

## What would stop it

- **Nobody has a repository to drive it against.** Step 6 is the point of the whole thing, and
  the interesting failures — rate limits, ETag behaviour, `since` semantics, Actions collapsing
  states — only appear against real GitHub. Without that, steps 1–5 are a fixture, and the honest
  thing is to say so and stop rather than carry a connector nothing uses. This is the same
  question Linear's projects faced and passed; here it is unanswered.
- **The external facts in the note are asserted, not all verified.** The 304 rate-limit
  behaviour and the primary limits were checked against GitHub's documentation; the pagination
  caps on `/actions/runs`, the GraphQL cost of a discussions page, and the exact semantics of
  `issues?since=` were not. Any of them can move a step's shape, and step 1 is where the first
  one will.
- **The settled window may be the wrong shape.** It is the note's central bet and nothing has
  used it. If a repository's closed issues turn out to matter to a real charter, the window
  becomes a floor somebody has to raise, and the two-cover split is worth re-opening rather than
  tuning.
