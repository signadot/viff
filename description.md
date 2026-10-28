# The ask grammar is one: verse-ask-create reads to, label and due as the CLI and verse mcp do

`verse-ask-create` (runner/runnerd/verserunner/versetools.go) reads `to`, `label` and `due` with its own parsers (listIn, labelsIn, a ParseDuration), beside the CLI's and `verse mcp`'s in cmd/verse/ask.go (answerersOf, labelsOf, dateOf). The tool takes what the MCP tool takes, so the two readings should be one: they already differ, since the CLI refuses an empty answerer (`a,,b`) where the tool drops it.

Move the grammar into a package outside `main` that cmd/verse and verserunner both use, and keep the CLI's refusals.