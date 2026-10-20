# spawn: environment and autonomy, systems with attributes and shapes, network as a system

Plan for the change agreed with Scott (call, 2026-10-20) on top of main (#24, 40934c34; branch `verse/environment` from 55af9728). One PR, each commit green.

## The spawn

```yaml
prompt: "Make the failing check on $subject pass."    # what: exactly one of prompt or script
agent: {name: verse, model: claude-sonnet-5, effort: high, annotations: {...}}   # who: optional; absent = runner's default agent
# person: user:ana@example.com                        # later: excludes agent
answer: {...}
environment:
  vars: {PR: $subject.value.number}                   # today's doer.with.env
  systems:                                            # reach, fixed at launch
    network: {mode: allow, tcp-client: [{host: api.github.com}]}
    git:
    - repo: github.com/acme/app
      from: main
      fetch: {}
      push:
      - {ref: "refs/heads/verse/auto/*", exists: "false"}
      - {ref: "refs/heads/verse/auto/*", own: "true"}
autonomy:                                             # the dial: ordered rows, replaceable mid-run
- {match: {system: git, verb: push}, then: ask}
- {then: allow}
```

- A `script:` spawn takes neither `agent` nor `person`; an ask's `then:` body is this same shape.
- `doer`, `with`, `policy` and `withhold` go. No `autonomy` = ask on every step.
- Presence in `environment.systems` is the reach: tools derive from it; a step must be within a block and one of its shapes, at the supervisor and at the act; `Provides`, `KeepsVerbs`, `KeepsWithheld` go. An autonomy row naming a verb or attribute value the environment doesn't give is refused.

## System documents

Attributes (`unique`, `required`, `default`, `form`) and verbs with `fields` (`form`, `values`, `required` in shapes). `git` keeps #24's `exists`/`own`. A tool and a verb are one to one by name; a step's fields are not a tool's arguments.

## Network is a system

Attribute `mode: none|open|allow` (fixed), verb `tcp-client` (`host`, `port`); shapes only under `mode: allow`; autonomy rows on it allow/deny only; the egress proxy asks the runner per connection; the record notes refusals and a count.

## The runner

- Ceilings: positive blocks in the environment's form, closed by default; strict containment at install, launch and act.
- Config: `systems.<name>: {credential?, api?, ceiling: <blocks>}`; settings never published; an `agent:` section for the offer's agent properties.
- Offer: `agent: {name, model, effort, annotations}` or `script: true`; `systems.<name>: {ceiling: <blocks>}`; network modes in `systems.network`; `with` goes; `requires: [...]` for systems a doer can't run without (Codex: commands, files), refused at install when the environment leaves them out.

## Commits

1. `systems!:` document format and the ten documents rewritten.
2. `systems!:` network as a system; the egress proxy decides per connection.
3. `systems:` environment parsing and validation, pattern containment, cross-block rules.
4. `launch!, runner!:` the spawn and the launch in the new shape; `then:` bodies as spawns.
5. `task!, runnerd!:` presence is the reach.
6. `runnerd!, versed!:` the offer and the runner config; strict containment.
7. `runnerd, versed, cli!:` `verse task autonomy`, checked in versed and the runner's parent.
8. `codex:` `requires`.
9. `delivery:` push with a lease on the tip the runner decided on.
10. `cli!, deploy!, docs:` `verse task run --environment/--autonomy`, charters, deployed configs, docs, smoke. Staging: `repopulate.sh --force`, verse and runners deployed together.

Out of scope: document versions, named credentials.