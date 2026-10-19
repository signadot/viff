# iod

`iod` is the binary that holds the five [iods](../glossary.md#iod) that verse ships. An iod is a
program that connects verse to one external system. It has two halves.

| Half | What it does |
|---|---|
| the [source](../glossary.md#source), which is the in half | It [reflects](../glossary.md#reflect) the external system: it keeps the state of that system in the [store](../glossary.md#store), as [entities](../glossary.md#entity). |
| the [sink](../glossary.md#sink), which is the out half | It acts on the external system. For example, it posts a message. |

This page says how to run each of the five. Other pages hold the rest.

| Subject | Page |
|---|---|
| what an iod is, its two halves, and which side opens each connection | [../io.md](../io.md) |
| the installed iod, the ownership patterns, and the routes that verse serves | [io.md](io.md) |
| an iod for a system that is not on this page | [examples/io](examples/io/main.go), which is a program that you copy |

`iod` is a binary of its own. It is in the same image as `verse` and `o`. It has five
subcommands, and each subcommand serves one external system:

```
iod git       [flags]   a git repository, or a mirror of a remote one
iod git-issue [flags]   the issues `git issue` keeps inside one
iod slack     [flags]   a Slack workspace
iod linear    [flags]   Linear teams
iod github    [flags]   GitHub repositories
```

Each subcommand serves the two routes that verse calls, `GET /io` and `GET /reflect`. The
subcommands `slack`, `linear` and `github` also serve a route that receives deliveries. A
*delivery* is a message that the external system sends when something changes there, such as
a webhook call.

You install one of the five as you install any other iod:

```sh
iod slack -addr :9801
verse io add slack http://localhost:9801 -root slack
verse io
```

## Common flags

Every subcommand accepts these flags.

| Flag | Environment | Default | What it sets |
|---|---|---|---|
| `-addr` | `SOURCED_ADDR` | `:9801` | the address that the iod listens on |
| `-admin-addr` | `SOURCED_ADMIN_ADDR` | the port of `-addr`, plus 1 | the address of the admin listener, which serves pprof. The value `off` disables it. |
| `-min-interval` | | off | a window of time. When verse asks again inside the window, the iod answers from its last reflection. |
| `-hint-url` | `VERSE_HINT_URL` | none | the hint route of verse for this installed iod: `http://verse:9797/io/<name>/hint` |
| `-root` | | the name of the subcommand | the [root](../glossary.md#root-of-an-iod) of the iod: the first segment of every [address](../glossary.md#address) that it writes |
| `-verse-addr` | `VERSE_ADDR` | the host of `-hint-url` | the verse that the out half reads and writes, as `host:port` |
| `-as` | | `agent:<root>-io`, such as `agent:slack-io` | the [principal](../glossary.md#principal) that the out half reads and writes as. The Slack out half reads the asks in a message to a person as that person, since a person's ask is read by whom it asks. |
| `-out-every` | | `5s` | how often the out half reads what is asked of it |
| `-token-file` | `VERSE_TOKEN_FILE` | none | the file holding the iod's own credential toward verse: on Kubernetes, the pod's projected service-account token, which verse's `auth.iod` names the iod by ([iods at the door](perms.md#iods-at-the-door)). Read on every request, since the kubelet rotates it. Without it, the iod is whoever `-as` says. |

With `-token-file`, every request the iod makes carries the token, and one it makes as a person
(the Slack out half reading their ask, a reply in a thread answering it) carries the person and
their Slack account beside it, `slack:<user id>`, so that a verse which verifies people admits
it through the account's binding and the ballot says which iod it came through
([a person relayed by a trusted iod](perms.md#a-person-relayed-by-a-trusted-iod)).

`-verse-addr`, `-as`, `-out-every` and `-token-file` configure the out half. Four subcommands
have an out half, `git`, `slack`, `linear` and `github`, and each starts it with `-out`.
`git-issue` accepts the flags and does not use them.

`-out` needs the address of the verse. It refuses to start without one of `-verse-addr`,
`VERSE_ADDR` and `-hint-url`. `git` has no hint url, since its hint is a hook, so its out half
is given the address outright.

**`-min-interval`** is the rate limit of the iod itself. Verse asks each iod for a full
reflection at a constant interval of one minute ([the reflector](io.md#the-reflector)). The
budget of an external API is not a constant, so the iod sets its own limit.

**`-hint-url`**. A [hint](../glossary.md#hint) is a message from an iod to verse that says
which addresses went stale. Without `-hint-url`, the iod verifies each delivery and then drops
it. Only the full reflection of every minute then
[converges](../glossary.md#converge) the store.

**`-root`** belongs to the operator who deploys the io. It separates two iods more coarsely
than `owns` does. `owns` is the list of the [claims](../glossary.md#claim-of-an-iod) of an iod:
the patterns that name what only that iod may write.

| Case | What to do |
|---|---|
| Two deployments share a root. | This is the ordinary case. The two need only disjoint claims. Each iod computes its claims from its own configuration, so nobody writes them by hand. |
| A deployment wants no coordination with any other. | Give it a root of its own. Two roots differ in the first segment, so neither iod must prove anything to the other. |
| A second instance of one product runs, such as a GitHub Enterprise Server beside github.com. | Give it a root of its own. `org/repo` #42 is unique within one host, and not across two. |

A root of its own has a cost, and whoever chose the root pays it. The address in a
[charter](../glossary.md#charter) begins with a literal segment, which names the subtree to
watch. So every [rule](../glossary.md#rule) about a separated root names that root.
[The root is a flag](#the-root-is-a-flag) gives the reason for the flag.

**A credential is never a flag.** It comes from the environment.

A Kubernetes Secret fills an environment variable. A flag is visible in the pod spec, in `ps`
and in the shell history, and an environment variable is not.

## git

`iod git` reflects git repositories.

| Flag | Value | Environment | Default | What it sets |
|---|---|---|---|---|
| `-repo` | `[<id>=]<dir>` | | | a repository that is already on disk. The flag is repeatable, and one value may hold several, separated by commas. |
| `-mirror` | `[<id>=]<url>` | | | a remote repository to mirror and keep fetched. The flag is repeatable. |
| `-state` | `<dir>` | `SOURCED_STATE` | `/repos` | the directory that holds the clones of `-mirror`, one directory for each repository id |
| `-tree` | `[<name>=]<path>` | | | a directory whose tree sha is reflected on every ref, as `tree.<name>`: the identity of its contents at that ref, and the empty tree where the directory is not there. The name defaults to the path's last segment, and is one plain word. The flag is repeatable. |
| `-out` | | | `false` | The iod also runs the out half ([the out half](#the-out-half-of-git)): a tag made, a branch moved, a tree of charters installed. |

`iod git` needs at least one `-repo` or one `-mirror`.

| Credential | What it is |
|---|---|
| none of its own | Without the App's keys, `iod git` reads no credential from the environment. `-mirror` runs git, so a private remote needs a credential that git finds by itself. The manifest `deploy/git-io` gives git a credential helper that reads a Secret. With `-out`, a mirror pushes with the same credential, which must then be allowed to push. The charters it installs it installs as itself (`-as`, `agent:git-io` by default), so it needs `child-owner` where they go. |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` | a GitHub App's installation, as the github iod takes it. Given, the installation's token is the only credential git presents over https, as `x-access-token`, minted again before it lapses, and any helper set before it is not tried: a fetch and a push are the App's. The three go together. |

```
iod git -repo   [<id>=]<dir>    a repository already on disk (repeatable, comma-separated)
            -mirror [<id>=]<url>    a remote to mirror and keep fetched (repeatable)
            -state  <dir>           where -mirror clones live (SOURCED_STATE, default /repos)
            -tree   [<name>=]<path> reflect the tree sha of this directory on every ref (repeatable)
            -out                    also make the tags, move the branches and install the trees
                                    of charters asked for under git.<repo>.out (see below)
```

**`-repo`** is for a machine that already has the repository. Verse can also reflect a local
repository with no program at all: `verse io add -dir`.

**`-mirror`** is for a deployed io. A
[mirror](../glossary.md#mirror-of-a-git-repository) is a copy of a remote repository, kept
fetched. A pod has no repositories, so the iod clones the remote at startup and fetches it at
every reflection.

- **Fetching is the read.** The reflection never pushes anything. The out half pushes exactly
  the refs it is asked for, and nothing else.
- A fetch that fails makes the reflection fail. So a mirror that stops following its remote
  says so in `lastError`. It does not answer with old shas.
- An unreachable remote, or a missing credential, is a failure at **startup**. It is not a
  failure of each reflection.

**The hint** is a git hook (`io.Git.InstallHook`). The hook posts to verse directly, so
`iod git` receives nothing. A mirror has no hook, because nobody pushes to a mirror. A mirror
converges at the full reflection of every minute.

### What git reflects

| Entity | Payload |
|---|---|
| `git.<repo>.ref.<name>` | `repo, name, sha, type`, and `tree` for each directory `-tree` names: `tree: {<name>: <object sha>}`, the empty tree where the directory is not there. These are branches, tags and remote-tracking branches, and nothing else. |
| `git.<repo>.head` | `repo, ref, sha, dirty`, and `ahead`/`behind` when an upstream is set |
| `git.<repo>.commit.<sha>` | `repo, sha, msg, author, parents`, and `trailers` when the commit has any. `parents` is a list of shas: empty for a root commit, two for a merge, first parent first. No [cover](../glossary.md#cover-of-a-reflection) reaches a commit, so history stays. |

A cover is a pattern under which a reflection is complete. Verse
[tombstones](../glossary.md#tombstone) a stored entity that a cover reaches and that the
reflection does not hold.

**A repository is a level of the address.** Everything that one repository reflects stands
under `git.<repo>`.

The iod claims `git.<repo>` for each repository. The claim is one address, with no `*`. The
install writes an owner [record](../glossary.md#record) at that address for the installer.
Every ref and every commit beneath the address is owned through that record.

A ref name keeps its slashes inside one segment: `git.verse.ref."origin/main"`.

**The iod names three ref spaces.** It asks git for `refs/heads`, `refs/tags` and
`refs/remotes`, and for nothing else. The tip of a pull request is
`github.<org>.<name>.pr.<n>.headSha`, from the iod that owns pull requests.
[Three ref spaces are named](#three-ref-spaces-are-named) gives the reason.

**`msg`** is the subject of the commit, which is the first line of its message. The iod does
not reflect the body.

**`trailers`** is the block of `Key: value` lines at the end of a message, as an object. With
it, a charter tests what a commit *declared*, and does not search the text of the message.

```tony
condition:
  subject:
    path: "git.verse.commit.*"
    value:
      trailers:
        Tier: change
```

`!glob "*"` in place of a value matches any commit that declared the key at all. A charter
finds the pushes of a coding runner in this way, by their `Verse-Task`.

**The iod reflects every key and interprets none.** It has no allowlist. `trailers` is absent
when a commit has no trailer block. It is not an empty object on every commit.

Match a key that has a **namespace**, such as `Verse-Task`. Do not match a bare `Status`.
[Every trailer key is reflected](#every-trailer-key-is-reflected) gives the reason.

**`tree`** is the sha of a directory's tree object at the ref, for each directory `-tree`
names, under the name given it (`-tree .verse/charters` is `tree.charters`). A tree's sha is the
identity of the directory's contents: two refs that differ only outside the directory carry the
same one, and so does the same change after a squash or a rebase. A charter that keeps a
repository's rules installed compares this, not the commit (git-issue j9vbwvja).

A ref where the directory is not there carries git's **empty tree**,
`4b825dc642cb6eb9a060e54bf8d69288fbee4904`, which every repository has without storing it. No
files is a state of the directory like any other, so removing the directory reads as a change
to it, and installing the empty tree with the out half removes every charter at the address.

```tony
condition:
  subject:
    path: "git.verse.ref.\"origin/main\""
    value:
      tree:
        charters: !glob "*"
```

### The out half of git

With `-out`, `iod git` writes refs where it reads them. Somebody, a person or a charter, writes
an **input** under the repository's out place, `git.<repo>.out`, and the iod answers with a
**status** beside it, as the other out halves do ([the out half](io.md#the-out-half)).

| input | does | status |
|---|---|---|
| `{to: tag.<name>, sha}` | makes the tag at the commit | `ref, sha, at` |
| `{to: branch.<name>, sha}` | moves the branch to the commit, forward or back | `ref, sha, at`, and `was`, the sha it stood at |
| `{to: branch.<name>, sha, create: true}` | makes the branch at the commit where there is none, and leaves one that stands alone; a tag takes `create` too | `ref, sha, at`, and `kept: true` where it stood already |
| `{to: install.<address>, tree, sha}` | installs the charters in the tree object into the verse at the address, as `verse c -a <address> -d <dir> --prune` would, from the iod's own copy of the repository; `sha` is the commit it is installed for | `tree, sha, address, at`, and `charters`, how many |
| `{to: check.<address>, tree}` | asks the verse's charter check what an install of the tree at the address would do, and installs nothing | `tree, address, checkedAt`, and the check's `verdict` (`installs` or `refused`), `refusal`, `packages` and `pruned` |
| `{to: check.<address>, ref, of, round}` | the same check for the tree a ref holds now in the iod's own copy (`of` names the `-tree` directory, and may be left out where there is one), made again whenever `round` changes | as above, with `ref`, `sha` and `round`; `changes`, how many charters the install would add, change, remove or prune (0 is in step with what runs); `differingFor`, the rounds in a row it has differed at one commit; a ref the repository lacks is the verdict `missing` |
| `{to: obsolete, prefix, keep, before}` | answers `obsolete`, as the iod, every pending question waiting on it whose id begins with `prefix`, whose key (the id after the prefix, up to `~`) is none of the strings under `keep`, posed by `before`, and offering `obsolete` | `obsoleted`, the ids answered; `sweptAt`, `before` |

```sh
verse data git verse out live -put '{to: tag.live-verse.3, sha: 4f668c1c}'
verse data git verse out head -put '{to: branch.verse/live, sha: 4f668c1c}'
verse data git verse out install -put '{to: install.repos.verse, tree: 4b825dc6, sha: 4f668c1c}'
verse data git verse out check -put '{to: check.repos.verse, tree: 4b825dc6}'
verse data git verse out livecheck -put '{to: check.repos.verse, ref: verse/live, round: "1"}'
```

- **A check is not an install.** It reads the tree's charter files as the install does and asks
  `POST /charter/check`, which answers what an install at the address would do without doing
  it: a verdict, and per package what it adds, changes, removes and holds. A verdict of
  `refused`, a file that does not parse among them, is an answer on the status and not a failed
  input; a tree the repository lacks is asked about again on the next pass, since a pull request's
  tree can be asked about before this iod has fetched it, and a verse that cannot be reached is
  tried again. A tree checked already costs nothing. Checked by `ref` with a `round`, it is the
  repo-charters reconciler's minute check that verse/live is what is installed.
- **An obsolete sweep settles what nothing proposes.** The charter that asked decides what is
  still proposed and names it in `keep`; the iod only applies it, and only to questions posed by
  `before`, the instant `keep` was read at, so a question about something newer is left for the
  next sweep. The iod answers because the charter named it among the question's answerers, so
  nobody grants anything for it.
- **An install is the iod's own act.** It archives the tree out of its mirror, reads every
  charter file in it (a README is left alone), and makes one request as itself: every charter in
  the tree or none, and what the tree leaves out under the address removed. A tree with no
  charter file, the empty tree among them, removes every charter at the address. So the iod has to own
  the address, or hold `child-owner` above it (`verse perms repos --grants '[{child-owner:
  [agent:git-io]}]'` on a deployment). A tree the repository lacks, a file that does not parse, or
  a charter the verse refuses is refused for good until the input changes; a verse that cannot be
  reached is tried again. A tree already installed for the same commit costs nothing. It is how
  the repo-charters reconciler installs what the team said yes to (git-issue dzxn44gw).

- **`sha`** is any name the repository resolves to a commit: a full sha, a short one, a tag. One
  the repository does not have is refused for good.
- **A tag is not moved.** A tag that stands at another sha is refused for good. A tag is a
  record, and a rollback is a new tag on the older commit.
- **A branch moves with a lease** on the sha it last stood at, so a move somebody else made
  meanwhile is a refusal that is tried again, and never an overwrite.
- **A ref already at the sha costs nothing.**
- **Where the ref is written.** A mirror pushes to the remote it fetches from, with the
  credential it fetches with, and writes its own copy so the next reflection sees it. A
  repository on disk gets the ref written in place.

This half is what the repo-charters reconciler uses to keep `verse/live` at the commit of the
tree the team said yes to, to install the tree `verse/live` holds, and to tag the commit a tree
first ran from ([the example](examples/repo-charters/repo-charters.tony)). It makes no commit and no tree.

## git-issue

`iod git-issue` reflects the issues that
[`git issue`](https://pkg.go.dev/github.com/signadot/tony-format/go-tony/cmd/git-issue) keeps
inside a git repository.

| Flag | Value | Environment | Default | What it sets |
|---|---|---|---|---|
| `-repo` | `[<id>=]<dir>` | | | a repository that is already on disk. The flag is repeatable, and one value may hold several, separated by commas. |
| `-mirror` | `[<id>=]<url>` | | | a remote repository to mirror and keep fetched. The flag is repeatable. |
| `-state` | `<dir>` | `SOURCED_STATE` | `/repos` | the directory that holds the clones of `-mirror` |
| `-bodies` | | | `true` | The iod reflects the description body of each issue, and the text of its discussion. |

`iod git-issue` needs at least one `-repo` or one `-mirror`. It needs no credential of its
own. For a private remote, `-mirror` needs the credential that git finds, as for `iod git`.

```
iod git-issue -repo   [<id>=]<dir>    a repository already on disk (repeatable, comma-separated)
                  -mirror [<id>=]<url>    a remote to mirror and keep fetched (repeatable)
                  -state  <dir>           where -mirror clones live (SOURCED_STATE, default /repos)
                  -bodies                 reflect each issue's description body and its discussion's text (default true)
```

[`git issue`](https://pkg.go.dev/github.com/signadot/tony-format/git-issue) keeps its issues
**in the repository, as refs**.

| Ref | What it is |
|---|---|
| `refs/git-issues/v1/open/<xidr>` | an open issue |
| `refs/git-issues/v1/closed/<xidr>` | a closed issue |

Each ref names a commit, and the tree of that commit holds `meta.tony` and `description.md`.
So the iod calls no service and holds no credential. To reflect a tracker is to read a
directory. `iod git-issue` takes the same `-repo`/`-mirror` flags as `iod git`.

**The layout belongs to issuelib, and the iod takes it from there.** The `v1` is the
generation of git-issue, and it changes when the layout changes. A client of git sees only
the refs that it names. So the iod lists the prefixes that its own version of the library
defines. The next generation arrives with the version of go-tony that brings it.

The iod does not read refs in the layout that precedes the generations (`refs/issues/`,
`refs/closed/`). `git issue` adopts those refs in any clone that it runs in, and a push from
that clone migrates the remote that a mirror fetches.

**`git-issue` is a subcommand of its own**, and running both `iod git` and `iod git-issue`
over one repository is the ordinary case. The two write disjoint addresses, so nothing
coordinates them. [A separate subcommand](#a-separate-subcommand) gives the reason.

### What git-issue reflects

| Entity | Payload |
|---|---|
| `git-issue.<repo>.issue.<xidr>` | `repo, id, title, status`, and `createdAt, updatedAt, body, labels, keys, commits, branches, related, blocks, blockedBy, duplicates, closedBy, metaError, multiValued`. `comments` is how many comments the issue has. With any comment, the payload has `lastCommentAt`. Under `-bodies`, the payload has the `discussion` itself, as `[{at, text}]`. The lists, `keys` and `comments` are always present, and empty when there is nothing, so a charter may name them. |
| `git-issue.<repo>.repo` | `repo, open, closed, issues` |

The iod claims `git-issue.<repo>` for each repository. The claim is one address, for the same
reason as the claim of `iod git`.

**`status` comes from the namespace of the ref, and not from `meta.tony`.** Match on `status`.
[Status comes from the ref](#status-comes-from-the-ref) gives the reason.

**The iod reflects a `key=value` label as a key and a value.** git-issue splits a label at its
first `=`, and a key holds one value. So `git-issue-phase=planned` is
`keys: {git-issue-phase: planned}`, and a rule matches a phase directly:

```tony
condition:
  subject:
    path: "git-issue.verse.issue.*"
    value:
      keys:
        git-issue-phase: planned
```

`labels` holds the plain labels only, so a label with a key appears once.

A key may hold two values. A merge by a git-issue older than v0.0.233 can leave this, because
those versions merge labels as a union. The iod reads such a key as git-issue reads it: the
last value. The iod keeps every value that the key held under `multiValued`, which is present
only in this case. So a charter can see the conflict, and need not trust the value that
survived.

The prefix `git-issue-` is reserved for the conventions of a program that drives the tracker.

**The title is the first line of `description.md`.** It is not in `meta.tony`, because
`Issue.Title` is `tony:"-"` in git-issue. The iod removes `# ` from the line, and the rest is
the title. `-bodies=false` drops the rest of the file. The store then holds an index of the
state of the issues, and not a copy of what everyone wrote.

**The iod covers the issues once, and the cover is complete.** A closed issue keeps its entity
for as long as its ref exists. The iod tombstones an issue when its ref is deleted, because
the issue does not exist any more. The iod lists every issue with one `git for-each-ref`, and
reads every blob of a reflection with one `git cat-file --batch`.
[The issues are covered once](#the-issues-are-covered-once) compares this with `iod github`.

**A mirror carries the tracker.** This is the deployed case. `git clone --mirror` maps every
ref, so the refs of the open and the closed issues arrive with the branches. Each reflection
fetches. So a `git issue push` by somebody else reaches the iod without a restart. A fetch is
the only way it reaches the iod, because issues travel as refs.

**A malformed issue does not stop the reflection.** Some issue refs carry a number of six
digits and `id: 5`, which is a number where the struct wants a string. The iod treats
`meta.tony` as an addition. The id and the status come from the ref, and the title comes from
`description.md`. So the iod reflects such an issue with what it can know, and a `metaError`
field says what it cannot know.

`git issue migrate` repairs such an issue. Until somebody runs it, the rest of the tracker
goes on converging. The reader of git-issue itself does less: `git issue show` fails on such
an issue, and `git issue list` leaves it out and says nothing.

**The iod has no hook, and it converges at the full reflection of every minute.** A repository
has one `reference-transaction` hook, and `iod git` usually holds it. So `iod git-issue` does
not take it. An operator who wants a shorter delay adds a line to the hook:

```sh
curl -fsS -X POST "$VERSE_HINT_URL?at=git-issue.<id>" >/dev/null 2>&1 || true
```

A mirror has no hook, because nobody pushes to a mirror.

## slack

`iod slack` reflects a Slack workspace. With `-out`, it also posts messages in Slack.

| Flag | Value | Default | What it sets |
|---|---|---|---|
| `-workspace` | `<name>` | the name of the team that the token belongs to | the prefix of every id |
| `-channel` | `<name>` or `<id>` | every channel that the bot is in | The iod reflects these channels only. The flag is repeatable. |
| `-depth` | `<n>` | `20` | the number of messages in the window of each channel |
| `-threads` | | `true` | The iod also reflects the replies to the threaded messages in the window. |
| `-users` | | `true` | The iod reflects the people. This needs `users:read`. |
| `-out` | | `false` | The iod also runs the out half ([the out half](#the-out-half--out)). |

| Environment | What it is |
|---|---|
| `SLACK_BOT_TOKEN` | the bot token (`xoxb-…`). Required. |
| `SLACK_SIGNING_SECRET` | the signing secret of the app. Without it, `POST /slack/events` refuses every delivery. |
| `SLACK_APP_TOKEN` | the app-level token (`xapp-…`, `connections:write`). With it, the iod connects to Slack in Socket Mode, and the deliveries of events arrive on that connection. With `-out`, a reply that answers a question arrives there too. Without it, nothing in Slack can answer a question. |

```
iod slack -workspace <name>    the id prefix (default: the team name the token belongs to)
              -channel <name|id>   narrow to these (repeatable; default: every channel the bot is in)
              -depth <n>           messages per channel in the window (default 20)
              -threads             also reflect replies of threaded messages in the window (default true)
              -users               reflect the people (default true; needs users:read)
              -out                 also say in Slack what is written under slack.<ws>.out, and answer
                                   the questions those messages ask (see below)
              -verse-addr <addr>   the verse the out half reads and writes (VERSE_ADDR; default: -hint-url's host)
              -as <principal>      who the out half reads and writes as (default agent:slack-io)
              -out-every <d>       how often the out half reads what is asked (default 5s)
```

`-verse-addr`, `-as` and `-out-every` are [common flags](#common-flags). `iod slack` is one
of the two subcommands that use them.

The iod acts as the **bot user** of a Slack app. The bot needs these scopes to reflect.

| Scopes | What the iod does with them |
|---|---|
| `channels:read` + `groups:read` | It lists the channels that the bot is in. |
| `channels:history` + `groups:history` | It reads the window of each channel. |
| `users:read` | It reads the people. |

**The membership of the bot is the configuration.** You invite the bot to the channels that
you want reflected. The iod lists `users.conversations`, which is the channels that the bot is
in. It does not list every public channel, because the bot can name a public channel and
still not read it.

Deliveries arrive in one of two ways.

| Way | What it needs |
|---|---|
| Socket Mode | The app is in Socket Mode, and `SLACK_APP_TOKEN` is set. The deliveries arrive on the connection that the iod opens. Nothing needs a public url. |
| HTTP | The request url of the app's Event Subscriptions is `https://<host>/slack/events`. |

In both ways, a delivery is a hint that names the channel it is about. A delivery that names
no channel asks for a full reflection.

The app subscribes to the bot events for what it reflects (`message.channels`,
`message.groups`, `channel_rename`, …). It subscribes to `message.im`, with the scope
`im:history`, when a question that is asked in a DM must be answerable there.

### What slack reflects

| Entity | Payload |
|---|---|
| `slack.<ws>.channel.<C…>` | `workspace, channel, name, private, archived`, and `topic, purpose, members, created` |
| `slack.<ws>.message.<C…>."<ts>"` | `workspace, channel, ts, text`, and `user, bot, username, subtype, thread, replies, edited, reactions, url` |
| `slack.<ws>.user.<U…>` | `workspace, user, name, bot, deleted`, and `real, display, email, title, tz` |

**The workspace is a level of the address.** A message stands under its channel, so a channel
and its history are one subtree. `verse data slack acme message C123` is the window of that
channel. A timestamp is quoted because it looks like a number.

`reactions` is an object that maps a name to a count. So `{reactions: {shipit: null}}` is how
a charter tests that a reaction is present.

The iod claims `slack.<workspace>`. It is one claim, because everything that the iod writes
stands under that address.

**The window is bounded and covered.** The iod tombstones a message that leaves `-depth`. It
does not keep it. The window has one irregularity: a reply does not move its parent up in the
history of the channel. So the iod stops reflecting a long thread when the parent of the thread
leaves the window. [The window is covered](#the-window-is-covered) compares this with the
commits of `iod git`.

**The people are a separate cover.** When `users.list` fails, the iod drops the cover of the
users *and* the user entities from its answer. So a missing scope leaves the stored users as
they are, and does not delete them. The iod does not treat the channels in this way: a reflection
that lost the channels has nothing true to say.

### The out half: `-out`

With `-out`, `iod slack` also posts messages in Slack. Somebody, a person or a charter, writes
an **input** under the `out` address of the workspace, `slack.<ws>.out`. The iod answers with
a **status** beside the input.

| | where | written by | holds |
|---|---|---|---|
| input | anywhere under `slack.<ws>.out` | whoever asks | `to`, `text`, `asks` and `thread` |
| status | the sibling `<name>~status` | the iod alone | `channel, ts, thread, permalink, postedAt, updatedAt, posted`, and `error` while an error stands. `posted` is the fingerprint of the message. |

```sh
verse data slack acme out hello   -put '{to: "user:ana@acme.test", text: "the review is waiting on you"}'
verse data slack acme out release -put '{to: C0456, text: "v1.3 is out"}'
verse data slack acme out 'hello~status'
```

`to` is a Slack id, or somebody that verse knows.

| `to` | Who it is | What the iod does |
|---|---|---|
| `U…` | a person | It opens the DM of the person. |
| `C…` and `G…` | channels | It posts in the channel as it is, so the bot must be in the channel. |
| `user:<email>`, or the bare email | a person, named as the store names them | It finds the person with `users.lookupByEmail`, and sends a DM. |

`<name>` is the choice of the writer. The out half needs the scopes `chat:write`, `im:write`
and `users:read.email`, in addition to the scopes of the reflection.

`thread` is the ts of a message. The iod posts the input's message as a reply in that thread, and
a reply in the thread is matched to the questions of the message. Several inputs can speak in
one thread, and a reply there is about the questions of all of them.

The spelling `user:<email>` lets a charter address the people that a
[question](../glossary.md#question) waits on, because the `waitingOn` of a question holds
principals. The iod writes the DM that it opens on the status. So the iod looks up a person once
for each request, and not once for each pass.
[The people are named by email](#the-people-are-named-by-email) gives the reason.

**A pass is level-triggered.** A *pass* is one run of the out half, and a pass runs every
`-out-every`. A pass acts on the state that it reads, and not on events. In a pass, the iod
lays out the message that each input asks for. It compares the message with what the status
says it posted, and does only what is missing.

| The input | What the pass does |
|---|---|
| is new | It posts the message. |
| would show differently: its `posted` fingerprint does not match | It sends a `chat.update` of the same message. It does not post a second message. |
| would show differently, and its message was deleted (`message_not_found`) | It posts the message again. |
| already shows as asked | Nothing. So after a restart the iod resumes from the statuses, and does not repeat a message. |
| is refused | It writes `error` once, in the words of Slack, with `failed: permanent`, and offers the input again only when it is edited ([A refusal is permanent](io.md#what-is-asked-and-what-became-of-it)). |
| fails another way (`ratelimited`, `service_unavailable`, `internal_error`, `fatal_error`, `request_timeout`, a 5xx) | It writes `error` once. The next pass tries again. |

**The status is a sibling, and not a child.** So an input can grow structure, and the
structure does not collide with the status. The iod never reads a name that ends in `~status`
as an input. When somebody removes an input, its status stays, and the message stays as it
is.

**Only the iod writes a status, and the permissions of verse enforce it.** The iod reads and
writes the store as its own principal. The principal is `-as`, and it is `agent:slack-io` by
default.

**The install writes the grants.** You name the out half when you install the iod, with three
flags of `verse io add` ([the out half](io.md#the-out-half)).

| Flag of `verse io add` | What it names |
|---|---|
| `-out` | the address under which the out half reads inputs |
| `-as` | the principal that the out half acts as. The default is `agent:<root>-io`. |
| `-requesters` | who may write an input, as a list of [grantees](../glossary.md#grantee) with commas between them |

```sh
verse io add slack http://localhost:9801 -root slack \
  -out slack.acme.out -as agent:slack-io -requesters group:verse-dev
```

The install writes four [grants](../glossary.md#grant-permission) on the record of the out
address.

| Grant | To | What it allows |
|---|---|---|
| `read-write`, below the address | the requesters | They write inputs. |
| `read`, below the address | the principal of `-as` | The iod reads the inputs. |
| `child-owner` | the principal of `-as` | The iod owns each status that it creates. |
| `child-grants` of `{read: ["*"]}` | everyone | Everyone may read a status. |

Without `-requesters`, the install grants nobody the right to write an input, and the owner
of the out address writes that grant by hand. Written by hand, the four grants are these
commands:

```sh
verse perms slack acme out --grant '{read-write: [<askers>]}' --below
verse perms slack acme out --grant '{read: [agent:slack-io]}' --below
verse perms slack acme out --grant '{child-owner: [agent:slack-io]}'
verse perms slack acme out --child-grants '{read: ["*"]}'
```

The iod holds `child-owner`, so it owns each status that it creates. The owner record of a
status stops the grant of the requesters from above. So the requesters can read a status and
cannot write it.

The grant of the requesters is `--below`, and not a child grant. Verse copies a child grant
onto every record that is created beneath, and so onto the records of the statuses too.

Nothing stops a requester who creates a node with the name of a status first. A grant says
who may create beneath a node. It never says which name belongs to whom. Verse then refuses
the write of the iod, and the iod logs the refusal.

The out address stands outside the covers of the reflection (`channel`, `message`, `user`).
So a reflection never tombstones it.

#### A message that asks a question

An input may name one question, or several, that it is about. The message then says what
each question says at that moment, and how to answer it:

```sh
verse data slack acme out review -put '{
  to: "user:ana@acme.test",
  text: "slack-try asks: approve demo.slack.1?",
  asks: "verse.trigger.slack-try.approval~efcf56b5"}'
```

The iod writes those lines itself, because the words of the writer could say anything. At
every pass the iod reads each question from its ask, `verse.ask.<id>`, which everyone may read.

| the question | the line, and what follows it |
|---|---|
| pending | see the list below |
| answered | `<id> answered <verdict> by <who>`, then what they said with the answer. The [ballots](../glossary.md#ballot) follow where more than one answerer settled the question, or where any ballot carried words. |
| withdrawn | `<id> was withdrawn unanswered`, and why |
| not posed | `No question <id> stands`. The rest appears when the question stands. |

For a pending question, the message has these lines, in this order:

1. The `pose` of the question, when its [gate](../glossary.md#gate-of-a-rule) wrote one.
   Otherwise the line is `Answers <id>`.
2. The `about` of the question, on a line of its own. It is what the subject of the question
   says of itself.
3. `waiting on …`, from the `waitingOn` of the question. A wildcard reads as "the
   [docket](../glossary.md#docket)".
4. `so far: …`, when somebody has cast a ballot. Each ballot has what its answerer said with
   it.
5. When a deadline stands: the option that the question then settles on, and when. The time
   is in the timezone of the reader.
6. `Answer in this thread:`, and the [options](../glossary.md#option) that the question
   takes. Each option has its `says`, when the gate wrote one.

So when a question settles, the iod draws its message again, and nobody writes anything. In a
channel, everyone sees the verdict and who gave it, and not only the person who answered.

**A message is text, and not blocks.** Slack renders mrkdwn in the text.
[A message is text](#a-message-is-text) gives the reason.

#### Answering: a reply in the thread

With `SLACK_APP_TOKEN` set, a reply in the thread of a message answers the question, when the
reply says one of the options of the question. The iod connects to Slack in Socket Mode. So
the app needs no public url, and the verse needs no Ingress. The reply arrives as an ordinary
message event.

The iod decides two things, and it decides them separately.

| What the iod decides | How |
|---|---|
| **Which** question a reply answers | It does not read the words. The parent of the thread is a message that the iod posted, and the iod remembers what each of its messages asks. |
| **What** the reply says | It reads the words against the options of that question, and against nothing else. A reply never has an effect of its own. |

**How much of the words the iod reads depends on who the reply is addressed to.**

| The reply is addressed | What the iod reads |
|---|---|
| to the thread | One word. The reply must say an option: the word of the question itself, or `y`/`yes` and `n`/`no` for the two options that every question has. |
| to the bot by name, such as `@verse approve it pls` | A sentence. |

Everyday words stand for the two options that every question has: `approve`, `yes`, `lgtm`,
`ship`, `go`; and `reject`, `no`, `block`, `hold`. An option that the gate defines is said by
its id, which is the word that its author chose.

The iod does not resolve a sentence that holds a negation, and it says so. "Do not ship it"
and "ship it" differ by one word.
[How much of a reply is read](#how-much-of-a-reply-is-read) gives the reason.

**Words that answer nothing are recorded on the question.** The iod says them on the question
(`POST /asks/{id}/say`,
as the person who typed them). So the reason that something waits stands beside
the question, for whoever reads it later. The words do not stay only in the window where
somebody typed them. They decide nothing.

The iod replies to nobody for talking, with one exception. Somebody may address the bot by
name and say something that the iod cannot read as an answer. The iod tells that person so, and
tells them that it kept their words.

**A thread stays open after its question settles.** The iod still hears what people say in the
thread, and still records those words on the question. Why the question went that way, and
what it cost, are worth as much as the words before. When somebody addresses the bot there,
the iod tells them what became of the question.

**The iod answers as the person who typed the reply.** `users.info` turns the Slack id of the
person into `user:<email>` (`users:read.email`). The answer goes to `POST /asks/{id}/answer` as
that person, so the permissions of verse decide whether the person may answer.

| Outcome | Where the iod says it |
|---|---|
| the answer is accepted | in the thread, where everyone who reads the thread can see it |
| the answer is refused: the person is not an [answerer](../glossary.md#answerer), or somebody answered first | to the person alone, in the words of verse. A refusal is about the person, and not about the question. |

The iod never answers as `-as`. An answer recorded as `agent:slack-io` is an answer that
nobody gave. An answer also asks for a pass at once, so the iod draws the message again while
the person looks at it.

#### Linking: `/verse link <nonce>`

A person who is a verse user proves to the app which one they are by saying, from their Slack
account, a nonce their verse gave them: `verse account -link slack` prints `/verse link <nonce>`.
Said as a slash command (the app registers `/verse`), typed in a DM to the app, or at the bot
(`@verse link <nonce>`), the iod reports to the verse, as itself, that this account said it
(`POST /accounts/slack/heard`), and the verse binds the account to whoever claimed the nonce
([accounts in other systems](perms.md#accounts-in-other-systems)). The person alone is told what
became of it. From then on a reply of theirs is relayed through the binding, which is the only
way in where the provider names no `domains`.

The Slack id is the word of Slack, on a connection that the app token opened. What the iod
tells the verse is that id, as the person's account, beside its own token; the verse admits the
request as the person only through the account's binding, and never on the iod's say-so alone
([a person relayed by a trusted iod](perms.md#a-person-relayed-by-a-trusted-iod)).

## linear

`iod linear` reflects Linear teams. With `-out`, it also comments on Linear issues.

| Flag | Value | Default | What it sets |
|---|---|---|---|
| `-team` | `<KEY>` | every team that the credential can see | a team to reflect. The flag is repeatable, and one value may hold several, separated by commas. |
| `-page-size` | `<n>` | `100` | the number of issues in one GraphQL page |
| `-max-issues` | `<n>` | `5000` | The iod refuses a team that has more issues than this. |
| `-out` | | `false` | The iod also runs the out half ([the out half of linear](#the-out-half-of-linear)). |

| Environment | What it is |
|---|---|
| `LINEAR_API_KEY` | the `Authorization` header, **verbatim**. A personal API key (Settings → API) goes bare. An OAuth access token goes as `Bearer lin_oauth_…`. Required. |
| `LINEAR_WEBHOOK_SECRET` | the signing secret of the webhook. Without it, `POST /linear/webhook` refuses every delivery. |

```
iod linear -team <KEY>        a team to reflect (repeatable, comma-separated;
                                  default: every team the credential can see)
               -page-size <n>     issues per GraphQL page (default 100)
               -max-issues <n>    refuse a team bigger than this (default 5000)
               -out               also comment on the issue named by what is written under
                                  linear.<KEY>.out, and write each one's <name>~status
```

In Linear, set Settings → API → Webhooks to `https://<host>/linear/webhook`.

### What linear reflects

| Entity | Payload |
|---|---|
| `linear.<KEY>.team` | `team, id, name, private`, and `description` |
| `linear.<KEY>.issue.<KEY>-<n>` | `team, id, identifier, title, state, stateType, priority, url, createdAt, updatedAt`, and `priorityLabel, completedAt, estimate, assignee, assigneeEmail, labels, project, cycle, parent` |
| `linear.<KEY>.cycle."<n>"` | `team, id, number`, and `name, startsAt, endsAt, completedAt` |
| `linear.project.<uuid>.owners.<KEY>` | `team, id, name, url, …`. There is one node for each team that owns the project, and nothing at the project itself. |

**The team is a level of the address.** Everything that a team has alone stands under the
team, and `verse data linear ENG` is all of it. The number of a cycle is quoted because it
looks like a number.

Match on **`stateType`**, and not on `state`. The type is one of the words of Linear itself:
`triage`, `backlog`, `unstarted`, `started`, `completed`, `canceled`. The `state` beside it
is the name that somebody gave to a column, and anybody may change that name.

The iod claims `linear.<KEY>` and `linear.project.*.owners.<KEY>` for each team. These are two
addresses, and neither has a `*` inside a segment. `linear.ENG` and `linear.ENGINE` are
different addresses, because verse compares names, and not prefixes of strings.

**The iod reads a team to the end.** A team with more issues than `-max-issues` is an error.
The iod does not cut the list short. The answer covers the whole team, and a partial list
under a complete cover tombstones what the iod only failed to read.

**A project is owned one level down.** Each team is the single writer of one node inside the
project, `linear.project.<uuid>.owners.<KEY>`. The install proves these nodes disjoint, and
converging keeps them apart. The project goes when its last owner leaves.
[A project is owned one level down](#a-project-is-owned-one-level-down) gives the reason.

The cost is **a project with no top**. Every field that Linear has about a project is global.
So the node of each owner carries its own copy, and the project itself has no `name`. A
charter reads a project through an owner (`linear.project.<uuid>.owners.ENG.state`). An issue
still carries the name of its project as a plain field. A rule about an issue wants that
field, and it needs no join.

### The out half of linear

With `-out`, `iod linear` comments on Linear issues. It reads inputs under one address for
each team, `linear.<KEY>.out`. Each of these addresses is inside the claim of its team and
outside every cover of the reflection.

| | where | written by | holds |
|---|---|---|---|
| input | anywhere under `linear.<KEY>.out` | whoever asks | `to`, which is the identifier of the issue, such as `ENG-42`, and `text` |
| status | the sibling `<name>~status` | the iod alone | `comment, url, postedAt, updatedAt, posted`, and `error` while an error stands. `comment` is the id of the comment, and `posted` is the fingerprint of its text. |

A pass runs every `-out-every`, as the principal of `-as`, which is `agent:linear-io` by
default.

| The input | What the pass does |
|---|---|
| is new | It finds the issue by its identifier, and writes a comment on it. |
| has a text that changed | It updates the same comment. It does not write a second comment. |
| has a text that changed, and its comment was deleted (`Entity not found`) | It comments again. |
| has the text that the comment carries | Nothing. |
| is refused: a 4xx, an error Linear marks as the request's own (`userError`), an issue the credential cannot see, or no text | It writes `error` once, with `failed: permanent`, and offers the input again only when it is edited ([A refusal is permanent](io.md#what-is-asked-and-what-became-of-it)). |
| fails another way: a rate limit, a 5xx, an error Linear does not mark | It writes `error` once. The next pass tries again. |

## github

`iod github` reflects GitHub repositories.

| Flag | Value | Default | What it sets |
|---|---|---|---|
| `-repo` | `<org>/<name>` | | a repository to reflect. The flag is repeatable, and one value may hold several, separated by commas. |
| `-settled` | `<n>` | `50` | the number of closed issues, pull requests and discussions that the iod keeps for each repository |
| `-runs` | `<n>` | `50` | the number of workflow runs that the iod keeps for each repository |
| `-comments` | `<n>` | `200` | the number of comments that the iod keeps for each repository |
| `-max-open` | `<n>` | `2000` | The iod refuses a repository that has more open items than this. |
| `-discussions` | | `false` | The iod reflects discussions. |
| `-tree` | `[<name>=]<path>` | | a directory an open pull request reports when it changes it, as `changes.<name>`: the identity of the directory's contents at the head, present where they differ from the directory at the head's merge base with the pull request's base. Against the merge base, so a pull request branched before the base's copy changed changes nothing. Read once per head and kept. The name defaults to the path's last segment. The flag is repeatable. |
| `-out` | | `false` | The iod also acts back: see [The out half of github](#the-out-half-of-github). |

| Environment | What it is |
|---|---|
| `GITHUB_TOKEN` | a fine-grained PAT. It is read-only on **Contents, Metadata, Issues, Pull requests and Actions** of the repositories, and on Discussions too with `-discussions`. With `-out` it also needs write on **Issues and Pull requests**, on **Contents** to merge, and on **Commit statuses** for a check. Required, unless the app below is set. |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` | a GitHub App's id, its installation's id, and its private key as PEM. The iod authenticates as the installation: it signs a JWT with the key, exchanges it for an installation token, and mints another shortly before that one expires. A token GitHub refuses (401), because the iod's clock runs slow or the token was revoked, is minted again and the request sent once more. A 401 a fresh token gets too is believed. What the out half does is then the app's (`<slug>[bot]`), so it can review a pull request a person opened. The three go together, and some but not all of them is refused. Given with `GITHUB_TOKEN`, the app is used and the token is ignored. The app needs the permissions the token would. |
| `GITHUB_WEBHOOK_SECRET` | the signing secret of the webhook. Without it, `POST /github/webhook` refuses every delivery. |
| `GITHUB_API_BASE` | the root of the API, for GitHub Enterprise Server |

Contents is easy to omit from the token, and it is the first thing that the iod reads. Without
it, a reflection fails on `git/ref/heads/<branch>` with
`403 Resource not accessible by personal access token`.

```
iod github -repo <org>/<name>   a repository to reflect (repeatable, comma-separated)
               -settled <n>         closed issues, PRs and discussions kept per repo (default 50)
               -runs <n>            workflow runs kept per repo (default 50)
               -comments <n>        comments kept per repo (default 200)
               -max-open <n>        refuse a repo with more open items than this (default 2000)
               -discussions         reflect discussions (default false)
               -out                 also act back: comment, open, merge, review, close (default false)
```

In the repository, set Settings → Webhooks to `https://<host>/github/webhook`, with the
content type `application/json`.

### What github reflects

| Entity | Payload |
|---|---|
| `github.<org>.<name>.repo` | `repo, org, name, private, archived, fork`, and `defaultBranch, defaultBranchSha, cloneUrl, sshUrl, description, topics, url` |
| `github.<org>.<name>.issue.<n>` | `repo, number, title, state, comments`, and `author, url, createdAt, updatedAt, closedAt, stateReason, body, milestone, labels, assignees` |
| `github.<org>.<name>.pr.<n>` | `repo, number, title, state, draft, merged`, and `author, base, head, headSha, checks, url, …, labels, assignees`; an open one also `changes.<name>` for each `-tree` directory it changes |
| `github.<org>.<name>.discussion.<n>` | `repo, number, title, state, answered, comments`, and `author, category, url, …` |
| `github.<org>.<name>.run.<id>` | `repo, id, status, runNumber, attempt`, and `workflow, event, conclusion, branch, headSha, url, startedAt, updatedAt, actor` |
| `github.<org>.<name>.comment.<n>.<id>` | `repo, on, id, kind`, and `author, authorId, body, url, createdAt, updatedAt`. `authorId` is GitHub's number for the account, which a rename does not change, and an [account binding](perms.md#accounts-in-other-systems) keys on it. |

**A repository is two levels of the address**, because it is two levels at GitHub. Everything
that `signadot/verse` reflects stands under `github.signadot.verse`. A number is quoted where
it is written: `github.signadot.verse.pr."42"`.

The iod claims `github.<org>.<name>` for each repository. The claim is one address, so
`acme.app` and `acme.app2` are apart and nothing more must be said. The install writes an
owner record at that address for the installer.

**The iod covers the issues, the pull requests and the discussions twice.** One cover is the
open items, complete. The other cover is the closed items, as a window of the most recent
`-settled`. An item that closes appears in the second cover with its new state. It ages
there, and then it leaves.

A repository with more open items than `-max-open` is an error. The iod does not cut the list
short, because a partial list under a complete cover tombstones what the iod failed to read.
[Two covers for a set](#two-covers-for-a-set) gives the reason for the two covers.

**Runs are a window and nothing else**, and one cover reaches the whole window. A run may
still be in progress when more than `-runs` newer runs exist. The run then leaves the window,
and the iod tombstones it. A charter that waits for the conclusion of that run never sees one.
A larger `-runs` prevents this.

Match `conclusion`, and not a transition. A run goes `queued → in_progress → completed` in
less than a minute, and one reflection often holds all three as one write.

**Comments are entities, and a window**, like the messages of Slack. The iod keeps the
`-comments` with the most recent updates, and tombstones what leaves the window. A
conversation is a stream with no bound, and a store that holds all of one is an archive.

The iod reads two listings of the whole repository:

- conversation comments, which an issue has, and which the discussion of a pull request *is*;
- review comments on a diff.

So comments cost two conditional requests for each reflection, and not one request for each
item. `kind` says which of the two a comment is.

A comment stands under the item that it is on (`….comment."7"."900"`). So "the comments on
#7" is an address, and not a predicate over the repository. `on` carries that number.

The join is by address, and not by a field that names a kind.
`github.<org>.<name>.issue.<n>` and `github.<org>.<name>.pr.<n>` are two addresses, and
exactly one of them exists.

**An issue carries a count of comments, and a pull request does not.** The count comes from
the listing of issues. The iod reads a pull request from the listing of pulls, which has no
count. The comments themselves answer what the field is for.
[The comment count](#the-comment-count) gives the reason.

**The state of CI is a field on the pull request.** The iod derives `checks` from the runs
that it already read, by the sha of the head. So the field costs no request. A pull request
whose head has no run in the window carries no `checks` field.

**Discussions are a separate cover, and they are off by default.** With `-discussions`, a
repository that has discussions costs two requests that count against the rate limit, at
each reflection. This limits one token to about forty repositories. A repository that does
not have discussions still costs nothing.
[Discussions are off by default](#discussions-are-off-by-default) gives the reason.

In three cases the iod drops the covers of the discussions *with* the entities:

- discussions are switched off;
- the repository has the feature disabled;
- the read is refused.

The iod then leaves what stands in the store. It does not read an empty answer as "there are
none". So when you switch discussions off after the iod reflected some, what the iod wrote
stays and does not change. Nothing covers it, so nothing tombstones it.

### The out half of github

With `-out`, `iod github` comments on issues and pull requests, opens, merges and reviews pull
requests, puts verse's commit status on a pull request it opened, files issues, and closes either. It
reads inputs under one
address for each repository, `github.<org>.<name>.out`, and in the
[outboxes](../glossary.md#outbox) that packages declare for it. Each address is inside the
claim of its repository and outside every cover of the reflection.

| | where | written by | holds |
|---|---|---|---|
| input | anywhere under `github.<org>.<name>.out`, or in an outbox sending there | whoever asks | `to`, which is `pr.<n>` or `issue.<n>`, and `text`; or `text` and `asks` (below); or `to: branch.<name>` and `open`, with `commitStatus`; or `to: issue` and `open`; or `to: pr.<n>` and `merge` or `review`; or `to: pr.<n>` or `issue.<n>` and `close` (below) |
| status | the sibling `<name>~status` | the iod alone | `comment, url, postedAt, updatedAt, posted`, and `error` while an error stands. `comment` is the id of the comment, and `posted` is the fingerprint of its text. With `asks`, also `handled`: the replies acted on. With `commitStatus`, `commitStatus.<context>`: `state`, `sha` and `at`. |

A pass runs every `-out-every`, as the principal of `-as`, which is `agent:github-io` by
default, proving itself with `-token-file` where it has one.

**A question asked on a pull request.** An input carrying `asks`, one
[question](../glossary.md#question) id or a list, keeps a comment that says what each question
asks and how to answer it: a comment on the pull request saying `/verse <option>` on a line of
its own, with any words after the option kept as what the answerer said. Each pass reads the
replies after the iod's own comment, as the iod itself reflects them under the pull request, and
answers one **as its author**: the author's GitHub account
is looked up for who they are ([accounts in other systems](perms.md#accounts-in-other-systems)),
and the answer is made as that person with the account beside it, which a verse that verifies
people admits through the binding and records on the ballot
([a person relayed by a trusted iod](perms.md#a-person-relayed-by-a-trusted-iod)). It is never
made as the io. An author verse does not know, or a command naming no option the question
offers, is told so in one reply and not again. A reply that says `/verse link <nonce>` on a line
of its own is the author proving the account is theirs (`verse account -link github -account
<login>` printed it): the iod reports to the verse, as itself, that this account said it
(`POST /accounts/github/heard`), and tells the author what became of it, once. The comment is redrawn as the question settles.
A reply reaches the reflection on the comment's delivery, which also asks the out half for a pass,
or on the resync; a reply older than the in half's comment window is not in the reflection and
is not seen.

| The input | What the pass does |
|---|---|
| is new | It writes a comment on the pull request or issue. GitHub numbers both in one sequence. |
| has a text that changed | It updates the same comment. It does not write a second comment. |
| has a text that changed, and its comment was deleted (404) | It comments again. |
| has the text that the comment carries | Nothing. |

**A pull request for a branch.** An input that carries `open` asks for a pull request from a
branch:

```tony
to: branch.verse/repo-charter-review-x     # everything after the first dot is the branch
open: {base: main, title: "…", body: "…"}  # base defaults to the repository's default branch
```

Its status holds `number, url, postedAt, updatedAt, posted`, and `adopted: true` when a person had
already opened a pull request from that branch.

| The input | What the pass does |
|---|---|
| is new, and the branch has an open pull request | It adopts that pull request. Its title and body stay its author's. One the iod opened itself, whose status was lost, is recorded as opened and kept in step. |
| is new, and the branch has none | It opens one, from the branch into `base`. |
| has a title or body that changed | It updates the pull request it opened. An adopted one is left as its author wrote it. |
| has the title and body the pull request carries | Nothing. |

An `open` input may also carry what a `pr.<n>` input carries, for a charter that cannot know the
number before the pull request exists: `text` and `asks` keep the question's comment on the pull
request it opened and hear the answer there (the comment's fields stand beside the pull request's
as `comment`, `commentUrl`, `commentPosted`, `commentAt`); `merge` merges it at the head decided
on, as below; `closed: true` closes it unmerged, once. The iod never merges or closes a pull
request it adopted.

**Commit statuses on the pull request.** An `open` input carrying `commitStatus` keeps verse's
commit statuses on the pull request's head, linking to the pull request. It maps GitHub's
context, the name a status stands under on the page, to the status asked for under it, so two
things said about one pull request stand side by side and neither overwrites the other:

```tony
commitStatus:
  verse:         {state: pending, description: "waiting for an answer", sha: <head>}   # sha defaults to the head GitHub reports
  "verse/check": {state: success, description: "installs cleanly over what runs"}
```

`state` is `pending`, `success`, `failure` or `error`. Its status holds `commitStatus.<context>`:
`state`, `sha`, `at` and `posted`. The same state, description and sha again costs nothing; a
changed one replaces the status under that context. The App needs `statuses: write` for it
([the GitHub App](../../deploy/README.md#the-github-app-signadot-verse)). A status GitHub refuses,
such as one an App without `statuses: write` asks for, is `error` under its context and does not
fail the input; a refusal is not asked again until the status asked for changes.

**An issue.** An `open` input with `to: issue` files an issue, once, and keeps its title and body
in step from then on; `text`, `asks` and `closed: true` then apply to it as to a pull request the
iod opened. `closed: false` reopens it where the iod closed it, its status saying
`reopenedAt`: one standing issue a charter opens while something is wrong and closes when it is
right, as many rounds as it takes. It is how a charter reports what went wrong where the repository's people read, with no
number to know before the issue exists:

```tony
to:   issue
open: {title: "charters: the install of <tree> failed", body: "…"}
```

**A merge at a sha.** An input that carries `merge` asks for a pull request to be merged at the
head it was decided on:

```tony
to: pr.42
merge: {sha: <head>, method: merge}   # merge, squash or rebase; merge by default
```

Its status holds `merged: true`, the merge commit as `commit`, `head` and `mergedAt`.

| The pull request | What the pass does |
|---|---|
| is open, and its head is `sha` | It merges it, with GitHub's own check that the head is still `sha`. |
| is open, and its head is another commit | It refuses, naming both: a head that moved is a different change. |
| is already merged | It records `merged: true` with no `commit`. That is how a merge made around verse shows. A merge the iod made whose status write was lost shows the same way, because the next pass cannot tell the two apart (git-issue nj9fce9dh, open). |
| is merged, by an earlier pass | Nothing, and no request to GitHub. |

**A close.** An input that carries `close` asks for an issue or a pull request to be closed:

```tony
to: pr.42        # or issue.42
close: {}
```

Its status holds `closed: true`, `closedAt`, and `closedBy`, the account that GitHub says closed it.
A pull request is closed unmerged.

| The item | What the pass does |
|---|---|
| is open, and is the kind that `to` names | It closes it. |
| is the other kind (`to: pr.7`, and #7 is an issue) | It refuses, naming what #7 is. GitHub numbers both in one sequence, so a wrong kind is a wrong item. |
| is already closed | It records `closed: true` with `already: true`. That is how a close made around verse shows. |
| is closed, by an earlier pass | Nothing, and no request to GitHub. |

**A review.** An input that carries `review` asks for the iod's own review on a pull request,
which is how the state of an ask is shown in GitHub:

```tony
to: pr.42
review: {event: request-changes, body: "an objection stands: …"}   # or approve
```

Its status holds `review` (its id), `event`, `url` and `reviewedAt`.

| The input | What the pass does |
|---|---|
| is new, or its event or body changed | It posts a review. A later review supersedes the iod's earlier one, since GitHub counts a reviewer's latest. |
| says what the iod's latest review says | Nothing. |

GitHub refuses a review from the pull request's own author, so the iod cannot review a pull
request it opened. It also refuses `request-changes` without a body, which the iod refuses first.

`{event: dismiss, body}` dismisses every request for changes the iod left standing on the pull
request, not only its latest, with `body` as the reason GitHub shows; the status says how many,
`dismissed`. Where main's protection requires approving reviews, the iod's request for changes
blocks the merge whatever other approvals the pull request has, and dismissing it lifts the
block.

**Several things on one pull request.** A `pr.<n>` input that carries `commitStatus`, or `review`
together with `text` or `asks`, keeps all of them on that pull request at once: the question's
comment (its facts as `comment`, `commentUrl`, `commentPosted`, `commentAt`), the iod's review
(`reviewEvent`, `review`, `reviewPosted`, `reviewedAt`), and its commit statuses
(`commitStatus.<context>`). It is how a question is asked on a pull request somebody else opened
and the merge held until it is answered:

```tony
to: pr.42
text: "The charters change here."
asks: <question id>
review: {event: request-changes, body: "verse: waiting for an answer to the question below"}
commitStatus: {verse: {state: pending, description: "waiting for an answer"}}
```

A comment that cannot be made fails the input and is tried again. A review or a status GitHub
refuses is said beside the rest (`reviewError`, `commitStatus.<context>.error`) and does not stop
the comment, whose replies are where the answer comes from.

**A refusal is not asked again.** GitHub may refuse a request with a 4xx other than a timeout or a
rate limit, or the iod may refuse an input itself: a `to` that is no target, a missing title, a head
that moved, the wrong kind. Either way the status records `error` with `failed: permanent`, and the
input is offered again only when it is edited
([A refusal is permanent](io.md#what-is-asked-and-what-became-of-it)). A rate limit is not a
refusal: a 429, or a 403 that says so (`x-ratelimit-remaining: 0`, `retry-after`, or GitHub's
words). Neither is a 5xx. Both are tried again on the next pass.

## Deploying them

The manifests are `deploy/git-io`, `deploy/git-issue-io`, `deploy/slack-io`,
`deploy/linear-io` and `deploy/github-io`: one for each of the five ios. A setting of its own
switches each manifest on, such as `-e slackIO=true`, or
`-e githubIO=true -e githubRepos=org/name`. The manifests carry the label `tier: iod`, and
`make ship-ios` selects on that label.

A readiness probe asks each iod `GET /io`. This is the route by which an iod says what it is.
So a pod is ready exactly when it can answer what verse asks it.

[deploy/README.md](https://github.com/signadot/verse/blob/main/deploy/README.md) describes
the credentials, the ingress and the install.

The install stays a command that somebody runs. An install carries a claim, and verse checks
the claim against what already stands in the store.

## Why it is this way

### The root is a flag

Operators deploy more than one io. Whoever runs the iods decides how to divide the work
between them: by credential, by budget, or by which repositories are busy. That division has
no reason to follow the way the external system is organised.

A root of its own costs every rule a literal segment, and only the operator who chose the
root pays that cost. For this reason the root is a flag, and not a part of the form of every
address.

### Three ref spaces are named

`for-each-ref` with no pattern lists every ref, and a mirror maps every ref that the remote
has. So `iod git` asks for `refs/heads`, `refs/tags` and `refs/remotes` explicitly.

On GitHub the other refs are `refs/pull/<n>/head` and `refs/pull/<n>/merge`, and
`refs/git-issues/*`, which is the namespace of git-issue.

The merge refs are the reason, and the number of refs is not. GitHub creates the merge refs
again whenever the base branch moves. With them, every fetch after a push to main changes
many shas, and the store takes writes at every reflection although nothing happened.

### Every trailer key is reflected

`iod git` has no allowlist of keys. It cannot tell a key that means something from an
accidental key better than the repository can.

A trailer block is a *heuristic* over the last paragraph of a message. A message that only
ends with something of the form `Status: proposal` gives the key `Status`, in the parser of
git itself too.

So the convention belongs in the **namespace** of the key. To match `Verse-Task` is safe. To
match a bare `Status` is not safe.

### A separate subcommand

`git-issue` is a subcommand of its own, and it does not add kinds to `git`. The two answer
different questions about one directory: what the repository *is*, and what somebody *filed*
in it.

An operator may want the state of the branches without a tracker. Another may want a tracker
without fifty commits in each reflection. Each says so by running one subcommand and not the
other.

### Status comes from the ref

To close an issue is to *move* its ref, and the move keeps the history of the issue. The move
leaves a `status` field in the tree. An issue that somebody edited by hand, or migrated in
part, can hold a `status` field that disagrees with the ref.

The namespace of the ref cannot be wrong, so the `status` of the entity is the answer of the
namespace.

### The issues are covered once

`iod github` covers issues twice: the open issues complete, and the closed issues as a
window. The closed issues of a GitHub repository have no bound, and every page costs a
request against a budget.

For `iod git-issue`, the listing is one `git for-each-ref` over a local directory. It lists
every open and every closed issue with one process and no network. So the answer is complete,
and one cover is correct.

The iod reads every blob of a reflection in one `git cat-file --batch`. The other way is two
`git show`s for each issue.

### The window is covered

The history of a repository is finite and does not change, so `iod git` keeps every commit
and covers none. The history of a channel is a stream with no bound. So `iod slack` covers
the window, and tombstones a message that leaves `-depth`.

### The people are named by email

The `waitingOn` of a question holds principals. If a charter carried Slack ids, somebody
would maintain a table that maps a principal to a Slack id. That table would be stale at the
moment when a question most needs to reach somebody.

### A message is text

Text renders wherever Slack renders: in the notification of a phone, in a client that draws
no blocks, in a search result. A block kit layout renders in fewer places. In text, a gate
with five options is five ids in a row.

### How much of a reply is read

People talk under the messages of the io. No reading of their sentences may let a
conversation answer a question by accident. So a reply to the thread must say an option, in
one word.

A sentence that is addressed to the bot by name is written to the iod, and its writer means
the iod to act on it. So the iod reads the sentence.

### A project is owned one level down

A Linear project belongs to several teams at once, so its position gives it no single owner.
No team can claim the project itself. Two iods over two teams would each tombstone what the
other wrote.

### Two covers for a set

`iod git` keeps history, and `iod slack` keeps a window. Neither fits a set of issues. The
set has no bound, and its *live* part has a bound. So the iod covers the live part complete,
and the closed part as a window.

### The comment count

That an issue has a count and a pull request has none looks inconsistent, and it is not. The
iod keeps a window over the issues and a separate window over the pulls. So a closed pull
request can be inside one window and past the end of the other. A count on the pull request
would read 0 in that case, and would change as items update.

### Discussions are off by default

Every REST read of `iod github` is conditional. A 304 does not count against the rate limit,
so a repository where nothing happened costs nothing.

Discussions are the exception. They are GraphQL, and GraphQL carries no ETag. So discussions
are all of what a quiet repository costs.

Discussions are opt-in for each repository at GitHub, so most repositories have none. An iod
that asked every repository would use the whole budget of a quiet repository to learn that it
has none.

With `-discussions`, a repository without discussions still costs nothing. The field
`has_discussions` comes with the read of the repository. That read is conditional, and it is
free when nothing changed. So the iod skips the GraphQL query and does not ask.
