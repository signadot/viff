# sourced — the sources verse ships

The surface of the connector binary. For what a source *is*, see [../sources.md](../sources.md);
for the registration, the ownership globs and the routes verse serves, see
[sources.md](sources.md); to reflect something that is not here, copy
[examples/source](examples/source/main.go).

`sourced` is a **second binary**, in the same image as `verse` and `o`. Three subcommands, one
external system each:

```
sourced git    [flags]   a git repository, or a mirror of a remote one
sourced slack  [flags]   a Slack workspace
sourced linear [flags]   Linear teams
```

Each serves the same two routes verse dials (`GET /source`, `GET /reflect`), plus — for the two
that have one — the delivery endpoint it owns. Install one exactly like any other source:

```sh
sourced slack -addr :9801
verse source add slack:workspace http://localhost:9801
verse sources
```

## Common flags

| | |
|---|---|
| `-addr` | what to listen on (`SOURCED_ADDR`, default `:9801`) |
| `-min-interval` | answer from the last reflection when asked again inside this window. The source's own rate limit — verse's resync interval is a constant and an API budget is not. Default off |
| `-hint-url` | verse's hint route for **this registration** (`VERSE_HINT_URL`): `http://verse:9797/sources/<system>/<name>/hint`. Without it, deliveries are verified and dropped, and only the resync converges |

A credential is **never a flag**. It comes from the environment, because that is what a k8s
Secret fills in and what keeps it out of the pod spec, `ps` and shell history.

## git

```
sourced git -repo   [<id>=]<dir>    a repository already on disk (repeatable, comma-separated)
            -mirror [<id>=]<url>    a remote to mirror and keep fetched (repeatable)
            -state  <dir>           where -mirror clones live (SOURCED_STATE, default /repos)
```

`-repo` is the machine-that-has-it case; verse can also reflect a local repo with no program at
all (`verse source add --dir`). `-mirror` is the deployed case: a pod has no repositories, so
the source clones one at startup and fetches on every pass. **Fetching is the read** — nothing
is ever pushed — and a fetch that fails fails the pass, so a mirror that has stopped tracking
says so in `lastError` instead of quietly answering last week's shas.

An unreachable remote or a missing credential is a **startup** failure, not a per-pass one.

The hint is a git hook (`source.Git.InstallHook`), which posts to verse directly; there is
nothing for this program to receive. A mirror has no hook — nobody pushes to it — so it
converges on the resync.

| entity | payload |
|---|---|
| `git:ref:<repo>/<name>` | `repo, name, sha, type` |
| `git:head:<repo>` | `repo, ref, sha, dirty`, and `ahead`/`behind` when an upstream is set |
| `git:commit:<repo>/<sha>` | `repo, sha, msg, author, parents` — covered by nothing, so history stays |

Owns `git:*:<id>/*` and `git:head:<id>` per repo.

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
| `slack:channel:<ws>/<C…>` | `workspace, channel, name, private, archived`, and `topic, purpose, members, created` |
| `slack:message:<ws>/<C…>/<ts>` | `workspace, channel, ts, text`, and `user, bot, username, subtype, thread, replies, edited, reactions, url` |
| `slack:user:<ws>/<U…>` | `workspace, user, name, bot, deleted`, and `real, display, email, title, tz` |

`reactions` is an object, name → count, so `{reactions: {shipit: null}}` is the presence test a
charter writes.

Owns `slack:*:<workspace>/*` — one claim, since every id is workspace-prefixed.

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
| `linear:team:<KEY>` | `team, id, name, private`, and `description` |
| `linear:issue:<KEY>-<n>` | `team, id, identifier, title, state, stateType, priority, url, createdAt, updatedAt`, and `priorityLabel, completedAt, estimate, assignee, assigneeEmail, labels, project, cycle, parent` |
| `linear:cycle:<KEY>/<n>` | `team, id, number`, and `name, startsAt, endsAt, completedAt` |

Match on **`stateType`**, not `state`: the type (`triage`, `backlog`, `unstarted`, `started`,
`completed`, `canceled`) is Linear's own vocabulary, while the name beside it is whatever
somebody called a column last Tuesday.

Owns `linear:team:<KEY>`, `linear:issue:<KEY>-*` and `linear:cycle:<KEY>/*` per team. The
hyphen in the issue claim is load-bearing — `ENG-*` does not claim `ENGINE-1`.

**A team is read to exhaustion**, and `-max-issues` is an error rather than a truncation: the
answer covers the whole team, and a partial list under a complete cover tombstones what it
merely failed to read.

**Projects are not reflected**, and their absence is an argument rather than an omission: a
Linear project can belong to several teams, so no team can claim one as an address, and two
sources over two teams would each tombstone what the other wrote. An issue carries its
project's name as a field instead. See
[working/sources-residual.md](../working/sources-residual.md).

## Deploying them

`deploy/git-source`, `deploy/slack-source` and `deploy/linear-source` are the manifests, each
off by default and turned on by its own knob (`-e slackSource=true`). See
[deploy/README.md](../../deploy/README.md) for the credentials, the ingress and the registration
step — which stays a verb somebody runs, because an install carries an ownership claim verse
checks against what is already standing.
