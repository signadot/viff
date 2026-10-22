# verse mcp refuses -read-only: the command never registers its options

`verse mcp -read-only` on deploy/staging.24 (7e92489a):

    usage error: unknown option: "read-only"

and `verse mcp` lists "mcp options: (none)". `mcpConfig` declares `ReadOnly` with its `cli:` tag, but `mcpCmd` builds the command without `cli.StructOpts(cfg)` / `WithOpts`, so the flag the reference (docs/reference/mcp.md, "Reading only") and `claude mcp add verse-read -- verse mcp -read-only` name is not one the parser knows. The tests build the server with `readOnly` directly (newMCPServer), never through the command line.

Fix: register the options as every other command does, and test `-read-only` through the command line.