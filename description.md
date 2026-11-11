# mcp.md: the sample shape reads narrower than it matches, and nothing says how a stdio server is run

From Daniel's review of PR #7 (pullrequestreview-5864267561), in `docs/systems/mcp.md`:

1. The sample ceiling `{tool: save_issue, arguments: {team: ENG}}` reads as "create issues in ENG", but arguments match open and Linear's `save_issue` updates the issue an `id` names, so it admits `{team: ENG, id: <any>}`. The matching is right and stays; the document teaches the reading. The samples use `!and [{team: ENG}, !not {id: null}]`, with a call-out that `!not` applies to its value: `id: !not null` asks for an `id` no value can be, and admits no call. Checked against `pkg/systems`: the `!and` form admits `{team: ENG, title: x}` and refuses `{team: ENG, id: ENG-1}`; a spawn restating it is within such a ceiling, and a bare `{team: ENG}` is not.

2. The first example names `create_issue`, which Linear's server has not: the tool is `save_issue`.

3. His question on stdio servers. A task runs a stdio server itself, as a command, with `secret-exec` where it needs a credential; no bridge and no runner-side support is intended. The document says so where someone asking "how do I get verse to run my MCP server?" lands: a pointer at the top and a section of its own, rather than one clause at the end of the configuration.

Docs only.