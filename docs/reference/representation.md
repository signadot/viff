# Representation — tony, JSON and YAML

A charter is written in [tony](https://signadot.github.io/tony-format/), the document
language verse reads and writes. Tony is JSON's document model made easier to write —
unquoted keys and strings, optional commas, comments — since a charter is a rulebook and
rulebooks want annotating. It adds one thing the charter grammar leans on, which is *tags*: a
tag names what a value is, right where the value sits (`!glob "hotfix-*"`, `!quorum 2`,
`!until [ … ]`), and that is what keeps a charter a structure the engine walks rather than a
string it re-parses.

verse reads tony, JSON and YAML, and writes tony and YAML. The asymmetry is deliberate, and
it is worth thirty seconds up front:

```sh
verse triggers             # tony (the default)
verse triggers -o yaml     # yaml
verse triggers -o json     # refused — see below
curl -s localhost:9797/status                              # tony
curl -s -H 'Accept: application/yaml' localhost:9797/status # yaml
```

Write your input in JSON if you like. A `.json` charter, an `application/json` body, a JSON
payload typed on the command line: all fine, all unchanged. Valid JSON is valid
[tony](https://signadot.github.io/tony-format/), so verse simply parses it — there is no
conversion step and nothing to configure.

What verse will not do is hand JSON back. `-o json`, `VERSE_FORMAT=json` and
`Accept: application/json` are refused, the last with a 406. The reason is tags. A formula
is built from them — `!quorum 2`, `!until [...]` — and JSON has no tag syntax, so
rendering one means rewriting it as a single-key object: `{"quorum": 2}`. That is
unambiguous where the surrounding keys are a closed set, and NOT unambiguous inside a
match's `value`, where a payload key may itself be named `or`. A rendering that is
faithful for most of the model and quietly wrong for the rest is worse than one honestly
not offered.

Reading is unaffected by all of this, including the single-key spelling: a charter that
writes `{"quorum": 2}` loads exactly like one that writes `!quorum 2`. That is how you
reach an operator from JSON, and it is why JSON stays a first-class way to write a charter
even though it is no longer a way to read one back.

With one exception, for the same reason the rendering is refused: the single-key spelling
is only unambiguous where the keyspace is closed, so it is *not* applied inside a payload
predicate, where a key may itself be named `or`. Plain equality predicates work in JSON;
an operator over a payload needs tony (see [the trigger model](../trigger-model.md)).

Output is colorized on a terminal and plain when piped, so `verse triggers | o v` keeps
working. `--color always` forces it (for a pager), `--color never` or `NO_COLOR=1` turns it
off.

The file suffix decides what a file is read as, so `verse trigger add -f charter.json`
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
