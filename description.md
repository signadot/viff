# verse.data-get, data-put, data-rm: a task reads and writes the verse as its owner

The verse system's next verbs after the ask verbs: the data verbs, as `verse data` and verse_data, verse_data_put and verse_data_rm take them. Data is what a task working on charters needs first: what a rule sees, the state it fires on, and what a firing did.

- Tools `verse-data-get` (at, if, if_at, trim, paths, depth), `verse-data-put` (at, value, create, update, expect, grants), `verse-data-rm` (at), each taking what its MCP tool takes, as strings (a boolean as true/false, depth a number).
- Carried as GET /state, POST /entity and DELETE /entity, admitted at the door as the task's owner: relayedActs gain the three routes.
- The grammar (the read's query, the write's body and conditions, the removal's query) moves out of cmd/verse into api, and the paths walk into entity, so the CLI, verse mcp and the runner read and answer alike.
- Steps `verse.data-get`, `verse.data-put`, `verse.data-rm`, with no fields: matching an autonomy row on the address wants a pattern over kinded paths, which the step fields' patterns are not yet.