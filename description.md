# design: agent cost — Claude through gateway keys and through developers' own runners

Status: design in progress. Nothing here is a commitment; the open gaps below are the work.

## The problem

Agent work is hard to cost: one prompt can take one model call or thousands, so metered API
tokens make every task a guess. A Claude Team seat is flat but lives on one developer's machine,
which pushes teams toward agents nobody can see or govern. The design is for verse to run both,
with permissions deciding who may spend what.

## The two paths

1. **Gateway keys on a shared runner.** `driver: llm`, a virtual key named by address
   (`virtualKey`), read as whoever named the spawn. Permissions decide who may spend and the model gateway
   caps the cost. Every action is gated (*enforced*).
2. **A developer's own runner.** A coderunner on the developer's workstation, with their own
   `claude` login, registered with `verse runner add` and restricted by perms on the runner
   entity. Who may read the runner is who may spend the seat, and the token never enters verse.
   Only the invocation is gated (*attested*).

The broker and `/verse-work` cover a worker verse cannot reach, and a person at the keyboard.

## Tested (local verse, 2026-09-24)

`fence-runner-probe.sh` (attached, with its output) passes all 13 checks:

- `verse task run --runner`: open runner, owner-only, a group `read` grant, a member dropped.
- A charter's spawn reads the runner **as the rule's owner**. It is refused when the owner of a
  subject-writing rule cannot read the runner, even when whoever wrote the subject can.
- `read-write` lets a group member repoint the runner's URL, but not change its perms or remove
  it. **Grant `read`.**

The virtual-key setup was checked by hand. With a place restricted by `--grants '[]'` and
`--child-grants '{}'` and a key node granting `read` to bo, bo's
`task run --virtual-key` spawns, and carol is refused in the permission model's own words.

## Open gaps

- A provider selection cannot choose the runner, so "a person with a seat" is not a doer it can
  pick (`provider-selection.md`, Corners).
- The `claude` binary cannot spend a per-spawn virtual key: the command driver ignores
  `virtualKey` (`taskproc.go`, default branch).
- There is no per-tool gating inside Claude. This needs an MCP tool surface (the Derive, Check,
  Do sequence served to the agent) plus `call_agent` as a real spawn.
- A refused runner spawn says "none are installed".

Related: dxe2txpg (verse holding virtual keys in the store).