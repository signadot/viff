# A config field should be able to say where it reads itself from

Moved from `docs/working/work/config-secrets.md` (last at `0cba64af`); the note follows whole, its relative links as they were written there.


Status: a want with no design, written down because this repo already answers the same question
twice and differently, and the third answer is better than either. Nothing here runs.

## What is there

**verse's own config stores the VALUE.** `~/.config/verse.tony` holds

```tony
llm:
  apiKey:     <the secret, in the file>
  virtualKey: <the other one>
```

with a fixed environment override per field, hard-coded in `config.applyEnv`:
`<GATEWAY>_API_KEY` → `llm.apiKey`, `<GATEWAY>_VIRTUAL_KEY` → `llm.virtualKey`, `OPENAI_MODEL` →
`llm.model`, and so on. So a secret CAN be kept out of the file today — set the variable and the
override wins — but only for the fields somebody added a line for, and only under the one name
that line names.

**The coderunner's config stores the NAME of a variable.** `llm.apiKeyEnv: <GATEWAY>_API_KEY`, and
`credentials: [{name: gh-verse, env: GITHUB_TOKEN}]`, for the reason its own comment gives: a
config file is read, copied and committed, so it must carry no secret. That is a per-field
`…Env` twin, invented once per secret.

Two mechanisms, one question. The first needs a code change to cover a new field; the second
needs a schema change and doubles the field.

## What was asked for

```tony
llm:
  apiKey: !env <GATEWAY>_API_KEY
```

— a tony-level eval, so the config says where the value comes from instead of holding it, for
ANY field, with no per-field table and no twin. It subsumes both of the above: the hard-coded
override list becomes unnecessary, and `apiKeyEnv` becomes `apiKey: !env …`.

## The line that has to be drawn first, and it is not obvious

**A config file, yes. An ENTITY, no.**

A config file is read by a CLI running as the person who wrote it, so `!env` reads that person's
own environment and gives them nothing they did not already have. A charter is different in kind:
it is data anybody with write access can put in the store, evaluated by the ENGINE, whose
environment holds the deployment's credentials. `!env` reachable from a charter is a charter that
exfiltrates the daemon's secrets, and it would arrive looking like a convenience.

So whoever builds this decides where evaluation happens before deciding what the tag is called.
If it lands in go-tony as a general tag, every reader of tony inherits it and the verse engine is
one of those readers — which is the wrong default and the hard part of the design. A verse-side
eval applied only on the config-loading path has none of that reach, and is probably what this
should be.

Related: [[coding-runner-residual]] for the `…Env` twin as built, and `deploy/` for the third
answer again — Kubernetes secrets into environment variables, which is the same shape once more.