# Scripts as a doer of their own, and a spawn that names no runner (ENG-1297, v3)

Plan v3 of Linear ENG-1297 (ENG-1297), as settled with Scott on 2026-10-27/28. The full plan is the issue's attachment there; this issue tracks the three steps in the repo.

One line: a spawn names its agent or carries its script, never a runner; a runner offers what its config says, the agents it names and, if it says so, scripts, each under its own ceilings and its own shell environment; every runner proves itself to its verse, the laptop's included; verse routes to a runner that offers the doer, whose last handshake stood, and whose ceilings hold the spawn.

## The problem

Scripts are a second-class doer, and `runner:` on the spawn is the patch over it:

- a script offer is a bare flag (`Offer{Script: true}`), offered unconditionally by every built-in runner, versed's embedded one included, so a spawn naming no runner runs inside versed;
- a script's ceilings and shell environment are the runner's, shared with the agent on it;
- a registration without a subject is a runner taken at its word, a third kind of principal at the door (Daniel's point 3 on PR 49).

## Steps (one PR each, in order, main green after each)

1. **The runner config, scripts opt-in, one command** (`runnerd!:`, Tier: contract). `agents:` map keyed by agent name (program keys, `shellEnv`, `settings{values,default}`, `annotations{type,default}`, `systems` per agent); `script: {shellEnv, systems}`, presence is the switch; credentials from fixed variables; `repos.api`; start-time refusals; `Describe` from the sections, `Check` refuses a doer offered twice; `runnerd -config` as the one command, refusing to start without `verse:` and a token file; deploy configs, docs, tests. Nothing in versed.
2. **Every runner proves itself** (`runners!:`, Tier: contract). `subject` required on a registration; `verified`/`unverified` go, a registration is described or it is not; the row gains `offers`; the door has one rule; every versed issues (`issuer.key` in the data dir when `auth.issuer` names none). After the PR 49 rollout on staging, or with it.
3. **The spawn names no runner; verse routes; the local runner is a spawned runnerd** (`spawn!:`, Tier: contract). `runner:` removed; routing by candidates, fit, load, handshake with fall-through; `defaultAgent`, `localRunner`; `verse up` spawns `runnerd` with a static token, adopts runner and store pair by dialing, stop route on the control face; data dir `~/.verse/<addr>`. After verse-system v0.

Out of scope: toolsets (ENG-1322), iods converging on registrations, one spec across agents.