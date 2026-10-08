# verse/remove-broker

Branch `verse/remove-broker`, worktree `.worktrees/remove-broker`, cut from `verse/remove-connect` at 78defbcd (main e96c714d + the connect removal), 2026-10-09.

The idea, its status at removal and the next-step designs are written up in Notion under "Verse Umbrella" (page "The broker: the idea, its status at removal, and what was next"), as the connect removal was (h6hx7ddc).

## What goes

The dial-in broker whole: the runner for a worker that cannot accept an inbound connection, and everything that existed for it.

Code:

- `broker/` (the package, its tests and conformance suite).
- versed: `UseBroker`, `BrokerHandler`, `HostBroker`, `brokerFor`, `brokerRuntime`, `mintNonce` where only the broker used it, `Server.broker`, `BrokerAddr`, `brokers`; the `broker` runner name in `runnerFor`, `runnerIdentity` and the listing; `registries()`' broker half; `runnerStatus`' `dial` and `hasFace`; `relaysControl` and `QueuedAnswer` (an answer is applied where it is accepted: a local registry or a remote runtime over the wire); `GET /tasks/{id}/dial` and `GET /tasks/{id}/inbox`.
- `cmd/verse up`: `--broker`, `--broker-addr`, `--broker-advertise-addr`, `brokerFace`, the broker argument of `adminAddrFor`, the sibling listener.
- CLI: `verse task do complete|fail|note|gate`, `verse task inbox`, `brokerOf`, `brokerClaim`, `brokerInbox`, `httpPostURL` and `client.PostURL` if nothing else posts to an absolute URL.
- Records: `task.Unclaimed`, `Instance.DialURL`, `InboxItem`, `Listen.Broker`, `RunnerStatus.Dial`; the mirror's `dialURL` and `unclaimed` initial state and `delivered: false`.
- `docs/reference/examples/worker`, `.claude/skills/verse-work`.
- Deploy: the `brokerFace` knob, `broker-service.yaml`, versed's broker args and port, the tailnet device, the Dockerfile's `--broker-addr`; profiles.
- Docs: seams §5; `task-control.md` loses hosting, brokering, the dial-in protocol and the human-as-worker material; `external-tasks.md` is cut to what stands (the contract, the seam, the attached runtime); `platform/tasks-worker.md` deleted; `platform/runners-walk.md`, `runners.md`, `reference/runners.md`, `reference/tasks.md`, `reference/up.md`, `platform/commands.md`, `platform/architecture.md`, `platform/glossary.md`, `platform/perms.md`, `perms-guide.md`, `reference/perms.md`, `asking.md`, `provider-selection.md`, `glossary.md`, `README.md`, `deploy/README.md`.
- Tests that used a brokered task as the fixture for "a task that waits with a gate open" get a hosted fixture instead.

## What stays

The task contract and `task.Runner`; local and remote runners and the coderunner; `PostFrameQuestion`, which a remote runner's frames still reach; `gateResolver`, which `runner.HTTP` implements; `task.Lister`; `verse ask` and the `ask` tool, which is how a task hands the turn to a person.

## Issues this settles or moves

- jp9dy8an (the broker face serves the local runtime unchecked): the face is gone, so the hole is gone; closes with this branch.
- 97g07t23 (snapshot, events, inbox unchecked): the inbox and the dial route go; the rest stands and is not about the broker.
- f6bmet8r, 27ad56rg, mt36ses1, md0te4qc, mrm86n11, 04xtth11: the broker parts of each are moot; commented, not closed.
- ezx7sarp (ask): unchanged; it is the surviving way a task reaches a person.

## Trailers where it lands

`Tier: contract, docs`; `Breaks:` names the flags, the routes, the verbs, the fields and the runner name.