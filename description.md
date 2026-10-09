# gates: an ad-hoc task's gate waits on everyone — should it default to the spawner, as a conversation's turn does?

Observed on staging, 2026-10-09, while answering a codex task's gates (pvk89kvah12kry4rs1n0).

A task spawned by hand, `verse task run … --as walter@example.com`, raised `shell` and `apply_patch` gates. Each question stood on the docket with `waitingOn: [user:*, agent:*]`, and `verse task attach <id> --as nobody@example.com` answered them, accepted.

Two things combine, and only one of them is this issue:

- Staging verifies no identity at the door yet, so `--as` is self-asserted. That is the auth work (hfh7km3t), parked for after launch, and not this issue.
- A task's own gate names no answerers. By the rule in docs/perms.md, a question that names nobody and is about nothing is anyone's to answer. So an ad-hoc run's gates are held out to every person and every agent, and the spawner, who typed the prompt and whose run it is, is one answerer among all.

The conversation's turn already does the other thing: the loop poses it with `Answerers: [$principal]` (task/loop.go, effTurn), so it waits on the person the task runs for and reaches them in Slack through `gate-ask`. A task's gate could default the same way: the spawn's principal, where the launch has one, as the default answerer of the gates its vector raises, with a charter's `answerers:` on a gate still standing over it as the policy.

What that would change:

- `verse question` for the spawner shows their own run's gates as waiting on them, and `gate-ask` DMs them, which is what a person running a task by hand expects. Today a gate that names nobody is deliberately not DM'd ("every person and so nobody to DM").
- Somebody else answering an ad-hoc run's gate becomes a resolver the spawner granted, or the owner path (the task's owner may always answer), rather than the default.
- A charter's spawn, which has no principal unless it says `as:`, is unchanged: its gates stay the docket's, as now.

Open: whether `agent:*` should stay in the default at all for a gate a person's run raises, and whether this is a per-launch default (set where `Answerers` is set for an ask's `then` and a rule's `as: subject`) or the vector's.