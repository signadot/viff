# mcp.md: the sample shape reads narrower than it matches, and nothing says where a stdio server stands

From Daniel's review of PR #7 (pullrequestreview-5864267561), in `docs/systems/mcp.md`:

1. The sample ceiling `{tool: save_issue, arguments: {team: ENG}}` reads as "create issues in ENG", but arguments match open and Linear's `save_issue` updates the issue an `id` names, so it admits `{team: ENG, id: <any>}`. The matching is right and stays; the document teaches the reading. The samples use `!and [{team: ENG}, !not {id: null}]`, with a call-out that `!not` applies to its value: `id: !not null` asks for an `id` no value can be, and admits no call. Checked against `pkg/systems`: the `!and` form admits `{team: ENG, title: x}` and refuses `{team: ENG, id: ENG-1}`; a spawn restating it is within such a ceiling, and a bare `{team: ENG}` is not.

2. The first example names `create_issue`, which Linear's server has not: the tool is `save_issue`.

3. His question on stdio servers. The document said only, at the end of the configuration, that servers over stdio are not run. It now says where one stands, in a section of its own with a pointer from the top: the runner starts none, so a stdio server's tools are not a task's tools and no call of one is decided; a task can run one as a command (its own shell, or `secret-exec`), which gives the agent a process to speak JSON-RPC to, none of the server's tools, and a decision on the command and not on each call.

Offering a stdio server's tools through this system, gated as a remote server's are, is a design of its own and has its own issue.

Docs only.
