# Reference — commands, configuration, representation

The command surface, the config layers, and what verse reads and writes. The
[tutorial](tutorial.md) walks the same ground as a path you follow; this is the flat
version you come back to.

## Commands

```
verse
  up   [--root <dir>]                        start the monolith (engine + store + proc)
                                             the dir an llm agent reads/acts in; default: an
                                             isolated temp dir, printed at start
       [--store memory]                      opt out of docd (no daemons, no persistence)
       [--data <dir>]                        bring logd+docd up on <dir> (default: fresh temp dir)
       [--docd-addr host:port]               attach to a running docd (client face) …
       [--docd-mount-addr host:port]         … and its controller/MOUNT face
       [--o-bin path]                        the o binary to spawn daemons (default: go run pinned cmd/o)
       [--policy <url>]                      run processes on a remote controlPolicy base URL …
       [--broker      ]                      … or on an in-process dial-in broker (needs --broker-addr;
                                             publishes each process's dialURL from it) …
       [--broker-addr host:port]             … the sibling port serving this verse's broker face
                                             (/broker/<nonce>); all default off (local in-process
                                             supervisor). See docs/control-policy.md.
  status                                     engine/store snapshot
  down                                       stop the verse on --addr (it reaps its spawned daemons)

  watch  [--system --kind --since <rev>]     stream store changes (cursored)

  Each collection is addressed twice: the PLURAL lists it, the SINGULAR acts on one member.

  entities [--system <s>] [--kind <k>]       the store
  entity (e)  get <ref> · put <ref> <payload>
  gates                                      pending decisions across the fleet
  gate    approve <id> [--say <text>] · deny <id> [--say <text>]
          approve: y, yes · deny: n, no · --say: -s
  triggers                                   the installed charter
  trigger add [--file <f>|-] [file] · show <name> · remove <name>  (add rejects a duplicate name)
  processes (ps)                             every process, whatever runs it
  process run <task…> [--attach] [--wait] [--driver scripted|llm]  (honors policy; mirrored)
          get <id> · attach <id> · inbox <id>            operator vantage (any backend):
          cancel <id> · say <id> <text…>
          approve|deny <id> <decision> [--say <text>]    (also y|yes, n|no)
          do complete|fail|note <id> [text] --as <identity>   executor vantage (dial in):
          do gate <id> <name> --as <identity> [--reason <t>] [--wait]   raise a gate up
  flows                                      the distinct subjects with live work
  flow    show <addr> · pause <addr> · resume <addr> · cancel <addr> · say <addr> <text…>
          <addr> addresses live processes by the subject they are about: demo:pr:1 (one),
          demo:pr:* (a kind), demo:*:* (a system) — a flow is a query, not a stored key
  goals                                      status / waitingOn / outcome, per goal
  goal    add <name> --done-when <formula> [--subject <ref>] [--deadline <dur>]
          add --file <f> (full record incl. owned triggers) · remove <name>
          pause|resume|cancel <name> · say <name> <text…>

  docket                                     list open motions (votes + provider selections)
  vote    <motion> <choice> --as <who>       cast a ballot (approve|reject, or a select-option)
  providers                                  list the provider registry (provider:<capability>:<id>)

  connect [path] [--task <t>] [--driver <d>]  federate a tree (own scope): scoped metadata +
          [--interval <dur>]                  content controller + a validator per file
  disconnect                                  drop the --scope overlay (else remove local:* entities)

  global:  --addr <host:port>   --scope <name>
           --ofmt, -o <tony|yaml>  output format (default tony; verse renders no json)
           --ifmt, -i <tony|json|yaml>  input format for stdin / extensionless files
           --color auto|always|never   colorize (auto = a terminal only)
```

Flags may appear before or after positionals (`gate approve <id> --say ok` works).

## Configuration

Defaults are resolved through layers, each overriding the one before:

1. `/etc/verse.{tony,json,yaml}` — system base
2. `~/.config/verse.{tony,json,yaml}` — per-user; it **patches** the system file (a merge,
   so you set only the keys you want to change; everything else falls through)
3. environment variables
4. CLI flags

**A config layer's format comes from its file name** — `.tony`, `.json`, `.yaml` or `.yml`
— and the two layers need not agree: `/etc/verse.json` patched by `~/.config/verse.tony`
merges fine, because both are parsed to the same tree before the merge. A config file
cannot declare its own format, so its name declares it. Two files for one layer is an error
rather than a silent precedence rule. `VERSE_CONFIG=<file>` replaces both layers outright.

A missing file is simply an absent layer. Example `~/.config/verse.tony`:

```tony
addr:  "localhost:9797"
scope: "me"
llm: {
  model:      "gpt-4o"
  apiKey:     "pk-…"
  virtualKey: "vk-…"
}
```

`addr` and `scope` are read by every command (they say which verse to talk to). The `llm`
block, however, is read by the **server** (`verse up`) — it hosts the process runtime, so
that's whose config/environment the agent driver reads for its model + creds; setting it for
a `process run` *client* does nothing. The equivalent env is `OPENAI_MODEL`,
`PORTKEY_API_KEY`, `PORTKEY_VIRTUAL_KEY`, `PORTKEY_BASE_URL` (which override the file).
Precedence for any value is, low → high:
`/etc/verse.tony` → `~/.config/verse.tony` → environment → CLI flag.

**The driver is chosen per spawn.** A trigger's `spawn.driver`, `connect --driver`, and
`process run --driver` each select `scripted` or `llm`; an empty driver uses the default
(`scripted`, or `llm` when creds are configured). Requesting `llm` with no configured creds
fails **that** process (status `failed`, with a message telling you to set the creds) —
the engine keeps running.

## Representation

verse **reads** tony, JSON and YAML, and **writes** tony and YAML. The asymmetry is
deliberate, and it is worth thirty seconds up front:

```sh
verse triggers             # tony (the default)
verse triggers -o yaml     # yaml
verse triggers -o json     # refused — see below
curl -s localhost:9797/status                              # tony
curl -s -H 'Accept: application/yaml' localhost:9797/status # yaml
```

**Write your input in JSON if you like.** A `.json` charter, an `application/json` body,
a JSON payload typed on the command line: all fine, all unchanged. Valid JSON is valid
[tony](https://signadot.github.io/tony-format/), so verse simply parses it — there is no
conversion step and nothing to configure.

**What verse will not do is hand JSON back.** `-o json`, `VERSE_FORMAT=json` and
`Accept: application/json` are refused, the last with a 406. The reason is tags. A formula
is built from them — `!quorum 2`, `!until [...]` — and JSON has no tag syntax, so
rendering one means rewriting it as a single-key object: `{"quorum": 2}`. That is
unambiguous where the surrounding keys are a closed set, and NOT unambiguous inside a
match's `fields`, where a payload key may itself be named `or`. A rendering that is
faithful for most of the model and quietly wrong for the rest is worse than one honestly
not offered.

Reading is unaffected by all of this, including the single-key spelling: a charter that
writes `{"quorum": 2}` loads exactly like one that writes `!quorum 2`. That is how you
reach an operator from JSON, and it is why JSON stays a first-class way to write a charter
even though it is no longer a way to read one back.

With one exception, for the same reason the rendering is refused: the single-key spelling
is only unambiguous where the keyspace is closed, so it is *not* applied inside a payload
predicate, where a key may itself be named `or`. Plain equality predicates work in JSON;
an operator over a payload needs tony (see [the trigger model](trigger-model.md)).

Output is colorized on a terminal and plain when piped, so `verse triggers | o v` keeps
working. `--color always` forces it (for a pager), `--color never` or `NO_COLOR=1` turns it
off.

The **file suffix decides** what a file is read as, so `verse trigger add -f charter.json`
and `-f charter.tony` both work regardless of the default. For input that names no format —
stdin, an extensionless file, a payload typed on the command line — `--ifmt` decides, then
the config default.

To change the default: `format.default` in the config file, or `VERSE_FORMAT`. `format.output`
(CLI stdout) and `format.wire` (HTTP bodies) override it separately, because wanting tony on
the wire and yaml on stdout is a reasonable thing to want. All three name a format to WRITE,
so all three refuse `json`.

```tony
format:
  default: tony
  output:  yaml   # …but render CLI output as yaml
```
