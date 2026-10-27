# iods: an iod's identity is its registration, as a runner's is

An iod and a runner both prove themselves at the door with a workload token from the deployment's issuer, and verse is told which party a token is in two different places. A runner's is its registration: `verse runner add -subject` writes the subject on `verse.runners.<name>`, the door looks it up there, the write is attributed and permission-checked, and it happens at runtime (pb6fwh8gh12krmb7vhn0). An iod's is config: `auth.iod.accounts` maps a subject to the iod, copied to `verse.identity.iod.account.<sub>` at start, and `auth.iod.static` the same by a token's hash. `verse iod add` installs the iod as a source and says nothing about who it is at the door. Daniel raised this on PR #49 (2026-10-27): the reasons the runner's side gives, that runners come and go at runtime and that the write is an act of vouching with an author, hold for iods too.

## What changes

- `verse iod add -subject <sub>` and `-token-file <f>` record how verse recognises the iod on its registration (`verse.iods.<root>.<name>`), as a runner's does: the subject of its workload token, or the hash of its static token. The door looks a workload subject, or a static token's hash, up among the iod registrations and the runner registrations, one table.
- `auth.iod.accounts` and `auth.iod.static` go. `auth.iod` keeps the issuer, its audience and CA: the deployment's workload issuer, which is the runners' too, so it is renamed for what it is (`auth.workload`, say; the Verified runners page leaves the name open).
- `providers.<p>.iods` stays config: it says whose word counts for which accounts, not who anybody is.
- The identity mirror under `verse.identity.iod` goes with the config it mirrored; the registration is where the fact lives and is read.
- Deploy: `deploy/auth/*.tony` lose their `accounts:`; `repopulate.sh` registers each iod with `-subject`, as it does the runners; the auth files keep the issuer.
- Docs: `docs/reference/perms.md` (iods at the door, one table), `docs/reference/iod.md`, `docs/iods.md`, `deploy/README.md`.

## What it costs, and what to decide

- A config break for every deployment, and the iods registered again with their subjects: one rollout, versed and the registrations together, as the verified-runners rollout is.
- The door reads iod identities from the store rather than from memory loaded at start. The registrations' keep that the door has for runners (`registrations`, five seconds, forgotten at an add or a remove) serves iods the same way, and the refusal on an unread store applies: a door that could not look up whether a subject is an iod's has not found that it is nobody's.
- Who may vouch for an iod moves from the config to the record: whoever may write under `verse.iods`. Bounded, since relaying a person still needs the provider's trust list in config; an iod registered at runtime and absent from that list acts as itself and relays nobody. Whether an open verse's seed should hold `owner` beneath `verse.iods` out to everyone, as it does beneath `verse.runners`, is the same question the Runners page leaves open, with the same answer for an administered verse (the admin alone writes, everyone reads).

Depends on pb6fwh8gh12krmb7vhn0 landing first (PR #49), whose door and keep this builds on. Not a blocker for the verse system verbs (3gf9wqb3h12kr2ymtnn0).