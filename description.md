# verse.ask-get: a task reads one of its owner's asks, to see whether it was answered

A task that raised an ask with `verse-ask-create` has no way to see what became of it: the call answers the ask's id and the ask outlives the task, but the task can't read it back. Add the verse system's second verb, `ask-get` (tool `verse-ask-get`): one ask by the name it was raised under or its id, shown as `verse ask <name|id>` and verse_ask show it (its answer, `pending` until it settles, its due, its then and else and what they did).

- The runner carries it as it carries the raise: GET /asks with its own token and `Verse-Task`, admitted as the task's owner, so it reads the owner's asks as `verse ask` does. The door's relayedActs gain `GET /asks`.
- The carrier generalises from the raise to a request to verse (method, path, body), so each verse verb is a route the door names rather than a new hook.
- The name-or-id resolution and the row move out of cmd/verse into mirror, so the CLI, MCP and the tool show an ask one way.