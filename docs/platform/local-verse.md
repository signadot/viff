# A verse on your machine, with Claude

This page sets up a verse on your own machine the way staging is laid out: the store, versed and a
runner, each a process of its own, run by [pitchfork](https://pitchfork.jdx.dev). The runner offers
scripts and the `claude` agent, so a spawn with `agent: {name: claude}` runs Claude Code, one step
at a time, under the autonomy you give it. Claude Code reaches the same verse through
[`verse mcp`](../reference/mcp.md).

For a first try of verse, a bare `verse up` is enough ([the README](../../README.md#quick-start)).
This page is for working with verse and Claude day to day.

## What runs, and where it lives

| pitchfork daemon | what it is | listens on, by default |
|---|---|---|
| `store` | logd and docd (`o sys up`), on the data dir | `127.0.0.1:9790`–`9793` |
| `versed` | `verse up --docd-addr … --local-runner=false`: the engine and the API, starting no runner of its own | `localhost:9797` |
| `runner` | a `runnerd` offering scripts and claude, registered with versed | `127.0.0.1:9810` |

They are declared in [`pitchfork.toml`](../../pitchfork.toml), at the root of this repository.
These are the parts of [a deployment](deployment.md), filled in for one machine.

Every address and path they use is a setting. The settings and their defaults are in
[`scripts/verse-local.env`](../../scripts/verse-local.env); yours go in
`~/.config/verse-local.env`, which is read after it, in the same `NAME="value"` form:

```sh
# ~/.config/verse-local.env: a second local verse beside the first, keeping patches for a week
VERSE_LOCAL_ADDR="localhost:19797"
VERSE_LOCAL_DATA="$HOME/.verse/localhost_19797"
VERSE_LOCAL_COMPACTION_CUTOFF="1w"
```

`setup` renders them into `~/.verse/local.env`, which each daemon's run line sources, so a
setting changed takes a `setup` and a restart of what it touches.

What the daemons read is under your home, and none of it is in the repository. With the defaults:

| path | setting | what it holds |
|---|---|---|
| `~/.verse/local.env` | | the settings, rendered: what the run lines source |
| `~/.local/bin` | `VERSE_LOCAL_BIN` | `verse`, `runnerd`, `verse-call-tool`, and the `o` this tree pins |
| `~/.config/verse-local.tony` | `VERSE_LOCAL_CONFIG` | the verse's config: who you are, its address, the issuer it signs as |
| `~/.verse/localhost_9797` | `VERSE_LOCAL_DATA` | the data dir: the store, the issuer's key (`issuer.pem`) and logd's config |
| `~/.verse/runner` | `VERSE_LOCAL_RUNNER_DIR` | the runner's `runner.tony`, its `token` (0600) and its credentials, `env` (0600) |

**The store keeps every patch for a day** (`VERSE_LOCAL_COMPACTION_CUTOFF`, `1d`, as on staging).
Past the cutoff a read at a commit is approximate, and verse refuses it, so a completion hook
whose task ran longer than the cutoff is lost. `setup` writes the cutoff into the data dir's
`logd.verse.tony`, which says where it came from; the store reads it when it starts.

Your `~/.config/verse.tony`, the config of whichever verse the CLI reaches by default, is not
touched: the local verse is reached with `VERSE_CONFIG=~/.config/verse-local.tony`.

## What you need

- Go, as for any build of this repository.
- [Claude Code](https://claude.com/claude-code) installed (`claude` on your `PATH`, in
  `~/.local/bin` is fine) and logged in.
- [pitchfork](https://pitchfork.jdx.dev), installed as
  [its own instructions](https://pitchfork.jdx.dev/installation) say.
- On Linux, bubblewrap (`bwrap`), which the runner confines each task with. On Ubuntu 24.04 it needs
  an AppArmor profile, since unprivileged user namespaces are restricted there:

  ```sh
  sudo tee /etc/apparmor.d/bwrap >/dev/null <<'EOF'
  abi <abi/4.0>,
  include <tunables/global>
  profile bwrap /usr/bin/bwrap flags=(unconfined) {
    userns,
    include if exists <local/bwrap>
  }
  EOF
  sudo apparmor_parser -r /etc/apparmor.d/bwrap
  bwrap --ro-bind / / --dev /dev --proc /proc --unshare-net true && echo ok
  ```

  The profile lets bubblewrap make the namespaces it confines tasks in, which is what the
  restriction exists to stop programs doing, so it is a choice; `VERSE_SANDBOX=none` (below) is the
  other.

## Setting it up

```sh
scripts/verse-local.sh setup
```

`setup` renders the settings, builds the binaries from this tree, and writes the store's logd
config. It writes what is missing of the config, the data dir's key, and the runner's config and
token, and it adds `verse mcp` to Claude Code as `verse-local`, acting as your git email. What
was there already it keeps, and says so. It checks that the config and `runner.tony` name the
addresses the settings do, and stops where they do not, since they are yours to edit. Run it
again after a pull, which rebuilds `verse` and `runnerd` together, as `verse` requires, or after
changing a setting.

The runner's credential for Claude is yours to make, once:

```sh
claude setup-token        # prints a long-lived token for your subscription
printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\n' '<the token>' >> ~/.verse/runner/env
chmod 600 ~/.verse/runner/env
```

The runner hands it to each claude task, with a Claude config dir of the task's own. Without it,
each task uses your own login and the `HOME` all of them share.

Then start, and run `setup` again: the runner's registration with versed is made once versed is up,
and verse keeps it in the data dir from then on.

```sh
pitchfork start -l
scripts/verse-local.sh setup
VERSE_CONFIG=~/.config/verse-local.tony verse runner    # laptop, offering script and claude
```

## Day to day

From the repository the short names do; anywhere else they are `verse/store`, `verse/versed` and
`verse/runner`.

```sh
pitchfork start -l                         # the store, then versed, then the runner
pitchfork stop -l                          # all three, in reverse
pitchfork list                             # what runs
pitchfork logs store versed runner --tail  # follow the logs
pitchfork tui                              # a dashboard
pitchfork restart versed runner            # after `setup` rebuilt the binaries
pitchfork restart store                    # after changing the compaction cutoff
pitchfork restart runner                   # after editing runner.tony or env
pitchfork disable runner                   # keep it down, e.g. to run it by hand; `enable` undoes it
```

With `export VERSE_CONFIG=~/.config/verse-local.tony`, the CLI reaches the local verse:

```sh
echo '{agent: {name: claude}, prompt: "Write hello.txt containing hi, then print it",
  environment: {systems: {files: {}, commands: {}}},
  autonomy: [{match: {system: files}, then: allow}, {match: {system: commands}, then: ask}, {then: ask}]}' |
  verse task run -f - --attach
verse ps
verse question
```

The file is written at once, and the `cat` waits on you in the attach; `--attach` makes the run a
conversation, so it hands you the turn when Claude is done. Without it, the run ends after its last
step, and its questions wait in `verse question`.

## From Claude Code

`setup` added `verse-local` to Claude Code, at user scope. In a session, `/mcp` lists it, and Claude
reaches verse through its tools ([the list](../reference/mcp.md#the-tools)): "run a verse task with
the claude agent that …, and ask me before any command", "what is waiting on me in verse",
"approve it", "show me that task's transcript". It acts as you, as the CLI does; `verse mcp
-read-only` is the server that cannot write.

## More than claude

What the runner offers is its `runner.tony` ([runnerd.md](../reference/runnerd.md#configuration)),
and a change to it, or to `env`, takes a `pitchfork restart runner`; the runner says hello, and
verse reads what it offers now.

- **codex**: an `agents.codex` section, with `files`, `commands` and `question` ceilings as claude
  has, and `OPENAI_API_KEY` in `env` (or a codex login).
- **the verse agent**: an `agents.verse` section, and `PORTKEY_API_KEY` in `env`.
- **repositories**: a `repos:` section (the mirrors' directory), `git` ceilings naming the
  repositories and what may be pushed (`push: {ref: "refs/heads/verse/**"}`), and `GITHUB_TOKEN` in
  `env`. A spawn then names its checkout, `git: {repo: …, from: main, fetch: {}}`.

## On macOS

The same, but for the sandbox. macOS has no bubblewrap, so the runner runs each task unconfined,
as you: verse still holds every step to the runner's ceilings and your autonomy rows, but nothing
underneath keeps a command to what it declares, and `shellEnv` is a declaration. An unconfined
runner builds only the host's network, so a spawn says `network: {mode: open}`, and one that names
no network is refused for it. `setup` and the runner choose this by themselves; on Linux,
`VERSE_SANDBOX=none` in `~/.verse/runner/env` makes the same choice.

## When something is wrong

| what you see | why, and what to do |
|---|---|
| `pitchfork: command not found` | pitchfork is not installed, or not on your `PATH`: [its instructions](https://pitchfork.jdx.dev/installation) |
| a daemon's log: `verse-local: no ~/.verse/local.env` | the settings are not rendered yet: `scripts/verse-local.sh setup` |
| `setup`: `… does not say addr: …, which VERSE_LOCAL_ADDR does` | a setting changed, and the config or `runner.tony` that names it was written before: edit the line it names |
| versed's log: `the runnerd at … is built from verse …, and this verse from …` | only for a `verse up` that starts its own runner; here, run `setup`, which builds both |
| the runner's log: `sandbox bwrap: … Failed RTM_NEWADDR: Operation not permitted` | bubblewrap is not let make namespaces: the AppArmor profile above, or `VERSE_SANDBOX=none` |
| the runner's log: `announcing to …: 401 Unauthorized … none names this one` | it is not registered yet: `scripts/verse-local.sh setup` with versed up |
| a task fails: `exec: "claude": executable file not found` | `claude` is not on the `PATH` the runner starts with; `pitchfork.toml` puts `~/.local/bin` first |
| the runner's log: `neither CLAUDE_CODE_OAUTH_TOKEN nor ANTHROPIC_API_KEY is set` | `~/.verse/runner/env` is missing, or not `KEY=value` lines |
| `unmarshal error at listen: expected string, got Object` | an address in tony is quoted: `listen: "127.0.0.1:9810"` |
| a runner refuses versed's tokens | its `verse.url` spells the issuer differently: `localhost`, as versed's config names it, not `127.0.0.1` |
| a spawn: `no runner offers …` | `verse runner` says what each offers; a runner listed with no offers is not described: `verse runner check <name>` |
