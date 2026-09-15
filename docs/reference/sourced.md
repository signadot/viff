# sourced

The surface of the connector binary. For what a source *is*, see [../sources.md](../sources.md);
for the registration, the ownership globs and the routes verse serves, see
[sources.md](sources.md); to reflect something that is not here, copy
[examples/source](examples/source/main.go).

`sourced` is a **second binary**, in the same image as `verse` and `o`. Five subcommands, one
external system each:

```
sourced git       [flags]   a git repository, or a mirror of a remote one
sourced git-issue [flags]   the issues `git issue` keeps inside one
sourced slack     [flags]   a Slack workspace
sourced linear    [flags]   Linear teams
sourced github    [flags]   GitHub repositories
```

Each serves the same two routes verse dials (`GET /source`, `GET /reflect`), plus — for the three
that have one — the delivery endpoint it owns. Install one exactly like any other source:

```sh
sourced slack -addr :9801
verse source add slack.workspace http://localhost:9801
verse source
```

## Common flags

| | |
|---|---|
| `-addr` | what to listen on (`SOURCED_ADDR`, default `:9801`) |
| `-min-interval` | answer from the last reflection when asked again inside this window. The source's own rate limit — verse's resync interval is a constant and an API budget is not. Default off |
| `-hint-url` | verse's hint route for **this registration** (`VERSE_HINT_URL`): `http://verse:9797/sources/<system>/<name>/hint`. Without it, deliveries are verified and dropped, and only the resync converges |
| `-system` | the place this source writes under — the first segment of its addresses — defaulting to the connector's own name |

**`-system` is the operator's, and it is a coarser separation than `owns`.** More than one source
gets deployed and the partition between them is drawn by whoever runs them — by credential, by
budget, by which repositories are busy — which has no reason to line up with how the external
system is organised. Two deployments sharing a system is the ordinary case and needs only
disjoint claims, which are computed from each source's own configuration rather than written by
hand. One that wants no coordination at all takes a system of its own: two systems are disjoint
on the first segment, so neither owes the other a proof. It is also how a second *instance* of a
product — a GitHub Enterprise Server beside github.com — gets a place that is not confused with
the first, since `org/repo` #42 is unique within a host and not across two.

What it costs is paid by whoever chose it: **a charter's path begins with a literal** — it names
the subtree to watch — so every rule about a separated place names it. That is why this is a flag
rather than the shape.

A credential is **never a flag**. It comes from the environment, because that is what a k8s
Secret fills in and what keeps it out of the pod spec, `ps` and shell history.

## git

```
sourced git -repo   [<id>=]<dir>    a repository already on disk (repeatable, comma-separated)
            -mirror [<id>=]<url>    a remote to mirror and keep fetched (repeatable)
            -state  <dir>           where -mirror clones live (SOURCED_STATE, default /repos)
```

`-repo` is the machine-that-has-it case; verse can also reflect a local repo with no program at
all (`verse source add -dir`). `-mirror` is the deployed case: a pod has no repositories, so
the source clones one at startup and fetches on every pass. **Fetching is the read** — nothing
is ever pushed — and a fetch that fails fails the pass, so a mirror that has stopped tracking
says so in `lastError` instead of quietly answering last week's shas.

An unreachable remote or a missing credential is a **startup** failure, not a per-pass one.

The hint is a git hook (`source.Git.InstallHook`), which posts to verse directly; there is
nothing for this program to receive. A mirror has no hook — nobody pushes to it — so it
converges on the resync.

| entity | payload |
|---|---|
| `git.<repo>.ref.<name>` | `repo, name, sha, type` — branches, tags and remote-tracking branches, and nothing else |
| `git.<repo>.head` | `repo, ref, sha, dirty`, and `ahead`/`behind` when an upstream is set |
| `git.<repo>.commit.<sha>` | `repo, sha, msg, author, parents` (a list of shas: empty for a root commit, two for a merge, first parent first), and `trailers` when the commit has any — covered by nothing, so history stays |

**A repository is a level of the address**, so everything one repository reflects stands under
`git.<repo>`. Owns `git.<repo>` per repo — a place, with no glob — and the install takes an
owner record there for its installer, which is what every ref and commit beneath is owned
through. A ref name keeps its slashes inside one segment: `git.verse.ref."origin/main"`.

**Three ref spaces, named rather than assumed.** `for-each-ref` with no pattern is every ref, and
a mirror maps every ref the remote has — so this asked for `refs/heads`, `refs/tags` and
`refs/remotes` explicitly. On GitHub the rest is `refs/pull/<n>/head` and `refs/pull/<n>/merge`
(4739 of them on one mirrored repo) plus `refs/issues/*`, which is git-issue's namespace. The
merge refs are the reason it matters rather than a size complaint: GitHub recreates them
whenever the base branch moves, so every fetch after any push to main changed a batch of shas
and the store took writes on every pass with nothing having happened. A pull request's tip is
`github.<org>.<name>.pr.<n>.headSha`, from the source that owns the concept.

`msg` is the **subject** — the first line — and the body is not reflected. `trailers` is the
exception: the `Key: value` block at the end of a message, as an object, so a charter can
condition on what a commit *declared* rather than search prose.

```tony
condition:
  subject: {path: [git, verse, commit, "*"], value: {trailers: {Tier: change}}}
```

and `!glob "*"` in place of a value matches any commit that *declared* the key at all — which is
how a charter finds the pushes a coding runner made, by their `Verse-Task`.

**Every key is reflected; none is interpreted.** There is no allowlist, because this source
cannot tell a meaningful key from an accidental one better than the repository can — and a
trailer block is a *heuristic* over the last paragraph, so a message that merely ends with
something shaped like `Status: proposal` yields `Status`, in git's own parser too. The
convention therefore belongs in the key **namespace**: matching `Verse-Task` is safe, matching
a bare `Status` never was. Absent when a commit has no trailer block, rather than an empty
object on every commit.

## git-issue

```
sourced git-issue -repo   [<id>=]<dir>    a repository already on disk (repeatable, comma-separated)
                  -mirror [<id>=]<url>    a remote to mirror and keep fetched (repeatable)
                  -state  <dir>           where -mirror clones live (SOURCED_STATE, default /repos)
                  -bodies                 reflect each issue's description body and its discussion's text (default true)
```

[`git issue`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/cmd/git-issue) keeps its
issues **in the repository, as refs** — `refs/issues/<xidr>` open, `refs/closed/<xidr>` closed,
each pointing at a commit whose tree holds `meta.tony` and `description.md`. So there is no
service to call and no credential to hold: reflecting a tracker is reading a directory, and the
same `-repo`/`-mirror` registry the git source takes serves this one.

**It is a separate subcommand from `git`, not more kinds on it.** The two answer different
questions about one directory — what the repository *is* versus what somebody *filed* in it —
and an operator who wants branch state without a tracker, or a tracker without fifty commits a
pass, says so by running one and not the other. Running both over the same repository is the
ordinary case: they write disjoint places, so nothing coordinates.

| entity | payload |
|---|---|
| `git-issue.<repo>.issue.<xidr>` | `repo, id, title, status`, and `createdAt, updatedAt, body, labels, commits, branches, related, blocks, blockedBy, duplicates, closedBy, metaError`; `comments` (how many) and, with any, `lastCommentAt`; under `-bodies` the `discussion` itself as `[{at, text}]`. The lists and `comments` are always present, empty when there is nothing, so a charter may reference them |
| `git-issue.<repo>.repo` | `repo, open, closed, issues` |

Owns `git-issue.<repo>` per repo, a place, for the reason git's claim is one.

**Status comes from the ref namespace, not from `meta.tony`.** Closing an issue *moves* its ref,
which is what preserves its history — and it leaves a `status` field behind in the tree that a
hand-edited or half-migrated issue can disagree with. The namespace is the one that cannot be
wrong. Match on `status`, which is that answer.

**A title lives in the first line of `description.md`**, not in `meta.tony` — `Issue.Title` is
`tony:"-"` upstream — so `# ` is stripped and what remains is the title. `-bodies=false` drops
the rest, which is the difference between an index of issue state and a copy of everyone's prose.

**The issues are covered once, complete**, which is the deliberate difference from the GitHub connector.
GitHub covers issues twice — the open set complete, the closed set as a window — because its
closed back catalogue is unbounded and every page costs a request against a budget. Here the
enumeration is one `git for-each-ref` over a local directory, listing every issue open and
closed for one process and no network, so the answer really is complete: a closed issue keeps
its entity for as long as its ref exists, and one whose ref is deleted is tombstoned because it
is genuinely gone. Every blob a pass needs is then read in a single `git cat-file --batch`
rather than two `git show`s per issue — 190 issues across two real trackers reflect in about
180ms.

**A mirror carries the tracker**, which is the deployed case: `git clone --mirror` maps every
ref, so `refs/issues` and `refs/closed` arrive with the branches, and a pass fetches — so
somebody else's `git issue push` reaches this source without a restart. It is in fact the *only*
way it does, since issues travel as refs.

**A malformed issue does not take the pass down with it.** Some issue refs in circulation carry
a six-digit number and `id: 5`, a number where the struct wants a string. `meta.tony` is
therefore treated as *enrichment*: the id and status come from the ref and the title from
`description.md`, so such an issue is reflected with what is knowable and a `metaError` field
saying what was not. `git issue migrate` is the fix, and until somebody runs it the rest of the
tracker keeps converging. Upstream's own reader does not manage this: `git issue show` errors on
such an issue and `git issue list` silently omits it.

**There is no hook, and convergence is the resync.** A repository has one
`reference-transaction` hook and `sourced git` is usually holding it, so this source does not
take it. An operator who wants lower latency adds a line to their own hook:

```sh
curl -fsS -X POST "$VERSE_HINT_URL?at=git-issue.<id>" >/dev/null 2>&1 || true
```

A mirror has no hook either — nobody pushes to it — and it is also the only way this source sees
somebody else's `git issue push`, since issues travel as refs and a fetch is how they arrive.

## slack

```
sourced slack -workspace <name>    the id prefix (default: the team name the token belongs to)
              -channel <name|id>   narrow to these (repeatable; default: every channel the bot is in)
              -depth <n>           messages per channel in the window (default 20)
              -threads             also reflect replies of threaded messages in the window (default true)
              -users               reflect the people (default true; needs users:read)
```

| environment | |
|---|---|
| `SLACK_BOT_TOKEN` | the bot token (`xoxb-…`). Required |
| `SLACK_SIGNING_SECRET` | the app's signing secret. Without it `POST /slack/events` refuses every delivery |

The venue is a Slack app's **bot user**: `channels:read` + `groups:read` to list what it is in,
`channels:history` + `groups:history` for the window, `users:read` for the people. **The bot's
membership is the configuration** — it is invited to what you want reflected, and
`users.conversations` is what the source lists, rather than every public channel it could name
but not read.

Point the app's Event Subscriptions request url at `https://<host>/slack/events`.

| entity | payload |
|---|---|
| `slack.channel.<ws>/<C…>` | `workspace, channel, name, private, archived`, and `topic, purpose, members, created` |
| `slack.message.<ws>/<C…>/<ts>` | `workspace, channel, ts, text`, and `user, bot, username, subtype, thread, replies, edited, reactions, url` |
| `slack.user.<ws>/<U…>` | `workspace, user, name, bot, deleted`, and `real, display, email, title, tz` |

`reactions` is an object, name → count, so `{reactions: {shipit: null}}` is the presence test a
charter writes.

Owns `slack.*.<workspace>/*` — one claim, since every id is workspace-prefixed.

**The window is bounded and covered**, which is the deliberate difference from git's commits: a
repository's history is finite and immutable, a channel's is an unbounded stream, so a message
that falls out of `-depth` is tombstoned rather than kept. One raggedness comes with it — a
reply does not bump its parent in the channel's history, so a long-running thread stops being
reflected when its parent ages out.

**The people are a separable claim.** A `users.list` that fails drops the user cover *and* the
user entities, so a missing scope leaves them standing instead of deleting them; the channels
are not treated that way, because a pass that lost them has nothing true left to say.

## linear

```
sourced linear -team <KEY>        a team to reflect (repeatable, comma-separated;
                                  default: every team the credential can see)
               -page-size <n>     issues per GraphQL page (default 100)
               -max-issues <n>    refuse a team bigger than this (default 5000)
```

| environment | |
|---|---|
| `LINEAR_API_KEY` | the `Authorization` header **verbatim**: a personal API key (Settings → API) goes bare, an OAuth access token as `Bearer lin_oauth_…`. Required |
| `LINEAR_WEBHOOK_SECRET` | the webhook signing secret. Without it `POST /linear/webhook` refuses every delivery |

Point Settings → API → Webhooks at `https://<host>/linear/webhook`.

| entity | payload |
|---|---|
| `linear.team.<KEY>` | `team, id, name, private`, and `description` |
| `linear.issue.<KEY>-<n>` | `team, id, identifier, title, state, stateType, priority, url, createdAt, updatedAt`, and `priorityLabel, completedAt, estimate, assignee, assigneeEmail, labels, project, cycle, parent` |
| `linear.cycle.<KEY>/<n>` | `team, id, number`, and `name, startsAt, endsAt, completedAt` |
| `linear.project.<uuid>` | `owners: {<KEY>: {team, id, name, url, …}}` — one slot per owning team, and nothing at the top |

Match on **`stateType`**, not `state`: the type (`triage`, `backlog`, `unstarted`, `started`,
`completed`, `canceled`) is Linear's own vocabulary, while the name beside it is whatever
somebody called a column last Tuesday.

Owns `linear.team.<KEY>`, `linear.issue.<KEY>-*`, `linear.cycle.<KEY>/*` and
`linear.project.*` **at** `owners.<KEY>` per team. The hyphen in the issue claim is load-bearing
— `ENG-*` does not claim `ENGINE-1`.

**A team is read to exhaustion**, and `-max-issues` is an error rather than a truncation: the
answer covers the whole team, and a partial list under a complete cover tombstones what it
merely failed to read.

**A project is reflected by slot**, because it belongs to several teams at once and so has no
positional owner: no team can claim it as an address, and two sources over two teams would each
tombstone what the other wrote. Each team is instead the single writer of one place inside it —
`owners.<KEY>` — proved apart at install and kept apart by converge, with the entity going when
the last owner leaves.

What that costs is **a project with no top**: every field Linear has about a project is global,
so each owner's slot carries its own copy and there is no `name` at the root. A charter reads a
project through an owner (`owners.ENG.state`). An issue still carries its project's name as a
plain field, which is what a rule about an issue wants and costs no join.

## github

```
sourced github -repo <org>/<name>   a repository to reflect (repeatable, comma-separated)
               -settled <n>         closed issues, PRs and discussions kept per repo (default 50)
               -runs <n>            workflow runs kept per repo (default 50)
               -comments <n>        comments kept per repo (default 200)
               -max-open <n>        refuse a repo with more open items than this (default 2000)
               -discussions         reflect discussions (default false)
```

| environment | |
|---|---|
| `GITHUB_TOKEN` | a fine-grained PAT, read-only on the repositories' **Contents, Metadata, Issues, Pull requests and Actions** (Discussions too with `-discussions`). Required. Contents is easy to leave off and is the first thing read: a pass with it missing dies on `git/ref/heads/<branch>` with `403 Resource not accessible by personal access token` |
| `GITHUB_WEBHOOK_SECRET` | the webhook signing secret. Without it `POST /github/webhook` refuses every delivery |
| `GITHUB_API_BASE` | the API root, for GitHub Enterprise Server |

Point the repository's Settings → Webhooks at `https://<host>/github/webhook`, content type
`application/json`.

| entity | payload |
|---|---|
| `github.<org>.<name>.repo` | `repo, org, name, private, archived, fork`, and `defaultBranch, defaultBranchSha, cloneUrl, sshUrl, description, topics, url` |
| `github.<org>.<name>.issue.<n>` | `repo, number, title, state, comments`, and `author, url, createdAt, updatedAt, closedAt, stateReason, body, milestone, labels, assignees` |
| `github.<org>.<name>.pr.<n>` | `repo, number, title, state, draft, merged`, and `author, base, head, headSha, checks, url, …, labels, assignees` |
| `github.<org>.<name>.discussion.<n>` | `repo, number, title, state, answered, comments`, and `author, category, url, …` |
| `github.<org>.<name>.run.<id>` | `repo, id, status, runNumber, attempt`, and `workflow, event, conclusion, branch, headSha, url, startedAt, updatedAt, actor` |
| `github.<org>.<name>.comment.<n>.<id>` | `repo, on, id, kind`, and `author, body, url, createdAt, updatedAt` |

**A repository is two levels of the address**, because it is two levels at GitHub: everything
`signadot/verse` reflects stands under `github.signadot.verse`, and a number is quoted where it
is written (`github.signadot.verse.pr."42"`). Owns `github.<org>.<name>` per repository — a
place, so `acme.app` and `acme.app2` are apart with nothing else said — and the install takes an
owner record there for its installer.

**The set-shaped places are covered twice**: the open set complete, the closed set as a window of
the most recent `-settled`. Neither precedent fits — git keeps history, Slack keeps a window —
because an issue set is unbounded while its *live* part is bounded. An item that closes appears
in the settled half carrying its new state, ages, and leaves. Exceeding `-max-open` is an error
rather than a truncation: a partial list under a complete cover tombstones what it failed to read.

**Runs are a window and nothing else**, covered whole. A run still in flight when more than
`-runs` newer ones have been created falls out and is tombstoned, so a charter waiting on its
conclusion never sees one — the lever is a larger `-runs`. Match `conclusion`, not a transition:
a run goes `queued → in_progress → completed` inside a minute and a pass will often collapse all
three into one write.

**Comments are entities, and a window**, like Slack's messages: the most recently updated
`-comments` are kept and what falls out is tombstoned, because a conversation is an unbounded
stream and an entity-verse holding all of one is a warehouse. Two repository-wide listings are
read — conversation comments (which an issue has, and which a pull request's discussion *is*) and
review comments on a diff — so it costs two conditional requests a pass rather than one per item,
and `kind` says which a comment was.

A comment hangs off the item it is on (`….comment."7"."900"`), so "the comments on #7" is a place
rather than a predicate over the repository. `on` carries that number, and the join is by ref
rather than by a field naming a kind: `github.<org>.<name>.issue.<n>` and
`github.<org>.<name>.pr.<n>` are two addresses and exactly one of them exists.

**An issue carries a comment count and a pull request does not**, which looks inconsistent and is
not: the count comes from the issues listing, and a pull request read from the pulls listing has
none — the two are windowed separately, so a closed PR can sit inside one and past the end of the
other, and the count would read 0 there and flip as items updated. The comments themselves answer
what that field was for.

**CI state is a field on the pull request**, derived from the runs already read by head sha, so
it costs no request. A PR whose head has no run in the window carries no `checks` field.

**Discussions are separable and are the whole of the budget.** Every REST read here is
conditional and a 304 does not count against the rate limit, so a repository where nothing
happened costs nothing — except discussions, which are GraphQL and carry no ETag: two chargeable
requests per repository per pass, which is what caps one token at about forty repositories.
So they are **off by default**: they are opt-in per repository on GitHub's side, so most
repositories have none, and a source that asked anyway would spend the whole of a quiet
repository's budget being told so. With them on, a repository that does not have them still
costs nothing — the `has_discussions` field rides the repository read, which is conditional and
free on a warm pass, so the GraphQL query is skipped without asking.

Switching them off, or a repository with the feature disabled, or a refused read, all drop the
discussion covers *with* the entities, leaving what is standing alone — rather than reading an
empty answer as "there are none". Which is also why flipping them off after they have been
reflected leaves what was written standing, frozen: nothing covers it, so nothing tombstones it.

## Deploying them

`deploy/git-source`, `deploy/slack-source`, `deploy/linear-source` and `deploy/github-source`
are the manifests, each off by default and turned on by its own knob (`-e slackSource=true`,
`-e githubSource=true -e githubRepos=org/name`).

**git-issue has no manifest yet**, which is a gap rather than a decision — it is the same
binary and the same `-repo`/`-mirror` flags as `git-source`, so the manifest is that one with
the subcommand and the knob changed. Run it outside the cluster, or copy `deploy/git-source`,
until somebody driving it needs one. See
[deploy/README.md](https://github.com/signadot/verse/blob/main/deploy/README.md) for the credentials, the ingress and the registration
step — which stays a verb somebody runs, because an install carries an ownership claim verse
checks against what is already standing.
