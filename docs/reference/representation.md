# Representation

A [charter](../glossary.md#charter) is written in [tony](https://signadot.github.io/tony-format/).
Tony is the document language that viff reads and writes. Like JSON, it has objects, arrays
and scalars. Unlike JSON, it allows unquoted keys and strings, optional commas and comments.
A charter is a document of rules and what they need, and comments make it easier to read.

Tony adds one thing that the charter grammar uses: *tags*. A tag names what a value is, at
the place where the value stands: `!glob "hotfix-*"`, `!or [ … ]`, `!not { … }`. With tags, a
charter is a structure that viff reads as it stands. It is not a string that viff parses a
second time.

Viff reads tony, JSON and YAML. It writes tony and YAML, and it does not write JSON, on
purpose ([no JSON as output](#no-json-as-output)).

```sh
viff charter             # tony (the default)
viff charter -o yaml     # yaml
viff charter -o json     # refused — see below
curl -s localhost:9797/status                              # tony
curl -s -H 'Accept: application/yaml' localhost:9797/status # yaml
```

## JSON as input

You may write your input in JSON. Viff accepts each of these unchanged:

- a `.json` charter,
- an `application/json` body,
- a JSON payload typed on the command line.

Valid JSON is valid [tony](https://signadot.github.io/tony-format/), so viff parses it as tony.
There is no conversion step and nothing to configure.

## No JSON as output

Viff does not write JSON. It refuses each of these:

| Request | Answer |
|---|---|
| `-o json` | refused |
| `VIFF_FORMAT=json` | refused |
| `Accept: application/json` | 406 |

The operators of a match are tags, such as `!glob "hotfix-*"` and `!or [...]`. JSON has no
syntax for a tag. So a charter rendered as JSON could not say what the charter says.

The same fact limits JSON input: a charter written in JSON has no operators. It can write
paths of names and `*`, and predicates of plain equality. Write an operator in tony (see
[the charter reference](../trigger-model.md)).

## Color

Viff colors its output on a terminal, and writes plain text when the output goes to a pipe.
So `viff charter | o v` works.

| Setting | Effect |
|---|---|
| `-color always` | color, even into a pipe (for a pager) |
| `-color never` | no color |
| `NO_COLOR=1` | no color |

## Which format viff reads

The suffix of a file decides the format viff reads it in. So `viff c -a -f charter.json`
and `-f charter.tony` both work, whatever the default is.

Some input names no format: stdin, a file with no extension, a payload typed on the command
line. For that input, `-ifmt` decides. Without `-ifmt`, the default of the config decides.

## The default format

Three settings name a format to write:

| Setting | What it sets |
|---|---|
| `format.default` in the config file, or `VIFF_FORMAT` | the default |
| `format.output` | the output of the CLI on stdout. It overrides the default. |
| `format.wire` | the bodies of HTTP responses. It overrides the default. |

You may want tony over HTTP and YAML on your terminal, so the two are separate settings. All three
name a format to write, so all three refuse `json`.

```tony
format:
  default: tony
  output:  yaml   # …but render CLI output as yaml
```
