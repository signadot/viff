# mcp.md: the first example writes through the server the configuration makes read-only

In `docs/systems/mcp.md` the spawn example calls `save_issue` on the server `linear`. The configuration sample further down defines `linear` as Linear's read-only endpoint and `linear_w` as the full one, and the page recommends exactly that split. So the example's write goes to a server that would refuse it.

The example names both servers, one block each: its reads on `linear`, its `save_issue` on `linear_w`. That shows the split in use, and matches the sample ceiling as written.

Docs only.