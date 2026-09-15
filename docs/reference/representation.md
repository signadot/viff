# Representation

A charter is written in [tony](https://signadot.github.io/tony-format/), the document
language verse reads and writes. Tony is JSON's document model made easier to write —
unquoted keys and strings, optional commas, comments — since a charter is a rulebook and
rulebooks want annotating. It adds one thing the charter grammar leans on, which is *tags*: a
tag names what a value is, right where the value sits (`!glob "hotfix-*"`, `!or [ … ]`,
`!not { … }`), and that is what keeps a charter a structure the engine walks rather than a
string it re-parses.

verse reads tony, JSON and YAML, and writes tony and YAML. The asymmetry is deliberate, and
it is worth thirty seconds up front:

```sh
verse charter             # tony (the default)
verse charter -o yaml     # yaml
verse charter -o json     # refused — see below
curl -s localhost:9797/status                              # tony
curl -s -H 'Accept: application/yaml' localhost:9797/status # yaml
```

Write your input in JSON if you like. A `.json` charter, an `application/json` body, a JSON
payload typed on the command line: all fine, all unchanged. Valid JSON is valid
[tony](https://signadot.github.io/tony-format/), so verse simply parses it — there is no
conversion step and nothing to configure.

What verse will not do is hand JSON back. `-o json`, `VERSE_FORMAT=json` and
`Accept: application/json` are refused, the last with a 406. The reason is tags. A match's
operators are tags — `!glob "hotfix-*"`, `!or [...]` — and JSON has no tag syntax, so a JSON
rendering of a charter could not say what the charter says.

The same fact bounds JSON input: a charter written in JSON has no operators. It writes paths
of names and `*`, and plain equality predicates; an operator is written in tony (see
[the trigger model](../trigger-model.md)).

Output is colorized on a terminal and plain when piped, so `verse charter | o v` keeps
working. `-color always` forces it (for a pager), `-color never` or `NO_COLOR=1` turns it
off.

The file suffix decides what a file is read as, so `verse c -a -f charter.json`
and `-f charter.tony` both work regardless of the default. For input that names no format —
stdin, an extensionless file, a payload typed on the command line — `-ifmt` decides, then
the config default.

To change the default: `format.default` in the config file, or `VERSE_FORMAT`. `format.output`
(CLI stdout) and `format.wire` (HTTP bodies) override it separately, because wanting tony on
the wire and yaml on stdout is a reasonable thing to want. All three name a format to *write*,
so all three refuse `json`.

```tony
format:
  default: tony
  output:  yaml   # …but render CLI output as yaml
```
